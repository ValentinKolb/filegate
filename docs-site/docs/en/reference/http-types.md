---
title: HTTP JSON types reference
navTitle: HTTP JSON types
section: Reference
order: 225
description: Complete JSON request and response type reference for the Filegate REST API.
tags: [reference, http, json]
---

# HTTP JSON types reference

This reference catalogs Filegate REST JSON types for developers implementing clients, SDKs, or application-server integrations.

All timestamps are Unix seconds unless a field name says otherwise. All byte fields are integer byte counts.

## Common envelopes

### ErrorResponse

| Field | Type | Scope | Meaning |
|---|---|---:|---|
| `error` | string | Failed JSON route | Human-readable error reason. |
| `existingId` | string, optional | `409 Conflict` | Existing node ID when the conflict is caused by an existing node. |
| `existingPath` | string, optional | `409 Conflict` | Existing virtual path when the conflict is caused by an existing node. |

### OKResponse

| Field | Type | Scope | Meaning |
|---|---|---:|---|
| `ok` | boolean | Acknowledgement response | `true` when the action was accepted. |

## Ownership

### Ownership

`Ownership` is used by create, update, transfer, and upload requests.

| Field | Type | Scope | Meaning |
|---|---|---:|---|
| `uid` | integer, optional | Target node | POSIX user ID. |
| `gid` | integer, optional | Target node | POSIX group ID. |
| `mode` | string, optional | Target file or directory | Octal mode string such as `0644` or `0755`. |
| `dirMode` | string, optional | Created directories | Octal mode for directories created during recursive operations. |

### OwnershipView

`OwnershipView` is returned in node metadata.

| Field | Type | Scope | Meaning |
|---|---|---:|---|
| `uid` | integer | Node | POSIX user ID. |
| `gid` | integer | Node | POSIX group ID. |
| `mode` | string | Node | Octal mode string. |

## Nodes and listings

### Node

`Node` describes a file, directory, or mount root.

| Field | Type | Scope | Meaning |
|---|---|---:|---|
| `id` | string | Node | Stable Filegate node ID. |
| `type` | string | Node | `file` or `directory`. |
| `name` | string | Node | Basename inside the parent directory. |
| `path` | string | Node | Virtual path under the configured mount roots. |
| `size` | integer | Node | File size in bytes, or directory size when recursive sizing was requested. |
| `mtime` | integer | Node | Modified time as Unix seconds. |
| `ownership` | `OwnershipView` | Node | POSIX owner, group, and mode. |
| `mimeType` | string, optional | File node | Detected or supplied MIME type. |
| `etag` | string, optional | File node | File fingerprint exposed as an HTTP/S3-style entity tag. |
| `sha256` | string, optional | File node | SHA-256 checksum when known. |
| `exif` | object | File node | Extracted image metadata as string key/value pairs. |
| `children` | `Node[]`, optional | Directory listing | Child entries returned for listing requests. |
| `pageSize` | integer, optional | Directory listing | Number of children requested in this page. |
| `nextCursor` | string, optional | Directory listing | Opaque cursor for the next page. Empty means the final page. |

### NodeListResponse

| Field | Type | Scope | Meaning |
|---|---|---:|---|
| `items` | `Node[]` | Root listing | Configured mount roots. |
| `total` | integer | Root listing | Number of returned roots. |

## Stats and capabilities

### StatsResponse

| Field | Type | Scope | Meaning |
|---|---|---:|---|
| `generatedAt` | integer | Stats snapshot | Snapshot time. |
| `index` | `StatsIndex` | Service index | Indexed entity counts and Pebble size. |
| `cache` | `StatsCache` | Service process | Path cache occupancy. |
| `mounts` | `StatsMount[]` | Configured mounts | Per-mount indexed file and directory counts. |
| `disks` | `StatsDisk[]` | Backing filesystems | Disk usage for filesystems backing mount roots. |
| `system` | `StatsSystem` | Service process | Go runtime and file descriptor stats. |

### StatsIndex

