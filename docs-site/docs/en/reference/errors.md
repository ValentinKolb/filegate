---
title: Error reference
navTitle: Errors
section: Reference
order: 240
description: Filegate HTTP error response reference.
tags: [reference, errors]
---

# Error reference

This reference catalogs Filegate error response shape and common status codes for REST clients.

## Error envelope

| Field | Type | Scope | Meaning |
|---|---|---:|---|
| `error` | string | Error response | Human-readable error message. |
| `existingId` | string | Conflict response | Existing node ID when a name conflict can be resolved. |
| `existingPath` | string | Conflict response | Existing virtual path when a name conflict can be resolved. |

Example:

```json
{
  "error": "path already exists",
  "existingId": "019ec...",
  "existingPath": "/data/photo.jpg"
}
```

## Common status codes

| Status | Scope | Meaning | Client action |
|---:|---:|---|---|
| `400` | Request | Invalid query, body, ID, checksum, segment, config, or path. | Fix the request. |
| `401` | Auth | Missing or invalid bearer token or scoped token. | Authenticate or mint a new scoped URL. |
| `403` | Permission | Filesystem access, reserved upload path, or Filegate permission guard rejected the request. | Check mount permissions, target path, and request scope. |
| `404` | Resource | Node, path, version, session, or route not found. | Resolve the current ID/path and retry if needed. |
| `408` | Request | Thumbnail generation was canceled by the request context. | Retry if the client still needs the thumbnail. |
| `409` | Conflict | Target exists, pin cap reached, or session commit cannot safely overwrite. | Prompt for conflict handling or inspect existing target. |
| `413` | Request body | Upload, segment, directory tar, or thumbnail source exceeds a limit. | Read capabilities or lower requested size. |
| `415` | Thumbnail source | Thumbnail generation does not support the file type. | Request thumbnails only for supported image files. |
| `500` | Service | Unexpected server-side failure. | Check service logs and activity records. |
| `501` | Service configuration | Index rescan route is mounted but rescan is unavailable. | Check the server command wiring. |
| `503` | Service pressure | Upload segment writer slots or thumbnail queue are full. | Retry with lower concurrency or increase worker capacity. |
| `507` | Storage | Free-space guard rejected a write. | Free disk space or adjust `upload.min_free_bytes`. |

## Conflict handling

| Mode | Scope | Conflict result |
|---|---:|---|
| `error` | Uploads, mkdir, transfers | Returns `409` with conflict details when available. |
| `overwrite` | File uploads, transfers | Replaces according to endpoint rules. |
| `rename` | One-shot uploads, mkdir, transfers | Creates a unique sibling name. |
| `skip` | `mkdir` only | Returns existing directory when compatible. |
