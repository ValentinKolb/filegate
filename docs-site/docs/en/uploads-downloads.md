---
title: Uploads and downloads
navTitle: Uploads and downloads
section: Use Filegate
order: 60
description: Choose between one-shot uploads, upload sessions, direct browser URLs, and direct downloads.
tags: [uploads, downloads, browser]
---

# Uploads and downloads

This page is for developers implementing file transfer flows with Filegate.

## Upload options

| Pattern | Scope | Use for | Auth seen by browser |
|---|---:|---|---|
| One-shot REST upload | One file | Small server-side uploads. | None, when run server-side. |
| Direct one-shot URL | One file | Browser upload of a small file through a scoped PUT URL. | Scoped URL token only. |
| Upload session | One file | Large, resumable, parallel uploads. | None, when relayed server-side. |
| Direct upload session | One file | Browser upload of large files with scoped segment URLs. | Scoped session token only. |
| Batch session creation | Many files | Folder uploads where the app server creates many sessions in one request. | Scoped session tokens only. |

## Recommended browser shape

Keep the Filegate bearer token on the application server:

```txt
browser -> app server: ask to upload files
app server -> Filegate: create direct upload sessions
browser -> Filegate: upload segments with scoped session tokens
browser or app server -> Filegate: commit sessions
```

The admin app uses this shape and can be used as a reference implementation.

## One-shot direct upload

Mint a short-lived upload URL from a trusted server:

```sh
curl -fsS -X POST \
  -H 'Authorization: Bearer dev-token' \
  -H 'Content-Type: application/json' \
  -d '{"path":"data/inbox/photo.jpg","contentType":"image/jpeg","expiresInSeconds":900,"onConflict":"rename"}' \
  http://127.0.0.1:8080/v1/uploads/direct
```

The browser uploads bytes to the returned `uploadUrl` with `PUT`.

## Resumable upload session

Create a session:

```sh
curl -fsS -X POST \
  -H 'Authorization: Bearer dev-token' \
  -H 'Content-Type: application/json' \
  -d '{"path":"data/archive.tar","size":104857600,"checksum":"sha256:<hex>","segmentSize":33554432}' \
  http://127.0.0.1:8080/v1/uploads/sessions
```

Upload segments in any order:

```sh
curl -fsS -X PUT \
  -H 'Authorization: Bearer dev-token' \
  -H 'X-Segment-Checksum: sha256:<hex>' \
  --data-binary @segment-0.bin \
  http://127.0.0.1:8080/v1/uploads/sessions/<session-id>/segments/0
```

Commit after all segments are uploaded:

```sh
curl -fsS -X POST \
  -H 'Authorization: Bearer dev-token' \
  http://127.0.0.1:8080/v1/uploads/sessions/<session-id>/commit
```

## Idempotency and integrity

| Behavior | Scope | Meaning |
|---|---:|---|
| Segment index | Upload session | Segment offsets and sizes are fixed at session creation. |
| Duplicate segment upload | One segment | Accepted when the bytes match the existing segment. |
| Segment checksum | One segment | Optional `X-Segment-Checksum: sha256:<hex>` validates the segment body. |
| Final checksum | Upload session | Required `sha256:<hex>` validates the assembled file before commit. |
| Commit | Upload session | Atomic target creation or replacement according to `onConflict`. |
| Abort | Upload session | Removes staged bytes for an in-progress session. |

## Downloads

Download by node ID through the REST API:

```sh
curl -fL -H 'Authorization: Bearer dev-token' \
  http://127.0.0.1:8080/v1/nodes/<node-id>/content \
  -o file.bin
```

Mint a direct download URL:

```sh
curl -fsS -X POST \
  -H 'Authorization: Bearer dev-token' \
  -H 'Content-Type: application/json' \
  -d '{"path":"/data/file.bin","expiresInSeconds":300}' \
  http://127.0.0.1:8080/v1/downloads/direct
```

Directories download as tar streams from `GET /v1/nodes/{id}/content`.

## Limits

Clients should read `GET /v1/capabilities` before choosing upload sizes.

| Capability | Unit | Scope | Meaning |
|---|---:|---:|---|
| `uploads.maxChunkBytes` | bytes | One request body or session segment | Maximum accepted chunk size. |
| `uploads.maxUploadBytes` | bytes | One-shot upload | Maximum accepted one-shot upload size. |
| `uploads.maxSessionUploadBytes` | bytes | Upload session | Maximum final file size for a session. |
| `uploads.maxConcurrentSegmentWrites` | count | Service process | Maximum concurrent segment writes accepted by the server. |