| Field | Type | Scope | Meaning |
|---|---|---:|---|
| `totalEntities` | integer | Service index | Indexed files and directories. |
| `totalFiles` | integer | Service index | Indexed files. |
| `totalDirs` | integer | Service index | Indexed directories. |
| `dbSizeBytes` | integer | Pebble index path | On-disk Pebble database size. |

### StatsCache

| Field | Type | Scope | Meaning |
|---|---|---:|---|
| `pathEntries` | integer | Service process | Current path cache entries. |
| `pathCapacity` | integer | Service process | Configured path cache capacity. |
| `pathUtilRatio` | number | Service process | Cache occupancy from `0` to `1`. |

### StatsMount

| Field | Type | Scope | Meaning |
|---|---|---:|---|
| `id` | string | Mount root | Stable node ID. |
| `name` | string | Mount root | Root name exposed in Filegate and S3. |
| `path` | string | Mount root | Virtual mount path. |
| `files` | integer | Mount root | Indexed file count under the mount. |
| `dirs` | integer | Mount root | Indexed directory count under the mount. |

### StatsDisk

| Field | Type | Scope | Meaning |
|---|---|---:|---|
| `diskName` | string | Filesystem device | Backing disk name. |
| `fsType` | string | Filesystem device | Filesystem type. |
| `used` | integer | Filesystem device | Used bytes. |
| `size` | integer | Filesystem device | Total bytes. |
| `roots` | string array | Filesystem device | Mount roots backed by this device. |

### StatsSystem

| Field | Type | Scope | Meaning |
|---|---|---:|---|
| `goroutines` | integer | Service process | Go goroutine count. |
| `heapAllocBytes` | integer | Service process | Allocated heap bytes. |
| `heapSysBytes` | integer | Service process | Heap bytes obtained from the OS. |
| `heapObjects` | integer | Service process | Live heap object count. |
| `numGC` | integer | Service process | Completed garbage collections. |
| `lastGCPauseNs` | integer | Service process | Last garbage collection pause in nanoseconds. |
| `openFDs` | integer | Service process | Open file descriptors. |
| `maxFDs` | integer | Service process | File descriptor limit. |

### CapabilitiesResponse

| Field | Type | Scope | Meaning |
|---|---|---:|---|
| `uploads` | `UploadCapabilities` | Service process | Server-enforced upload limits. |

### UploadCapabilities

| Field | Type | Scope | Meaning |
|---|---|---:|---|
| `maxChunkBytes` | integer | Segment or direct upload body | Maximum accepted chunk size in bytes. |
| `maxUploadBytes` | integer | One-shot upload | Maximum accepted one-shot upload size in bytes. |
| `maxSessionUploadBytes` | integer | Upload session | Maximum final session file size in bytes. |
| `maxConcurrentSegmentWrites` | integer | Service process | Concurrent upload-session segment writes accepted by the server. |

## Activity

### ActivityActor

| Field | Type | Scope | Meaning |
|---|---|---:|---|
| `kind` | string | Activity record | Actor category, such as bearer token or anonymous scoped token. |
| `id` | string | Activity record | Stable actor identifier. |
| `label` | string, optional | Activity record | Display label for the actor. |
| `delegatedActor` | string, optional | Activity record | Optional upstream actor supplied by a trusted caller. |

### ActivityTarget

| Field | Type | Scope | Meaning |
|---|---|---:|---|
| `kind` | string | Activity record | Target category. |
| `id` | string, optional | Activity record | Target node ID when available. |
| `path` | string, optional | Activity record | Target virtual path when available. |

### ActivityEvent

| Field | Type | Scope | Meaning |
|---|---|---:|---|
| `id` | string | Activity record | Record ID. |
| `at` | integer | Activity record | Event time. |
| `actor` | `ActivityActor` | Activity record | Caller identity captured from auth context and actor headers. |
| `operation` | string | Activity record | Operation name, for example `node.mkdir` or `upload.commit`. |
| `outcome` | string | Activity record | `succeeded`, `failed`, or `skipped`. Current HTTP and S3 handlers record `succeeded` or `failed`. |
| `target` | `ActivityTarget`, optional | Activity record | Node or path affected by the operation. |
| `durationMs` | integer, optional | Activity record | Handler duration in milliseconds. |
| `requestId` | string, optional | Activity record | Request correlation ID. |
| `error` | string, optional | Failed operation | Error summary. |
| `meta` | object, optional | Activity record | Operation-specific metadata. |

