# Chunked / Resumable Uploads

For files larger than `MaxUploadBytes` (default 500 MiB), or any time you want resume / parallel chunk delivery / progress tracking.

## State machine

```
   client                                     server
   ──────                                     ──────
   POST /v1/uploads/chunked/start
   { parentId, filename, size,
     checksum, chunkSize, onConflict }
                  ────────────────────────►
                                              [optimistic conflict check]
                                              [allocate staging dir]
                                              [pre-truncate part file]
                                              [persist manifest]
                  ◄────────────────────────
                  { uploadId, totalChunks, ... }

   PUT /v1/uploads/chunked/{uploadId}/chunks/0
   X-Chunk-Checksum: sha256:...               ┐
   <chunk bytes>                              │
                  ────────────────────────►   │
                                              ├ may arrive in any order,
                  ◄────────────────────────   │ duplicates allowed if checksums match
                  { progress... }             │
                                              ┘
   PUT .../chunks/N (last one)
                  ────────────────────────►
                                              [bitset full → auto-finalize]
                                              [hash whole file, verify checksum]
                                              [authoritative conflict check]
                                              [ReplaceFile → atomic rename into place]
                  ◄────────────────────────
                  { completed: true, file: { ... } }
```

## Deterministic upload ID

The server computes the upload ID as:

```
uploadId = hex( sha256( parentId.String() + ":" + filename + ":" + checksum )[0:8] )
```

Note the **colon separators** between fields, and that the hex output is
the first 8 bytes of the digest (16 hex characters).

This means: the same `(parentId, filename, checksum)` always produces the
same `uploadId`. If a client retries `/start` with identical parameters,
it reattaches to the existing session — already-uploaded chunks are not
re-sent.

If any of the three changes (e.g. the user picked a different file with
the same name), the `uploadId` differs → fresh session.

You typically don't need to compute the `uploadId` client-side — `/start`
returns it. Only compute it yourself if you're implementing tooling
outside the SDK (e.g. an admin script that wants to find the staging
directory of a specific upload).

## Resume from interruption

```ts
// Step 1: ask the server what's missing
const status = await fg.uploads.chunked.status(uploadId);
console.log("uploaded chunks:", status.uploadedChunks);
console.log("total chunks:", status.totalChunks);

// Step 2: compute what's left
const todo = new Set(Array.from({ length: status.totalChunks }, (_, i) => i));
status.uploadedChunks.forEach((i) => todo.delete(i));

// Step 3: send only the missing ones
for (const i of todo) {
  const slice = file.slice(i * chunkSize, Math.min((i + 1) * chunkSize, file.size));
  await fg.uploads.chunked.sendChunk(uploadId, i, slice, await chunks.sha256Bytes(new Uint8Array(await slice.arrayBuffer())));
}
```

## TS — streaming upload flow

The whole point of chunked upload is to **avoid loading the whole file in
memory**. Use `File.slice` (which is lazy — the browser only reads the
backing storage when the slice is actually consumed). For the overall
file checksum on multi-GB inputs, use an incremental SHA-256 library
(e.g. `@noble/hashes/sha256`) — `crypto.subtle.digest` is one-shot and
will buffer the whole file.

Two deployment shapes:

**Server-side (Bun/Node)**: construct `Filegate` with the daemon token
and call `uploads.chunked.*` directly. **Browser**: NEVER hold the
daemon token. Compute hashes and slice the file in the browser, then
relay each chunk through your backend. The backend's handler uses the
SDK normally and forwards the chunked-upload responses unchanged via
`uploads.chunked.sendChunkRaw`. See [`relay-patterns.md`](relay-patterns.md)
for the full relay pattern.

The uploader logic itself is the same in both — only the calls fan out
through different transport layers. Server-side example:

```ts
import { Filegate } from "@valentinkolb/filegate/client";
import { chunks } from "@valentinkolb/filegate/utils";
import { sha256 } from "@noble/hashes/sha256";    // incremental hasher

const fg = new Filegate({ baseUrl, token });
const file: File = pickedFile;        // never materialize the full bytes
const chunkSize = 8 * 1024 * 1024;

// Stream-hash the whole file for the overall checksum.
const reader = file.stream().getReader();
const hasher = sha256.create();
while (true) {
  const { value, done } = await reader.read();
  if (done) break;
  hasher.update(value);
}
const checksum = "sha256:" + Array.from(hasher.digest())
  .map((b) => b.toString(16).padStart(2, "0")).join("");

const start = await fg.uploads.chunked.start({
  parentId, filename: file.name,
  size: file.size,
  checksum,
  chunkSize,
  onConflict: "error",
});

// Send chunks in parallel — order does not matter, server tracks bitset.
const total = chunks.totalChunks(file.size, chunkSize);
const concurrency = 4;
let nextChunk = 0;
const inflight: Promise<unknown>[] = [];

async function sendOne(i: number) {
  const { start: s, end: e } = chunks.bounds(i, file.size, chunkSize);
  const blob = file.slice(s, e);                  // lazy — no buffering yet
  const slice = new Uint8Array(await blob.arrayBuffer());
  const cs = await chunks.sha256Bytes(slice);
  const res = await fg.uploads.chunked.sendChunk(start.uploadId, i, slice, cs);
  if (res.completed) console.log("done:", res.complete?.file.id);
}

while (nextChunk < total || inflight.length > 0) {
  while (inflight.length < concurrency && nextChunk < total) {
    const p = sendOne(nextChunk++).finally(() => inflight.splice(inflight.indexOf(p), 1));
    inflight.push(p);
  }
  await Promise.race(inflight);
}
```

