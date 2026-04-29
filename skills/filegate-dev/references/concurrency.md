# Concurrency

Filegate has many concurrent paths: the HTTP server, the change detector, the
chunked upload state machine, the job scheduler, several internal coalescers,
and the in-process event bus. Most of the bugs we've fixed in this codebase
have been concurrency bugs. Read this before adding any goroutine.

## Locking conventions

- `sync.RWMutex` for read-heavy state (`domain.Service.mu`,
  `infra/pebble/index.Index.mu`).
- `sync.Mutex` for write-only or single-purpose state.
- **Never** call externally-supplied callbacks (user functions, store methods
  that may block) while holding a service-level lock. This was the root cause
  of a deadlock class we explicitly avoid.

## Coalescing pattern (used in `dirsyncer` and `jobs.Scheduler`)

When N callers want the same in-flight operation:

```go
type flight struct {
    done    chan struct{}
    err     error
    joiners atomic.Int32 // count of late attachers — used by tests
}
```

- First caller stores a `*flight` in the map, runs the work, closes `done`,
  deletes the map entry.
- Late callers `LoadOrStore`, increment `joiners`, block on `<-done`, get the
  same result.
- The `joiners` counter exists primarily so tests can wait deterministically
  until N joiners attached before releasing the in-flight function — this
  replaces sleep-based "wait a bit and hope" patterns.

If you add a new coalescing layer, follow the same shape and add the same
counter. Verify it really exists with `grep "joiners atomic.Int32"
infra/jobs/scheduler.go domain/dirsyncer.go`.

## Bounded async work — the options

`infra/jobs.Scheduler` is the **preferred** way to run keyed, dedup'd
background work (currently used for thumbnail generation). It gives you:

- Bounded queue (`ErrQueueFull` on overflow — non-blocking submit).
- Bounded worker pool.
- Per-key deduplication via `inFlight` map.
- `Close(ctx)` cancels the worker context, drains the queue (waiters get
  `ErrClosed`), waits for workers up to `ctx`'s deadline, returns
  `ErrCloseTimeout` on overrun.

Two **other** bounded-async patterns also exist in the codebase and are fine
to follow when the Scheduler is overkill:

- **`infra/eventbus`** — bounded async event dispatch with a fixed semaphore
  controlling parallelism. No deduplication, no queue draining, just
  fire-and-forget pub/sub.
- **Per-component cleanup goroutines + semaphores** — e.g. `chunkedManager`
  in `adapter/http/upload_chunked.go` runs a single cleanup loop bounded by
  `cleanupStop`/`cleanupDone` channels and uses a semaphore for chunk-write
  parallelism. This is fine for component-internal bounded work.

What is **not** OK: free-form `go func() { ... }()` without a cancellation
mechanism, or with no upper bound on concurrency.

## Scheduler invariants you must preserve

Critical invariant: `getOrSubmit` holds `s.mu.RLock()` across the
closed-check AND the queue-send. `Close` takes the write lock before
flipping the flag. This serializes against in-flight submitters so a late
submitter cannot land a job in the queue after `Close` has drained it. That
race exists in the git history; the regression test
`TestSchedulerCloseRejectsRacingSubmissions` will catch it if it returns.

When using `Scheduler` from your own code:

- Always pass a context to `Close`, with a deadline. `context.Background()`
  is wrong — the caller has no escape if a job ignores cancellation.
- Always check `Do`'s error — `ErrQueueFull` and `ErrClosed` are normal at
  boundaries.

## Lifecycle conventions

The codebase has two `Close` shapes today, both intentional:

- **`Close(ctx context.Context) error`** — for components that own goroutines
  and where shutdown can plausibly take time (the only example today is
  `jobs.Scheduler`). The context bounds the wait.