### ActivityListResponse

| Field | Type | Scope | Meaning |
|---|---|---:|---|
| `items` | `ActivityEvent[]` | Activity query | Matching activity records. |
| `total` | integer | Activity query | Matching records retained in the ring buffer. |
| `offset` | integer | Activity query | Zero-based offset used for pagination. |
| `limit` | integer | Activity query | Maximum records requested. |
| `retained` | integer | Service process | Total records retained in memory. |
| `capacity` | integer | Service process | Configured activity ring buffer capacity. |
| `operations` | string array | Service process | Operation names present in retained records. |

## File and directory mutations

### MkdirRequest

| Field | Type | Scope | Meaning |
|---|---|---:|---|
| `path` | string | Parent node | Child directory path to create. |
| `recursive` | boolean, optional | Create operation | Creates missing intermediate directories when `true`. |
| `ownership` | `Ownership`, optional | Created directories | POSIX ownership and mode. |
| `onConflict` | string, optional | Target path | `error`, `skip`, or `rename`. Defaults to `error`. |

### UpdateNodeRequest

| Field | Type | Scope | Meaning |
|---|---|---:|---|
| `name` | string, optional | Node | New basename inside the current parent directory. |
| `ownership` | `Ownership`, optional | Node | POSIX ownership and mode update. |

### TransferRequest

| Field | Type | Scope | Meaning |
|---|---|---:|---|
| `op` | string | Transfer operation | `move` or `copy`. |
| `sourceId` | string | Source node | Node ID to move or copy. |
| `targetParentId` | string | Target directory | Parent directory node ID. |
| `targetName` | string | Target path | Basename to create under `targetParentId`. |
| `onConflict` | string | Target path | `error`, `overwrite`, or `rename` for an existing target. |
| `ownership` | `Ownership`, optional | Target node | POSIX ownership and mode for the resulting node. |

### TransferResponse

| Field | Type | Scope | Meaning |
|---|---|---:|---|
| `node` | `Node` | Result node | Created, moved, or copied node. |
| `op` | string | Transfer operation | Operation that was applied. |

## Search

### GlobSearchResponse

| Field | Type | Scope | Meaning |
|---|---|---:|---|
| `results` | `Node[]` | Search request | Matching nodes. |
| `errors` | `GlobSearchError[]` | Search request | Per-path search errors. |
| `meta` | `GlobSearchMeta` | Search request | Aggregate search metadata. |
| `paths` | `GlobSearchPath[]` | Search request | Per-base-path result summary. |

### GlobSearchError

| Field | Type | Scope | Meaning |
|---|---|---:|---|
| `path` | string | Search base path | Path where the error happened. |
| `cause` | string | Search base path | Error reason. |

### GlobSearchMeta

| Field | Type | Scope | Meaning |
|---|---|---:|---|
| `pattern` | string | Search request | Glob pattern that was evaluated. |
| `limit` | integer | Search request | Maximum requested result count. |
| `resultCount` | integer | Search request | Number of results returned. |
| `errorCount` | integer | Search request | Number of per-path errors. |

### GlobSearchPath

| Field | Type | Scope | Meaning |
|---|---|---:|---|
| `path` | string | Search base path | Base path searched. |
| `returned` | integer | Search base path | Result count returned for this base path. |
| `hasMore` | boolean | Search base path | More matches exist beyond the request limit. |

## Direct uploads and downloads

### DirectUploadURLRequest

| Field | Type | Scope | Meaning |
|---|---|---:|---|
| `path` | string | Target path | Virtual path the scoped PUT URL may write. |
| `expiresInSeconds` | integer, optional | Direct URL | Lifetime in seconds. |
| `contentType` | string, optional | Target file | Content type recorded for the upload. |
| `onConflict` | string, optional | Target path | `error`, `overwrite`, or `rename`. Defaults to `error`. |
| `maxBytes` | integer, optional | Direct URL | Maximum accepted body size in bytes. |

