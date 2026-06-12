# HTTP Routes

Base requirements:

- API prefix: `/v1`
- Auth: `Authorization: Bearer <token>` on `/v1/*` routes, except the final
  `PUT /v1/uploads/direct/{token}` request
- Health route without auth: `GET /health`
- JSON errors: `{ "error": "..." }`. On `409 Conflict`, the body also
  carries `"existingId"` and `"existingPath"` so clients can render a
  meaningful prompt without an extra resolve call.

## CORS

CORS is disabled by default: empty `server.cors.allowed_origins` produces no
`Access-Control-*` headers and no preflight handling. Prefer configuring CORS
at Traefik/Caddy/nginx. If Filegate must answer browser cross-origin requests
directly, configure explicit origins:

```yaml
server:
  cors:
    allowed_origins:
      - "https://app.example.com"
    exposed_headers:
      - "X-Node-Id"
      - "X-Created-Id"
    max_age: "10m"
```

`allowed_methods` and `allowed_headers` are optional. When omitted, Filegate
uses REST-safe defaults for common methods plus `Authorization`,
`Content-Type`, and `X-Chunk-Checksum`. `allow_credentials: true` is rejected
with wildcard origin `*`.

## Conflict Handling

Endpoints that may hit a name collision accept an `onConflict` argument
with the following modes. The default is **always** `error` — Filegate
never silently overwrites or drops data.

| Mode        | Semantics                                                    | Allowed at                                       |
|-------------|--------------------------------------------------------------|--------------------------------------------------|
| `error`     | 409 Conflict if the target already exists. **Default.**       | All endpoints                                    |
| `overwrite` | Replace the existing file in place; node id preserved.       | `PUT /v1/paths`, `POST /v1/uploads/chunked/start`, `POST /v1/transfers` |
| `rename`    | Pick a unique sibling name (`foo.jpg` → `foo-01.jpg` → `foo-02.jpg` …) and create a new node there. The response reflects the actually-used name. | All endpoints                                    |
| `skip`      | If a directory with the same name exists, return it unchanged. A name conflict with a *file* still fails — we cannot turn a file into a directory. | **Only** `POST /v1/nodes/{id}/mkdir`             |

A directory can never be replaced by a file PUT (any mode), and `mkdir` can
never recursively delete an existing subtree (`overwrite` is rejected for
mkdir). For directory replacement use `POST /v1/transfers` with `overwrite`.

## Health and Stats

- `GET /health`
  - Returns `200 OK` + `OK`
- `GET /v1/stats`
  - Runtime stats for index/cache/mounts/disks

## Paths API (Virtual Filesystem)

- `GET /v1/paths/`
  - List configured root nodes (virtual root entries)
- `GET /v1/paths/{path...}`
  - Return node metadata for virtual path
  - If node is a directory, response can include paged children
  - Query:
    - `pageSize` (default `100`)
    - `cursor` — opaque token from the previous page's `nextCursor`,
      passed back verbatim. Pagination survives concurrent deletion of
      the cursor entry. Legacy clients passing a bare child name keep
      working, but a deleted name then answers `400`.
    - `computeRecursiveSizes=true|false`
- `PUT /v1/paths/{path...}`
  - One-shot upload at path
  - Query: `onConflict=error|overwrite|rename` (default `error`)
  - Body: binary stream
  - Headers:
    - `X-Node-Id`: resulting node id
    - `X-Created-Id`: only when a new file was created
  - Returns `409 Conflict` (with `existingId`/`existingPath`) on collision
    when mode is `error`, or when the existing target is a directory and
    mode is `overwrite` (you cannot replace a directory via file PUT)
  - May return `507` when storage free-space guard rejects new writes

## Nodes API (ID-oriented)

- `GET /v1/nodes/{id}`
  - Metadata by id (same shape as path metadata)
  - Same directory paging query params as above
- `GET /v1/nodes/{id}/content`
  - File: raw file stream
  - Directory: tar stream of subtree
    - Preflight limit: max 100,000 tar entries, 10 GiB regular-file
      content, depth 128
    - Over-limit directory downloads return `413` before response
      headers are sent
  - Query: `inline=true|false`
- `GET /v1/nodes/{id}/thumbnail`
  - On-demand thumbnail for image-like sources
  - Query: `size`
- `POST /v1/nodes/{id}/mkdir`
  - Create subdirectory relative to parent node id
  - Body: `MkdirRequest` — `onConflict` accepts `error|skip|rename`
    (`overwrite` is rejected; use Transfer for that)
- `PUT /v1/nodes/{id}`
  - Replace file content for file node id
- `PATCH /v1/nodes/{id}`
  - Rename / ownership update
  - Query: `recursiveOwnership=true|false` (default `true`)
  - Body: `UpdateNodeRequest`
- `DELETE /v1/nodes/{id}`
  - Delete subtree

## Transfers

- `POST /v1/transfers`
  - Move or copy between parent ids
  - Query: `recursiveOwnership=true|false` (default `true`)
  - Body: `TransferRequest`

## Versions

