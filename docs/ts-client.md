# TypeScript Client (In-Depth)

This document describes the intended stateless TS client pattern for Filegate.

## Goals

- Stateless client construction
- Scoped namespaces (`paths`, `nodes`, `uploads`, `transfers`, `search`, `index`, `stats`, `utils`)
- Relay-first streaming APIs
- Minimal runtime surprises across server and browser

## Two Construction Modes

### Mode A: default env-based instance (server/runtime only)

Use this on Node/Bun server runtimes where env vars exist.

```ts
import { filegate } from "@valentinkolb/filegate/client";

process.env.FILEGATE_URL = "http://127.0.0.1:8080";
process.env.FILEGATE_TOKEN = "dev-token";

const roots = await filegate.paths.list();
```

Behavior expectation:

- default instance is created lazily on first use
- reads `FILEGATE_URL` and `FILEGATE_TOKEN`
- safe to import without immediate side effects

### Mode B: explicit instance (browser and explicit server wiring)

Use this in browsers and anywhere you want explicit dependency injection.

```ts
import { Filegate } from "@valentinkolb/filegate/client";

const fg = new Filegate({
  baseUrl: "https://filegate.internal.example",
  token: "<token>",
  fetchImpl: fetch,
});
```

## Core Usage

### List roots and stat a path

```ts
const roots = await fg.paths.list();
const node = await fg.paths.get("data/invoices/2026", { pageSize: 100 });
```

### Metadata by id and content stream

```ts
const meta = await fg.nodes.get(node.id, { pageSize: 100 });
const upstream = await fg.nodes.contentRaw(meta.id, { inline: false });
```

### One-shot upload

```ts
await fg.paths.put("data/uploads/hello.txt", new TextEncoder().encode("hello\n"), {
  contentType: "text/plain",
});
```

### Conflict handling

Every write defaults to `onConflict: "error"` — the server never silently
overwrites. To opt into the old "last write wins" behavior, pass `"overwrite"`
explicitly. To make the upload land under a different name when the target
exists, pass `"rename"`:

```ts
// File upload
try {
  await fg.paths.put("photos/sunset.jpg", bytes);
} catch (e) {
  if (e.status === 409) {
    // e.body.existingId / e.body.existingPath tell the UI what's there.
    const overwrite = await confirmFromUser(e.body.existingPath);
    await fg.paths.put("photos/sunset.jpg", bytes, {
      onConflict: overwrite ? "overwrite" : "rename",
    });
  } else throw e;
}

// mkdir — `skip` is the idempotent-folder pattern (mkdir -p style)
await fg.nodes.mkdir(parentId, { path: "uploads", onConflict: "skip" });

// Chunked — fail fast at start, save bandwidth
await fg.uploads.chunked.start({
  parentId, filename: "video.mp4", size, checksum,
  chunkSize: 8 << 20,
  onConflict: "error",     // 409 immediately if "video.mp4" already exists
});
```

Available modes per endpoint:

| Endpoint                  | Modes                          |
|---------------------------|--------------------------------|
| `paths.put` / `paths.putRaw` | `error`, `overwrite`, `rename` |
| `nodes.mkdir`             | `error`, `skip`, `rename`      |
| `uploads.chunked.start`   | `error`, `overwrite`, `rename` |
| `transfers.create`        | `error`, `overwrite`, `rename` |

### Chunked upload

Pure chunk math and hashing live at `@valentinkolb/filegate/utils`. They do
not require a `Filegate` instance, so they are safe in browsers, Web Workers,
and any environment that does not have a token.

```ts
import { chunks } from "@valentinkolb/filegate/utils";

const bytes = new Uint8Array(10 * 1024 * 1024);
const checksum = await chunks.sha256Bytes(bytes);
const chunkSize = 1024 * 1024;

const start = await fg.uploads.chunked.start({
  parentId: "<parent-id>",
  filename: "video.bin",
  size: bytes.byteLength,
  checksum,
  chunkSize,
});

for (let i = 0; i < chunks.totalChunks(bytes.byteLength, chunkSize); i++) {
  const { start: from, end: to } = chunks.bounds(i, bytes.byteLength, chunkSize);
  const chunk = bytes.slice(from, to);
  const chunkChecksum = await chunks.sha256Bytes(chunk);
  const res = await fg.uploads.chunked.sendChunk(start.uploadId, i, chunk, chunkChecksum);
  if (res.completed) {
    console.log("done", res.complete?.file.id);
  }
}
```

Notes:

- order does not matter
- duplicate chunks are allowed when content matches
- server auto-finalizes

## Relay/Proxy Pattern

### Upload passthrough

```ts
const upstream = await fg.paths.putRaw(virtualPath, request.body, {
  contentType: request.headers.get("content-type") ?? "application/octet-stream",
});

return new Response(upstream.body, {
  status: upstream.status,
  headers: upstream.headers,
});
```

### Download passthrough

```ts
const upstream = await fg.nodes.contentRaw(nodeId, { inline: true });
return new Response(upstream.body, { status: upstream.status, headers: upstream.headers });
```

## Error Model

Normalize errors to include:

- `status`
- `message` (prefer parsed `{ error: string }`)
- `method`
- `path`

This is critical for backend observability under load.

## Browser Notes

- Always use explicit `new Filegate(...)`.
- Do not rely on `process.env` defaults in browser bundles.
- Prefer short-lived backend-minted tokens, never hardcoded long-lived secrets.

## Contract Source of Truth

Server JSON contract lives in:

- [api/v1/types.go](https://github.com/ValentinKolb/filegate/blob/main/api/v1/types.go)

Keep generated TS types synchronized with this contract.
