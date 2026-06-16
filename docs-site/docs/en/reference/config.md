---
title: Config reference
navTitle: Config
section: Reference
order: 200
description: Complete Filegate configuration key reference.
tags: [reference, config]
---

# Config reference

This reference catalogs every Filegate configuration key for operators who maintain YAML files, environment variables, or `fg config` commands.

Environment variables use `FILEGATE_` plus the uppercase config path with `_` separators. Example: `server.listen` becomes `FILEGATE_SERVER_LISTEN`.

## Server

| Name | Type | Default | Scope | CLI flag | Meaning |
|---|---|---:|---:|---|---|
| `server.listen` | string | `:8080` | REST listener | `--server-listen` | Address the REST HTTP server binds to. |
| `server.public_url` | string | empty | Direct URL minting | `--server-public-url` | Public REST base URL used in direct upload and download URLs. |
| `server.trusted_proxies` | string array | `[]` | Request logging | `--server-trusted-proxies` | Proxy IPs or CIDRs whose forwarded client IP headers are honored. |
| `server.write_timeout` | duration | `5m` | HTTP request | `--server-write-timeout` | Maximum response write duration. |
| `server.shutdown_timeout` | duration | `60s` | Service shutdown | `--server-shutdown-timeout` | Grace period for in-flight handlers before force close. |
| `server.access_log_enabled` | boolean | `true` | REST and S3 listeners | `--server-access-log-enabled` | Enables request access logs. |

## CORS

| Name | Type | Default | Scope | CLI flag | Meaning |
|---|---|---:|---:|---|---|
| `server.cors.allowed_origins` | string array | `[]` | REST listener | `--server-cors-allowed-origins` | Allowed browser origins. Empty disables CORS handling. |
| `server.cors.allowed_methods` | string array | REST defaults | CORS preflight | `--server-cors-allowed-methods` | Allowed methods. Empty uses `GET, POST, PUT, PATCH, DELETE, HEAD, OPTIONS`. |
| `server.cors.allowed_headers` | string array | REST defaults | CORS preflight | `--server-cors-allowed-headers` | Allowed request headers. Empty uses authorization, content type, upload session, and checksum headers. |
| `server.cors.exposed_headers` | string array | `[]` | Browser responses | `--server-cors-exposed-headers` | Response headers visible to browser JavaScript. |
| `server.cors.max_age` | duration | `0s` | CORS preflight | `--server-cors-max-age` | Browser preflight cache duration. |
| `server.cors.allow_credentials` | boolean | `false` | CORS responses | `--server-cors-allow-credentials` | Sends credentialed CORS responses. Rejected with wildcard origin. |

## Auth

| Name | Type | Default | Scope | CLI flag | Meaning |
|---|---|---:|---:|---|---|
| `auth.bearer_token` | string | empty | REST API | `--auth-bearer-token` | Bearer token required for `/v1/*` REST routes. Required unless `s3.enabled=true` for an S3-only deployment. |

## Storage

| Name | Type | Default | Scope | CLI flag | Meaning |
|---|---|---:|---:|---|---|
| `storage.base_paths` | string array | required | Storage mounts | `--storage-base-paths` | Filesystem roots exposed as Filegate mount roots. |
| `storage.index_path` | string | `/var/lib/filegate/index` | Service metadata | `--storage-index-path` | Pebble index directory. |

## Detection

| Name | Type | Default | Scope | CLI flag | Meaning |
|---|---|---:|---:|---|---|
| `detection.backend` | enum | `auto` | Service | `--detection-backend` | Change detector backend: `auto`, `poll`, or `btrfs`. |
| `detection.poll_interval` | duration | `3s` | Poll detector | `--detection-poll-interval` | Polling interval when poll detection is used. |

## Cache

| Name | Type | Default | Scope | CLI flag | Meaning |
|---|---|---:|---:|---|---|
| `cache.path_cache_size` | integer | `100000` | Service process | `--cache-path-cache-size` | Maximum entries in the in-memory path resolution cache. |

## Jobs

| Name | Type | Default | Scope | CLI flag | Meaning |
|---|---|---:|---:|---|---|
| `jobs.workers` | integer | `min(max(CPU*4,16),256)` | Service process | `--jobs-workers` | Background worker count. |
| `jobs.queue_size` | integer | `8192` | Service process | `--jobs-queue-size` | Background job queue size. |
| `jobs.thumbnail_workers` | integer | `0` | Thumbnail jobs | `--jobs-thumbnail-workers` | Thumbnail worker count. `0` uses service defaults. |
| `jobs.thumbnail_queue_size` | integer | `0` | Thumbnail jobs | `--jobs-thumbnail-queue-size` | Thumbnail job queue size. `0` uses service defaults. |

## Upload

| Name | Type | Default | Unit | Scope | CLI flag | Meaning |
|---|---|---:|---:|---:|---|---|
| `upload.expiry` | duration | `24h` | time | Upload session | `--upload-expiry` | Expiry for inactive upload sessions. |
| `upload.cleanup_interval` | duration | `6h` | time | Service process | `--upload-cleanup-interval` | Sweep interval for expired upload sessions. |
| `upload.max_chunk_bytes` | integer | `52428800` | bytes | Segment or one direct chunk | `--upload-max-chunk-bytes` | Maximum single chunk or segment body size. |
| `upload.max_upload_bytes` | integer | `524288000` | bytes | One-shot upload | `--upload-max-upload-bytes` | Maximum file size accepted by one-shot upload. |
| `upload.max_session_upload_bytes` | integer | `53687091200` | bytes | Upload session | `--upload-max-session-upload-bytes` | Maximum final file size for upload sessions. |
| `upload.max_concurrent_segment_writes` | integer | `min(max(CPU*8,32),512)` | count | Service process | `--upload-max-concurrent-segment-writes` | Concurrent upload-session segment writes accepted by the server. |
| `upload.min_free_bytes` | integer | `67108864` | bytes | Backing filesystem | `--upload-min-free-bytes` | Minimum free bytes required before accepting writes. |

