# Raw HTTP API

For runtimes without a Filegate SDK (Python, Rust, curl, ...), or when you need to know exactly what the SDKs do under the hood.

## Universal rules

- **Base path**: all routes (except `/health`) live under `/v1`.
- **Auth**: every `/v1/*` request must include `Authorization: Bearer <token>`. No exceptions.
- **JSON**: request and response bodies use `application/json` unless explicitly noted (file uploads/downloads use the actual content type).
- **Errors**: `{ "error": "..." }` for all non-2xx. **On 409 the body
  may also carry** `{ "existingId": "...", "existingPath": "..." }` —
  populated when the daemon could resolve the colliding node (PUT
  `/v1/paths`, mkdir, chunked upload start, transfers). For chunked
  duplicate-chunk-content conflicts and a few other edge paths the body
  is just `{ "error": "conflict" }` without diagnostics. Code defensively.

## Health & stats

```
GET /health                   → 200 OK (text body "OK"), no auth
GET /v1/stats                 → daemon + index + cache + mounts + disks
```

## Paths (virtual filesystem)

```
GET /v1/paths/                                    → list mount roots
GET /v1/paths/{path...}?pageSize=&cursor=&computeRecursiveSizes=
                                                  → metadata; for dirs may include children
PUT /v1/paths/{path...}?onConflict=error|overwrite|rename
    Content-Type: <mime>
    Body: <binary stream>
                                                  → one-shot upload
                                                  → 201 Created (new) or 200 OK (overwrite)
                                                  → headers: X-Node-Id, X-Created-Id (only on create)
                                                  → may return 507 (storage guard)
                                                  → may return 409 (conflict)
```

## Nodes (ID-based)

```
GET    /v1/nodes/{id}                             → metadata
GET    /v1/nodes/{id}?pageSize=&cursor=&...       → directory metadata + children
GET    /v1/nodes/{id}/content?inline=true|false   → file bytes OR tar stream (for dirs)
GET    /v1/nodes/{id}/thumbnail?size=N            → image/jpeg
POST   /v1/nodes/{id}/mkdir                       → create subdirectory
       body: { path, recursive?, ownership?, onConflict? }
PUT    /v1/nodes/{id}                             → replace file content
       Body: <binary stream>
       (only valid for FILE nodes — directory IDs return 400)
PATCH  /v1/nodes/{id}?recursiveOwnership=true|false
       body: { name?, ownership? }                → rename / chown
DELETE /v1/nodes/{id}                             → remove subtree (no body)
```

## Transfers (move / copy)

```
POST /v1/transfers?recursiveOwnership=true|false
body: {
  "op": "move" | "copy",
  "sourceId": "...",
  "targetParentId": "...",
  "targetName": "...",
  "onConflict": "error" | "overwrite" | "rename",  // default "error"
  "ownership": { ... }                             // optional
}
→ { "node": { ... }, "op": "move" | "copy" }
```

(There is no `ensureUniqueName` field — it's just `onConflict: "rename"`.
Sending unknown fields returns 400.)

## Search

```
GET /v1/search/glob?pattern=&paths=&limit=&showHidden=&files=&directories=

pattern      doublestar glob (e.g. "**/*.{jpg,png}")
paths        comma- or semicolon-separated virtual paths (mount names OR sub-dirs); empty = all mounts
limit        fairness cap, split across listed paths
showHidden   "true" to include dotfiles (default false)
files        "true" to include files (default true)
directories  "true" to include directories (default false)

→ {
  "results": [Node, ...],
  "errors":  [{ "path": "...", "cause": "..." }, ...],
  "paths":   [{ "path": "...", "returned": N, "hasMore": bool }, ...],
  "meta":    { "pattern": "...", "limit": N, "resultCount": N, "errorCount": N }
}
```

## Chunked uploads

```
POST /v1/uploads/chunked/start
body: {
  "parentId": "...",
  "filename": "...",
  "size": 12345,
  "checksum": "sha256:<hex>",
  "chunkSize": 8388608,
  "ownership": { ... },                            // optional
  "onConflict": "error" | "overwrite" | "rename"   // default "error"
}
→ {
  "uploadId": "...",
  "chunkSize": ...,
  "totalChunks": ...,
  "uploadedChunks": [...],
  "completed": false
}
→ may return 409 (optimistic conflict check) or 507 (storage guard)

GET /v1/uploads/chunked/{uploadId}                 → status (same shape as above)

PUT /v1/uploads/chunked/{uploadId}/chunks/{index}
    X-Chunk-Checksum: sha256:<hex>                 // recommended
    Body: <chunk bytes>
→ {
    "chunkIndex": N,
    "uploadedChunks": [...],
    "completed": false
  }
  -- OR (if this was the closing chunk) --
→ {
    "completed": true,
    "file": { ...full Node metadata... }
  }
→ may return 409 (authoritative conflict check at finalize)
```