### DirectUploadURLResponse

| Field | Type | Scope | Meaning |
|---|---|---:|---|
| `uploadUrl` | string | Direct URL | Short-lived unauthenticated PUT URL. |
| `method` | string | Direct URL | HTTP method, always `PUT`. |
| `path` | string | Target path | Virtual path scoped into the token. |
| `expiresAt` | integer | Direct URL | Expiry time. |
| `maxBytes` | integer | Direct URL | Maximum accepted body size in bytes. |

### DirectDownloadURLRequest

| Field | Type | Scope | Meaning |
|---|---|---:|---|
| `nodeId` | string, optional | Download target | Node ID to download. |
| `path` | string, optional | Download target | Virtual path to resolve at mint time. |
| `expiresInSeconds` | integer, optional | Direct URL | Lifetime in seconds. |
| `inline` | boolean, optional | Response headers | Requests inline content disposition when supported. |

Exactly one of `nodeId` or `path` must identify the target.

### DirectDownloadURLResponse

| Field | Type | Scope | Meaning |
|---|---|---:|---|
| `downloadUrl` | string | Direct URL | Short-lived unauthenticated GET URL. |
| `method` | string | Direct URL | HTTP method, always `GET`. |
| `expiresAt` | integer | Direct URL | Expiry time. |
| `node` | `Node` | Download target | Node resolved when the URL was minted. |

## Upload sessions

### UploadSessionCreateRequest

| Field | Type | Scope | Meaning |
|---|---|---:|---|
| `path` | string | Upload session | Final virtual file path. |
| `size` | integer | Upload session | Final file size in bytes. |
| `checksum` | string | Upload session | Final checksum, formatted as `sha256:<hex>`. |
| `segmentSize` | integer | Upload session | Segment size in bytes. |
| `contentType` | string, optional | Target file | Content type recorded for the upload. |
| `ownership` | `Ownership`, optional | Target file | POSIX ownership and mode for the final file. |
| `onConflict` | string, optional | Target path | `error` or `overwrite`. Defaults to `error`. |
| `direct` | `UploadSessionDirectRequest`, optional | Upload session | Scoped direct token options. |

### UploadSessionBatchCreateRequest

| Field | Type | Scope | Meaning |
|---|---|---:|---|
| `uploads` | `UploadSessionCreateRequest[]` | Batch request | Independent one-file upload sessions to create. |
| `segmentSize` | integer, optional | Batch request | Default segment size used when an upload entry omits `segmentSize`. |
| `direct` | `UploadSessionDirectRequest`, optional | Batch request | Default direct-token options for sessions that omit `direct`. |

### UploadSessionDirectRequest

| Field | Type | Scope | Meaning |
|---|---|---:|---|
| `expiresInSeconds` | integer, optional | Scoped direct token | Token lifetime in seconds. |
| `allow` | string array, optional | Scoped direct token | Allowed direct-session operations: `putSegment`, `status`, `commit`, and `abort`. Empty means all four. |

### UploadSessionResponse

| Field | Type | Scope | Meaning |
|---|---|---:|---|
| `id` | string | Upload session | Session ID. |
| `path` | string | Upload session | Final virtual file path. |
| `size` | integer | Upload session | Final file size in bytes. |
| `checksum` | string | Upload session | Final checksum, formatted as `sha256:<hex>`. |
| `segmentSize` | integer | Upload session | Segment size in bytes. |
| `totalSegments` | integer | Upload session | Number of segments required for the file. |
| `segments` | `UploadSessionSegment[]` | Upload session | Segment plan. |
| `uploadedSegments` | integer array | Upload session | Segment indexes already uploaded. |
| `phase` | string | Upload session | Session state. |
| `direct` | `UploadSessionDirect`, optional | Upload session | Scoped token for direct segment uploads. |

### UploadSessionSegment

| Field | Type | Scope | Meaning |
|---|---|---:|---|
| `index` | integer | Upload session | Zero-based segment index. |
| `offset` | integer | Upload session | Byte offset in the final file. |
| `size` | integer | Upload session | Segment size in bytes. |