## Thumbnail

| Name | Type | Default | Unit | Scope | CLI flag | Meaning |
|---|---|---:|---:|---:|---|---|
| `thumbnail.lru_cache_size` | integer | `1024` | entries | Service process | `--thumbnail-lru-cache-size` | In-memory thumbnail cache capacity. |
| `thumbnail.max_source_bytes` | integer | `67108864` | bytes | Source file | `--thumbnail-max-source-bytes` | Maximum source file size for thumbnail generation. |
| `thumbnail.max_pixels` | integer | `41943040` | pixels | Decoded image | `--thumbnail-max-pixels` | Maximum decoded image pixels for thumbnail generation. |

## Versioning

| Name | Type | Default | Scope | CLI flag | Meaning |
|---|---|---:|---:|---|---|
| `versioning.enabled` | enum | `auto` | Service | `--versioning-enabled` | `auto`, `on`, or `off`. |
| `versioning.cooldown` | duration | `15m` | Per file | `--versioning-cooldown` | Minimum interval between automatic captures. |
| `versioning.min_size_for_auto_v1` | integer | `65536` | Per file | `--versioning-min-size-for-auto-v1` | Minimum file size for automatic initial version capture. |
| `versioning.retention_buckets` | array | See below | Service | `--versioning-retention-bucket` | Age windows and counts for pruning unpinned versions. |
| `versioning.pruner_interval` | duration | `5m` | Service process | `--versioning-pruner-interval` | Version pruner loop interval. |
| `versioning.max_pinned_per_file` | integer | `100` | Per file | `--versioning-max-pinned-per-file` | Maximum pinned versions. `0` disables the cap. |
| `versioning.pinned_grace_after_delete` | duration | `720h` | Per deleted file | `--versioning-pinned-grace-after-delete` | Retention grace for pinned versions after live file deletion. |
| `versioning.max_label_bytes` | integer | `2048` | Per version | `--versioning-max-label-bytes` | Maximum label size in bytes. |

Default retention buckets:

| `keep_for` | `max_count` | Meaning |
|---:|---:|---|
| `1h` | `-1` | Keep all versions in the last hour. |
| `24h` | `24` | Keep about hourly versions for the last day. |
| `720h` | `30` | Keep about daily versions for 30 days. |
| `8760h` | `12` | Keep about monthly versions for one year. |

## S3

| Name | Type | Default | Scope | CLI flag | Meaning |
|---|---|---:|---:|---|---|
| `s3.enabled` | boolean | `false` | Service | `--s3-enabled` | Enables the S3-compatible listener. |
| `s3.listen` | string | `:9000` | S3 listener | `--s3-listen` | Address the S3 HTTP server binds to. |
| `s3.region` | string | `us-east-1` | S3 SigV4 | `--s3-region` | Region clients must sign in the SigV4 credential scope. |
| `s3.access_key` | string | empty | Legacy S3 credential | `--s3-access-key` | Single-tenant access key with all-bucket access. |
| `s3.secret_key` | string | empty | Legacy S3 credential | `--s3-secret-key` | Single-tenant secret key. |
| `s3.max_concurrent_writes` | integer | `min(max(CPU*8,32),512)` | S3 listener | `--s3-max-concurrent-writes` | Concurrent S3 object and part writes. |
| `s3.keys` | array | `[]` | S3 credentials | `--s3-key` | Multi-tenant key entries. |
| `s3.cleanup.done_retention` | duration | `24h` when unset or `0s` | Multipart cleanup | `--s3-cleanup-done-retention` | Retention for completed multipart manifests. |
| `s3.cleanup.aborted_retention` | duration | `1h` when unset or `0s` | Multipart cleanup | `--s3-cleanup-aborted-retention` | Retention for aborted multipart manifests. |
| `s3.cleanup.stuck_upload_max_age` | duration | `168h` when unset or `0s` | Multipart cleanup | `--s3-cleanup-stuck-upload-max-age` | Maximum age for open stuck multipart uploads. |
| `s3.cleanup.interval` | duration | `1h` when unset or `0s` | Multipart cleanup | `--s3-cleanup-interval` | Cleanup loop interval. Negative disables the loop. |

S3 key entry:

| Name | Type | Default | Scope | Meaning |
|---|---|---:|---:|---|
| `access_key` | string | required | S3 key | SigV4 access key ID. |
| `secret_key` | string | required | S3 key | SigV4 secret. |
| `buckets` | string array | `[]` | S3 key | Allowed bucket names. `*` grants every configured mount. Empty grants no buckets. |
| `requests_per_second` | integer | `0` | S3 key | Sustained request rate limit. `0` means unlimited. |
| `burst` | integer | `0` | S3 key | Burst limit. Defaults to requests per second when unset. |

## Metrics

| Name | Type | Default | Scope | CLI flag | Meaning |
|---|---|---:|---:|---|---|
| `metrics.enabled` | boolean | `false` | Service | `--metrics-enabled` | Enables the Prometheus metrics endpoint on the REST listener. |
| `metrics.path` | string | `/metrics` | REST listener | `--metrics-path` | Metrics route path. Must not collide with `/v1`. |
| `metrics.token` | string | empty | Metrics endpoint | `--metrics-token` | Optional scrape bearer token. |

## Activity

| Name | Type | Default | Scope | CLI flag | Meaning |
|---|---|---:|---:|---|---|
| `activity.ring_buffer_size` | integer | `500` | Service process | `--activity-ring-buffer-size` | Number of recent activity records retained in memory. |