## Index maintenance

```
POST /v1/index/rescan                             → force full rescan, returns { "ok": true }

POST /v1/index/resolve
body: one of:
  { "path": "..." }            → { "item": { ...node... } | null }
  { "paths": ["...", "..."] }  → { "items": [node|null, ...], "total": N }
  { "id":   "..." }            → { "item": { ...node... } | null }
  { "ids":  ["...", "..."] }   → { "items": [node|null, ...], "total": N }
```

## Node shape (response)

```json
{
  "id": "01933abc-...",
  "type": "file" | "directory",
  "name": "sunset.jpg",
  "path": "data/photos/sunset.jpg",
  "size": 1234567,
  "mtime": 1734567890123,
  "ownership": { "uid": 1000, "gid": 1000, "mode": "644" },
  "mimeType": "image/jpeg",
  "exif": { ... },                  // always present, empty for non-image

  // For directories with paged listing:
  "children": [Node, ...],
  "pageSize": 100,
  "nextCursor": "..."               // empty on last page
}
```

`mtime` is milliseconds-since-epoch (not nanos).

## Status codes

| Code | When                                                                     |
|------|--------------------------------------------------------------------------|
| 200  | OK (existing resource updated, search results, status check, etc.)       |
| 201  | Created (new file/directory). Note: `mkdir` always returns 201, even with `onConflict=skip` returning an unchanged existing dir. |
| 204  | No Content (DELETE)                                                      |
| 400  | Invalid argument (bad JSON, invalid mode, malformed path)                |
| 401  | Missing or invalid bearer token                                          |
| 403  | Forbidden (path traversal attempt, symlink escape, root mutation)        |
| 404  | Not Found                                                                |
| 409  | Conflict — name collision; body has `existingId`/`existingPath`          |
| 413  | Payload Too Large (oversized image source for thumbnails)                |
| 415  | Unsupported Media Type (thumbnail of unsupported source format)          |
| 500  | Internal server error (something went wrong on the daemon)               |
| 503  | Service Unavailable (e.g. thumbnail scheduler queue full)                |
| 507  | Insufficient Storage — daemon's free-space guard would be violated (chunked upload start) |

For chunked uploads, oversized `size`/`chunkSize` at `start` returns **400**
(not 413) with `"invalid size"` / `"invalid chunkSize"`. For one-shot PUT,
the daemon limits the body via `MaxBytesReader` — exceeding the limit
typically surfaces as a connection-level error (`MaxBytesError`) rather
than a clean HTTP status. Pick chunked upload if you might hit the limit.

## curl examples

```bash
TOKEN=dev-token
BASE=http://127.0.0.1:8080

# List mounts
curl -sH "Authorization: Bearer $TOKEN" $BASE/v1/paths/

# Upload (default = error if exists)
curl -sX PUT -H "Authorization: Bearer $TOKEN" -H "Content-Type: text/plain" \
  --data-binary "hello" "$BASE/v1/paths/data/hello.txt"

# Upload with overwrite
curl -sX PUT -H "Authorization: Bearer $TOKEN" -H "Content-Type: text/plain" \
  --data-binary "v2" "$BASE/v1/paths/data/hello.txt?onConflict=overwrite"

# Download
curl -sH "Authorization: Bearer $TOKEN" \
  "$BASE/v1/nodes/01933abc.../content" --output out.bin

# Mkdir with skip
curl -sX POST -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -d '{"path":"uploads","recursive":true,"onConflict":"skip"}' \
  "$BASE/v1/nodes/$ROOT_ID/mkdir"

# Search
curl -sH "Authorization: Bearer $TOKEN" \
  "$BASE/v1/search/glob?pattern=**/*.jpg&limit=50&paths=data"

# Resolve path → ID
curl -sX POST -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -d '{"path":"data/photos/sunset.jpg"}' "$BASE/v1/index/resolve"
```
