---
title: Metrics reference
navTitle: Metrics
section: Reference
order: 230
description: Complete Filegate metrics and stats reference.
tags: [reference, metrics]
---

# Metrics reference

This reference catalogs Filegate-specific Prometheus metrics and REST stats fields for operators building dashboards or alerts.

## Prometheus metrics

| Name | Type | Unit | Scope | Dimensions | Meaning |
|---|---|---:|---:|---|---|
| `filegate_http_requests_total` | counter | requests | REST or S3 adapter | `adapter`, `op`, `status_class` | Total HTTP requests by adapter, operation, and status class. |
| `filegate_http_request_duration_seconds` | histogram | seconds | REST or S3 request | `adapter`, `op` | Request duration. |
| `filegate_http_requests_in_flight` | gauge | requests | Adapter | `adapter` | Current in-flight HTTP requests. |
| `filegate_http_request_size_bytes` | histogram | bytes | Request body | `adapter`, `op` | Request body size. |
| `filegate_http_response_size_bytes` | histogram | bytes | Response body | `adapter`, `op` | Response body size. |
| `filegate_multipart_cleanup_retired_total` | counter | manifests | S3 multipart cleanup loop | `reason` | Multipart staging dirs retired by reason. |
| `filegate_multipart_cleanup_errors_total` | counter | errors | S3 multipart cleanup loop | none | Cleanup loop errors. |
| `filegate_version_prune_deleted_total` | counter | versions | Version pruner | none | Versions deleted by the pruner. |
| `filegate_version_prune_kept_total` | counter | versions | Version pruner | none | Versions kept by the pruner. |
| `filegate_version_prune_errors_total` | counter | errors | Version pruner | none | Version pruning errors. |
| `filegate_detector_events_total` | counter | events | Filesystem detector | `type` | Detector events by type. |
| `filegate_s3_ratelimit_rejected_total` | counter | requests | S3 key limiter | none | S3 requests rejected with `503 SlowDown`. |
| `filegate_multipart_complete_phase_seconds` | histogram | seconds | S3 multipart complete | `phase` | CompleteMultipartUpload sub-phase duration. |
| `filegate_build_info` | gauge | info | Service binary | `version`, `commit` | Build information; value is always `1`. |
| `filegate_index_entities` | gauge | entities | Service index | `type` | Indexed files and directories. |
| `filegate_index_db_bytes` | gauge | bytes | Pebble index path | none | On-disk size of the Pebble index directory. |
| `filegate_path_cache_entries` | gauge | entries | Service process | none | Current path cache entries. |
| `filegate_mount_used_bytes` | gauge | bytes | Backing filesystem | `mount` | Used bytes on the filesystem backing a mount. |
| `filegate_mount_free_bytes` | gauge | bytes | Backing filesystem | `mount` | Free bytes on the filesystem backing a mount. |

## REST stats fields

| Field | Type | Unit | Scope | Meaning |
|---|---|---:|---:|---|
| `generatedAt` | integer | Unix seconds | Stats response | Snapshot time. |
| `index.totalEntities` | integer | count | Service index | Total indexed files and directories. |
| `index.totalFiles` | integer | count | Service index | Total indexed files. |
| `index.totalDirs` | integer | count | Service index | Total indexed directories. |
| `index.dbSizeBytes` | integer | bytes | Pebble index path | On-disk index size. |
| `cache.pathEntries` | integer | entries | Service process | Current path cache entries. |
| `cache.pathCapacity` | integer | entries | Service process | Configured path cache capacity. |
| `cache.pathUtilRatio` | number | ratio | Service process | Path cache occupancy from `0` to `1`. |
| `mounts[].id` | string | identifier | Mount root | Stable node ID of the mount root. |
| `mounts[].name` | string | name | Mount root | Mount basename exposed as root name and S3 bucket. |
| `mounts[].path` | string | path | Mount root | Virtual mount path. |
| `mounts[].files` | integer | count | Mount root | Indexed file count under the mount. |
| `mounts[].dirs` | integer | count | Mount root | Indexed directory count under the mount. |
| `disks[].diskName` | string | name | Filesystem device | Backing disk name. |
| `disks[].fsType` | string | name | Filesystem device | Filesystem type. |
| `disks[].used` | integer | bytes | Filesystem device | Used bytes. |
| `disks[].size` | integer | bytes | Filesystem device | Total bytes. |
| `disks[].roots` | string array | paths | Filesystem device | Mount roots backed by this device. |
| `system.goroutines` | integer | count | Service process | Go goroutine count. |
| `system.heapAllocBytes` | integer | bytes | Service process | Allocated heap bytes. |
| `system.heapSysBytes` | integer | bytes | Service process | Heap bytes obtained from OS. |
| `system.heapObjects` | integer | count | Service process | Live heap object count. |
| `system.numGC` | integer | count | Service process | Completed garbage collections. |
| `system.lastGCPauseNs` | integer | nanoseconds | Service process | Last GC pause duration. |
| `system.openFDs` | integer | count | Service process | Open file descriptors. |
| `system.maxFDs` | integer | count | Service process | File descriptor limit. |
