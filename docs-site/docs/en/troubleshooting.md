---
title: Troubleshooting
navTitle: Troubleshooting
section: Operate
order: 140
description: Diagnose common Filegate startup, upload, S3, versioning, and browser integration issues.
tags: [troubleshooting]
---

# Troubleshooting

This page is for operators and developers diagnosing Filegate runtime and integration failures.

## Startup failures

| Symptom | Likely scope | Check | Fix |
|---|---:|---|---|
| `storage.base_paths is required` | Config | `fg config show --config ...` | Set at least one storage mount. |
| `auth.bearer_token is required` | REST config | `auth.bearer_token` | Set a bearer token unless running S3-only. |
| Mount health check fails | Storage mount | Filesystem permissions, free space, xattr support | Fix mount permissions and ensure user xattrs are enabled. |
| Invalid trusted proxy | Server config | `server.trusted_proxies` | Use valid IP or CIDR values. |
| S3 startup fails on bucket name | Mount name | Mount basename | Rename or remount using an S3-valid bucket name. |

## Upload failures

| Status | Scope | Meaning | Action |
|---|---:|---|---|
| `409` | Target path | Target exists and `onConflict=error`. | Choose `overwrite`, `rename`, or skip in the application. |
| `413` | Request body or segment | Body exceeds configured limit. | Read `/v1/capabilities` and lower chunk size. |
| `507` | Backing filesystem | Free space guard rejected the write. | Free disk space or lower `upload.min_free_bytes`. |
| `400` | Request shape | Missing field, invalid checksum, or invalid segment. | Validate request body and checksum format. |

## Browser direct URL failures

| Symptom | Scope | Check | Fix |
|---|---:|---|---|
| Browser cannot reach returned URL | Public REST URL | `server.public_url` | Set the externally reachable URL. |
| CORS preflight fails | Browser origin | `server.cors.allowed_origins` or proxy CORS | Allow the exact origin and required headers. |
| Direct token expired | URL token | `expiresInSeconds` | Request a new URL or longer expiry. |
| Segment upload rejected | Upload session | Segment size and checksum | Use the server segment plan and matching checksum. |

## S3 failures

| Symptom | Scope | Check | Fix |
|---|---:|---|---|
| `AccessDenied` for bucket | S3 key | `s3.keys[].buckets` | Add the bucket name or `*` to the key allowlist. |
| Signature mismatch | S3 client | Endpoint, region, path-style setting | Use configured `s3.region` and path-style addressing. |
| `SlowDown` | S3 key | Rate limit fields | Reduce client concurrency or raise per-key rate limits. |
| Multipart staging grows | Mount | `.fg-uploads` and cleanup config | Check cleanup loop settings and stuck upload age. |

## Versioning failures

| Symptom | Scope | Meaning | Action |
|---|---:|---|---|
| `versioning not supported on this mount` | Mount | Filesystem does not support configured versioning mode. | Use btrfs or set `versioning.enabled=auto/off`. |
| Snapshot label rejected | File version | Label exceeds `versioning.max_label_bytes`. | Use a shorter label. |
| Pin fails with conflict | File version set | `versioning.max_pinned_per_file` reached. | Unpin older versions or raise the cap. |

## Where to look

| Source | Scope | Use for |
|---|---:|---|
| `systemctl status filegate` | Service | Process status and recent logs. |
| `journalctl -u filegate` | Service | Full service logs. |
| `GET /v1/stats` | Service | Current index, cache, disk, and runtime state. |
| `GET /v1/activity` | Service ring buffer | Recent operation failures and durations. |
| `/metrics` | Prometheus | Request rates, latency, storage pressure, cleanup and pruning counters. |