## Go — streaming upload flow

Don't `os.ReadFile` a large file — open it once, hash it streaming, then
read each chunk via `io.SectionReader`:

```go
import (
    "github.com/valentinkolb/filegate/sdk/filegate"
    "github.com/valentinkolb/filegate/sdk/filegate/chunks"
)

f, err := os.Open(srcPath)
if err != nil { return err }
defer f.Close()
info, err := f.Stat()
if err != nil { return err }
size := info.Size()
const chunkSize = int64(8 << 20)

// 1) Streaming whole-file hash — no memory blow-up.
checksum, err := chunks.SHA256Reader(f)
if err != nil { return err }
if _, err := f.Seek(0, io.SeekStart); err != nil { return err }

// 2) Start session.
start, err := fg.Uploads.Chunked.Start(ctx, filegate.ChunkedStartRequest{
    ParentID:   parentID,
    Filename:   filepath.Base(srcPath),
    Size:       size,
    Checksum:   checksum,
    ChunkSize:  chunkSize,
    OnConflict: string(filegate.ConflictError),
})
if err != nil { return err }

// 3) Send chunks in parallel via SectionReaders — each chunk reads its
// own slice from the open file without copying into a Go []byte.
total := chunks.TotalChunks(size, chunkSize)
sem := make(chan struct{}, 4)
var wg sync.WaitGroup
errs := make(chan error, total)
for i := 0; i < total; i++ {
    sem <- struct{}{}
    wg.Add(1)
    go func(idx int) {
        defer wg.Done()
        defer func() { <-sem }()
        s, e, _ := chunks.Bounds(idx, size, chunkSize)
        section := io.NewSectionReader(f, s, e-s)         // no copy
        // For the per-chunk checksum we DO need to hash the bytes — read
        // them once into a buffer of bounded size (chunkSize).
        buf := make([]byte, e-s)
        if _, err := io.ReadFull(section, buf); err != nil {
            errs <- err; return
        }
        cs := chunks.SHA256Bytes(buf)
        if _, err := fg.Uploads.Chunked.SendChunk(ctx, start.UploadID, idx,
            bytes.NewReader(buf), cs); err != nil {
            errs <- err
        }
    }(i)
}
wg.Wait()
close(errs)
for err := range errs { if err != nil { return err } }
```

The chunk buffer is at most `chunkSize` per in-flight goroutine — so worst
case `concurrency * chunkSize` of memory in use, not the whole file.

## Properties to remember

- **Out-of-order**: chunks can arrive in any order. The server uses a bitset to track receipt.
- **Duplicate-safe before finalization**: re-sending a chunk with the same
  content returns 200 with no work done. Re-sending with DIFFERENT content
  for an already-received index returns 409. **After the upload has been
  finalized**, the chunk-PUT handler returns the completed-file response
  immediately without comparing content — the upload is now an immutable
  resource and chunk replays are no-ops.
- **Auto-finalize**: when the bitset becomes full, the server finalizes immediately on the closing chunk's request. There is no separate "complete" call.
- **Crash-safe**: progress (the bitset) is persisted in a manifest file in the staging directory. After a daemon restart, sessions resume cleanly.
- **Expiry**: stale staging dirs are cleaned up by a background loop (default expiry 24h). After expiry, the `uploadId` becomes free again.
- **Conflict-aware**: see [`conflict-handling.md`](conflict-handling.md) for the start-vs-finalize check semantics.
- **Storage-aware**: if the daemon's free-space guard would be violated by the upload, `/start` returns `507 Insufficient Storage`.

## Header — chunk checksum

Every chunk PUT should include `X-Chunk-Checksum: sha256:<hex>`. The server verifies the chunk against this before accepting it. The TS SDK passes this automatically; with raw HTTP, set it yourself.

## Server-side staging

Chunks land in `<mount>/.fg-uploads/<uploadId>/data.part`. This is on the same filesystem as the final destination, so the rename-into-place at finalize is atomic (no cross-device fallback needed).
