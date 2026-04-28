# Troubleshooting

Symptoms → causes → fixes, ranked roughly by frequency.

## 401 Unauthorized

- **Missing `Authorization` header.** SDK construction without a token, or a fresh `fetch` call that bypasses the SDK.
- **Wrong token format.** Must be exactly `Bearer <token>`, single space, no quotes.
- **Daemon started without a configured token.** Check daemon config; `auth.bearer_token` is required (the daemon refuses to start without it).

## 404 Not Found on a path that should exist

- **External write hasn't been picked up yet.** The change detector is eventually-consistent. Either wait, or call `POST /v1/index/rescan` to force convergence.
- **Wrong mount name.** The first segment of a virtual path is the mount name (basename of the configured `base_paths` entry). `/data/foo` → mount `data`, virtual path `data/foo`.
- **Symlink pointing outside the mount.** Filegate refuses to follow these for security. The path resolves but returns 403/404 depending on where the resolution failed.

## 409 Conflict — what now?

- The body **may** have `existingId` and `existingPath`. They're populated
  for path-PUT, mkdir, chunked-start, and transfer conflicts where the
  daemon could resolve the colliding node. Chunked
  duplicate-chunk-content rejects and a few generic conflict paths return
  just `{"error": "conflict"}` — code defensively, fall back to a
  generic message when the diagnostic fields are missing.
- If they're present, show them to the user and let them choose
  `overwrite`, `rename`, or cancel.
- Default is `error` — explicit on purpose. If your business logic *always* wants to replace, set `onConflict: "overwrite"` consistently in your client code.
- For mkdir, if you want idempotent "make sure this folder exists", use `onConflict: "skip"`.
- For chunked uploads: a 409 at finalize after a clean start means another writer raced you. Retry `/start` with the same params and `onConflict: "overwrite"` — the staging chunks are kept.

Full details: [`conflict-handling.md`](conflict-handling.md).

## 413 Payload Too Large

- **Thumbnail source bigger than `ThumbnailMaxSourceBytes`** (default 64
  MiB) or decoded pixels exceed `ThumbnailMaxPixels` (default 40
  megapixels). Generate the thumbnail outside Filegate or downscale the
  source first.

For oversized **chunked-upload** parameters, the daemon returns 400 (not
413) at `/start` with `"invalid size"` or `"invalid chunkSize"`. For
oversized **one-shot** PUT bodies, you'll typically see a connection-level
`MaxBytesError` rather than a clean HTTP status.

## 507 Insufficient Storage

The daemon's `UploadMinFreeBytes` guard refused the write. Either:

- Free up disk space on the mount.
- Lower the guard at daemon config (not recommended — the guard exists to keep the system writable for other processes).
- Move the write to a mount with more free space.

This is also returned eagerly from `POST /v1/uploads/chunked/start` — no chunks land before the check fires.

## Browser uploads silently corrupt large files

- **You're using `JSON.stringify(body)` instead of passing the binary directly.** The TS SDK's `paths.put(path, body, opts)` expects `BodyInit` (Uint8Array, Blob, ReadableStream, etc.). Don't JSON-encode binary.
- **Missing or wrong `Content-Type`.** Pass the actual MIME type via `opts.contentType`.
- **Buffering in your relay backend.** If you do `await req.arrayBuffer()` and then forward, you've broken streaming. Use `paths.putRaw` (TS) or pass `r.Body` (Go) through directly.

## Chunked upload "finalizes" but downloaded file is wrong size or corrupted

- **Wrong overall checksum** in the `start` request. The server hashes the assembled file at finalize and rejects on mismatch. Compute `sha256` over the WHOLE bytes and pass `sha256:<hex>` (lowercase hex, with the prefix).
- **Wrong chunk boundaries.** Use `chunks.bounds(i, size, chunkSize)` from `@valentinkolb/filegate/utils` (or the Go equivalent) — manual arithmetic is the most common bug.
- **Trailing chunk uploaded with wrong size.** The last chunk is `size - (totalChunks-1) * chunkSize` bytes, not `chunkSize`. The bounds helper handles this.

## Resume "loses" already-uploaded chunks

- **Different parameters in retry.** `uploadId = hex(sha256(parentId + ":" + filename + ":" + checksum)[0:8])`. Any change to those three → different upload session, fresh start. If you're computing the file checksum in the browser, make sure it's deterministic across reloads. (Most clients let the SDK return the `uploadId` from `/start` and don't compute it themselves.)
- **Staging dir was cleaned up.** The default expiry is 24h. Sessions older than that are gone. Tune `upload.expiry` in daemon config if you need longer.

## "I deleted a file but it still shows up in listings"

- The change detector hasn't run yet, or ran but couldn't see the change (e.g., btrfs `find-new` race). Force a rescan: `POST /v1/index/rescan`.
- If you deleted via the API (`DELETE /v1/nodes/{id}`), this should never happen — file an issue.

## Slow listings on huge directories

- `computeRecursiveSizes=true` walks the whole subtree per child dir — O(N²) bad for huge trees. Default off; only set when you actually need recursive sizes.
- Make sure you're paginating with `pageSize` and `cursor`, not asking for everything in one call.

## Tar download is missing files

- **Symlinks are skipped on purpose** for security. If you need them, build the archive yourself.
- **A file disappeared mid-stream** — the tar walker logs and continues. The download status is still 200 because headers are already sent; check daemon logs.

## Concurrent writes to the same file

Filegate doesn't arbitrate concurrent writers — last write wins, and you may see partial states briefly. If your application has a concept of file ownership, enforce single-writer in your relay layer (e.g., a per-user-per-file lock).

## Tests against Filegate are flaky

- **You're racing the change detector.** Use `POST /v1/index/rescan` to force convergence at a known point in time, instead of sleeping and hoping.
- **You're using the same mount across parallel tests.** Use a temp dir per test (and configure the daemon to mount it).
- **Filesystem mtime granularity.** ext4/tmpfs round to milliseconds. Two writes within the same millisecond can produce equal mtimes; the change detector may not see one of them. Add a tiny pause OR rewrite the test to depend on size/content rather than timing.

## I changed something in the daemon config and it's not picking up

- The daemon does NOT hot-reload config. Restart the daemon process.
- The TS SDK's lazy default reads env vars at first property access, which may have happened before you changed them. Use explicit construction in tests.