- **`Close()` (no context)** — for components whose shutdown is bounded by
  internal cooperation (e.g. `chunkedManager.Close()` only waits on its own
  cleanup loop signaling done; `closeableHandler.Close() error` runs the
  router's pre-registered close functions in sequence). These are fine when
  the wait is genuinely short.

When adding a new component: prefer `Close(ctx) error` if any path could
block on user code or external I/O. Use parameterless `Close()` only when
you can prove the wait is bounded by your own code.

## Test-time race-window simulation

Some tests need to provoke a race deliberately (see
`TestIndexCloseRaceNoPanic`). The pattern:

1. Spawn N worker goroutines that loop on the operation under test.
2. Wait until each worker has done at least M iterations (use atomic counter,
   not `time.Sleep`).
3. THEN trigger the racing operation.

Counter-based warmup is deterministic across slow CI machines; sleeps are not.

## Channel-based test sync — the rule

```
NEVER use time.Sleep to synchronize between goroutines.
```

Acceptable uses of `time.Sleep` in tests:

- **Polling for an externally-observable condition that has no signal** —
  e.g. "wait until the eventually-consistent rescan picks up the file"
  (see `waitUntil` in `cli/serve_detector_linux_test.go`). Use a polling
  loop with a short interval and a generous deadline, and fail with a
  clear message on timeout.
- **Backoff/throttle in soak/chaos load-generation loops** — controlling
  rate of submitted operations, not waiting on a signal.
- **Filesystem mtime granularity workarounds** — ext4/tmpfs round mtimes
  to milliseconds, so two writes within the same millisecond may produce
  equal mtimes and confuse the change detector. If you write such a
  sleep, comment it clearly so the next reviewer doesn't "fix" it.
  (Currently this case is rare — most detector tests rely on size or
  content changes, not mtime alone.)

For everything else (waiting for a goroutine to start, for an event to be
processed, for a subscriber to register, for state transitions), use one of:

- `chan struct{}` closed at the synchronization point
- `sync.WaitGroup`
- `atomic.Int32` plus a tight `runtime.Gosched()` loop
- An internal helper that exposes the state under test (e.g., `inflightFor(key)`,
  `joiners` counter)

The git history shows several tests that flaked on CI for months until they
were converted to channel-based sync. Do not re-introduce the pattern.

## Inode-based reconciliation (rename-safety)

After every successful `syncSingle`, the service runs `reconcileByInode`
([domain/service.go](../../../domain/service.go)). It uses the secondary
inode keyspace (`familyInode`, format version 5) maintained automatically
by `infra/pebble.batch.PutEntity` / `DelEntity` to find every entity that
claims the same `(device, inode)` tuple as the just-synced path. For each
candidate ID other than the one we just wrote:

- Resolve the candidate's stored absolute path.
- `Stat` it. Gone (`ENOENT`) or different inode → `deleteSubtree(candidateID)`.
- Same path as the one we just synced → no-op (false-positive guard).

This is what catches external rename-within-mount on btrfs (where
`find-new` only emits the new path, never the old). Two short-circuits
keep it safe:

- `Device == 0 && Inode == 0`: skip. Mount-root entries don't carry stat
  info; we never want to "reconcile" them away.
- `Nlink > 1`: skip. The inode is legitimately referenced by multiple
  paths (hard links); removing any one of them would corrupt the index.

When you add a new write path, you typically don't have to do anything —
`buildEntityMetadata` reads `(Dev, Ino, Nlink)` from the stat result, the
Pebble batch maintains the secondary index, and `syncSingle` invokes the
reconciler. The only situation requiring manual care: if you bypass
`buildEntityMetadata` and construct an `Entity` literal (only mount roots
do this today), make sure leaving `Device`/`Inode` zero is genuinely what
you want — it disables reconciliation for that entity by design.

## Resource lifecycle

- Always `defer Close()` for things that own a resource (files, iterators,
  batches, sockets, subscribers).
- `pebble.Iterator` and `pebble.Batch` MUST be closed — leaks are fatal for
  the index over time.
- HTTP `resp.Body` MUST be drained-and-closed in SDK callers, even on error
  paths. The Go SDK's `*Raw` methods now return the response unchanged on
  4xx/5xx (relay-friendly), so callers are responsible for closing the body
  themselves.
- Cleanup goroutines (`chunkedManager.cleanupLoop`, `Scheduler.workers`)
  must be stoppable via the constructor's context, and the constructor must
  give the caller a `Close(...)` to wait for them.