Per-file version history for HTTP-mediated writes. btrfs-only via
reflinks; ext4 mounts return `404` with `versioning_unsupported`. See
[versioning.md](versioning.md) for behaviour, retention, and operator
notes.

- `GET /v1/nodes/{id}/versions`
  - Cursor-paginated list of captured versions (oldest first)
  - Query: `cursor=<vid>`, `limit=<n>` (default 100, max 1000)
- `GET /v1/nodes/{id}/versions/{vid}/content`
  - Streams version bytes (`application/octet-stream`)
- `POST /v1/nodes/{id}/versions/snapshot`
  - Body (optional): `{ "label"?: "..." }` (capped at `max_label_bytes`)
  - Captures current bytes immediately, ignoring cooldown. Pinned.
  - `409` if `max_pinned_per_file` would be exceeded
- `POST /v1/nodes/{id}/versions/{vid}/pin`
  - Body (optional): `{ "label"?: "..." | null }`
  - Pins version, optionally updates label. Idempotent
- `POST /v1/nodes/{id}/versions/{vid}/unpin`
  - Clears pinned flag. Label preserved. Idempotent
- `POST /v1/nodes/{id}/versions/{vid}/restore`
  - Body (optional): `{ "asNewFile"?: bool, "name"?: string }`
  - Default = in-place: snapshot current, then load target. File ID
    preserved
  - `asNewFile=true` = sibling file (`<base>-restored<ext>` or `name`,
    with `-N` conflict suffix). New File ID
- `DELETE /v1/nodes/{id}/versions/{vid}`
  - Manual purge. Works on any version (including pinned) → 204

## Search

- `GET /v1/search/glob`
  - Query:
    - `pattern` (required)
    - `paths` (comma/semicolon list)
    - `limit`
    - `showHidden`
    - `files`
    - `directories`

## Chunked Uploads

- `POST /v1/uploads/chunked/start`
  - Body: `ChunkedStartRequest` — `onConflict` accepts `error|overwrite|rename`
  - Initializes or resumes deterministic upload session
  - **Optimistic conflict check**: returns `409` immediately when the name
    already exists and mode is `error`, before the client uploads any chunk
  - Resume can upgrade the persisted mode (e.g. retry with `overwrite`
    after the first attempt collided at finalize)
- `GET /v1/uploads/chunked/{uploadId}`
  - Status + uploaded chunk list
- `PUT /v1/uploads/chunked/{uploadId}/chunks/{index}`
  - Upload one chunk
  - Header: `X-Chunk-Checksum: sha256:<hex>` (optional but recommended)
  - Supports out-of-order and duplicate chunk uploads
  - Auto-finalizes when all chunks are present
  - **Authoritative conflict check** at finalize: if a concurrent writer
    created the target between start and finalize, the persisted
    `onConflict` mode decides — `error` returns 409 (chunks remain in
    staging, the client can retry start with a different mode), while
    `overwrite` and `rename` proceed
  - May return `507` when storage free-space guard rejects new writes

Chunk staging location is mount-local: `<mount>/.fg-uploads/<uploadId>/`.

## Direct Upload URLs

Direct upload URLs let a trusted backend mint a short-lived browser upload URL
without exposing the Filegate bearer token to the browser.

- `POST /v1/uploads/direct`
  - Requires `Authorization: Bearer <token>`
  - Body:
    - `path` (required): virtual target path, for example `data/inbox/a.jpg`
    - `expiresInSeconds` (optional): default `900`, maximum `86400`
    - `contentType` (optional): required by the final uploader when set
    - `onConflict` (optional): `error|overwrite|rename`, default `error`
    - `maxBytes` (optional): per-URL body cap, must be `<= upload.max_upload_bytes`
  - Returns `201` with:
    - `uploadUrl`: absolute `PUT` URL
    - `method`: `PUT`
    - `path`
    - `expiresAt`: Unix seconds
    - `maxBytes`
- `PUT /v1/uploads/direct/{token}`
  - Does not accept the REST bearer token; the URL token is the credential
  - Body: binary stream
  - Returns the same node JSON and `X-Node-Id` / `X-Created-Id` headers as
    `PUT /v1/paths/{path...}`
  - Returns `401` for expired, malformed, or tampered URLs
  - Returns `413` when the body exceeds the URL's `maxBytes`

`server.public_url` controls the base URL used in `uploadUrl`. Leave it empty
only when the authenticated minting request already arrives with the public
host and scheme.

## Index Maintenance

- `POST /v1/index/rescan`
  - Force full rescan
- `POST /v1/index/resolve`
  - Resolve by path(s) or id(s)
  - Body accepts one of:
    - `path`
    - `paths[]`
    - `id`
    - `ids[]`

## Node Shape

`Node` returns a discriminated union style via `type` (`file|directory`) with shared metadata:

- `id`, `type`, `name`, `path`, `size`, `mtime`
- `ownership { uid, gid, mode }`
- `mimeType` (when available)
- `exif` (always present; empty map when not applicable)
- directory-only optional listing fields:
  - `children[]`, `pageSize`, `nextCursor`

Source of truth for JSON structs: [`api/v1/types.go`](https://github.com/ValentinKolb/filegate/blob/main/api/v1/types.go)