### UploadSessionDirect

| Field | Type | Scope | Meaning |
|---|---|---:|---|
| `baseUrl` | string | Scoped direct token | Base URL for direct session routes. |
| `token` | string | Scoped direct token | Token sent by the browser for direct session operations. |
| `expiresAt` | integer | Scoped direct token | Expiry time. |
| `allow` | string array | Scoped direct token | Server-approved session or path scopes. |

### UploadSessionBatchCreateResponse

| Field | Type | Scope | Meaning |
|---|---|---:|---|
| `sessions` | `UploadSessionResponse[]` | Batch request | Created upload sessions. |

### UploadSegmentResponse

| Field | Type | Scope | Meaning |
|---|---|---:|---|
| `sessionId` | string | Upload session | Session ID. |
| `index` | integer | Uploaded segment | Segment index accepted by the server. |
| `uploadedSegments` | integer array | Upload session | Segment indexes uploaded after this request. |

### UploadSessionCommitResponse

| Field | Type | Scope | Meaning |
|---|---|---:|---|
| `node` | `Node` | Upload session | Final committed file node. |
| `checksum` | string | Upload session | Final checksum verified by the server. |

## Versions

### VersionResponse

| Field | Type | Scope | Meaning |
|---|---|---:|---|
| `versionId` | string | File version | Version ID. |
| `fileId` | string | File version | Source file node ID. |
| `timestamp` | integer | File version | Version creation time. |
| `size` | integer | File version | Version size in bytes. |
| `mode` | integer | File version | POSIX mode as an integer. |
| `pinned` | boolean | File version | Whether pruning can remove the version. |
| `label` | string, optional | File version | Operator-supplied label. |
| `deletedAt` | integer, optional | File version | Source file deletion time when the version is in delete grace. |

### ListVersionsResponse

| Field | Type | Scope | Meaning |
|---|---|---:|---|
| `items` | `VersionResponse[]` | Version listing | Returned versions. |
| `nextCursor` | string, optional | Version listing | Opaque cursor for the next page. |

### VersionSnapshotRequest

| Field | Type | Scope | Meaning |
|---|---|---:|---|
| `label` | string, optional | New version | Operator-supplied label. |

### VersionPinRequest

| Field | Type | Scope | Meaning |
|---|---|---:|---|
| `label` | string, optional | Version | New label. Omit to keep the existing label; send an empty string to clear it. |

### VersionRestoreRequest

| Field | Type | Scope | Meaning |
|---|---|---:|---|
| `asNewFile` | boolean, optional | Restore operation | Restores as a new sibling file when `true`; restores in place when `false` or omitted. |
| `name` | string, optional | Restore operation | Name for the new sibling file when `asNewFile=true`. |

### VersionRestoreResponse

| Field | Type | Scope | Meaning |
|---|---|---:|---|
| `node` | `Node` | Restore operation | Restored source node or new sibling node. |
| `asNew` | boolean | Restore operation | `true` when the restore created a new file. |

## Index resolution

### IndexResolveRequest

| Field | Type | Scope | Meaning |
|---|---|---:|---|
| `path` | string, optional | Resolve request | Single virtual path to resolve. |
| `paths` | string array, optional | Resolve request | Multiple virtual paths to resolve. |
| `id` | string, optional | Resolve request | Single node ID to resolve. |
| `ids` | string array, optional | Resolve request | Multiple node IDs to resolve. |

Use one single field (`path` or `id`) or one batch field (`paths` or `ids`) per request.

### IndexResolveSingleResponse

| Field | Type | Scope | Meaning |
|---|---|---:|---|
| `item` | `Node`, nullable | Resolve request | Resolved node, or `null` when no node matches. |

### IndexResolveManyResponse

| Field | Type | Scope | Meaning |
|---|---|---:|---|
| `items` | `Node[]`, nullable entries | Resolve request | Resolved nodes in request order; missing entries are `null`. |
| `total` | integer | Resolve request | Number of entries returned. |
