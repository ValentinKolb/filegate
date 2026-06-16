---
title: Versioning
navTitle: Versioning
section: Operate
order: 120
description: Use per-file version history on supported mounts.
tags: [versioning, btrfs]
---

# Versioning

Versioning is for operators and applications that need per-file history for HTTP-mediated writes on supported filesystems.

## Scope

| Item | Scope | Meaning |
|---|---:|---|
| Automatic capture | Per file | Captures versions for HTTP-mediated writes, subject to cooldown and size rules. |
| Manual snapshot | Per file | Captures current bytes immediately and pins the version. |
| Restore | Per file | Restores in place or creates a new sibling file. |
| Retention | Per service config | Prunes unpinned versions according to retention buckets. |
| Filesystem support | Per mount | btrfs supports efficient reflink versions; unsupported mounts return `404` with `versioning not supported on this mount` when versioning is forced or requested. |

External writes through `cp`, `rsync`, shell tools, or S3-compatible clients are not automatic HTTP version captures.

## Config modes

| Mode | Scope | Meaning |
|---|---:|---|
| `auto` | Service | Enable versioning on supported mounts and no-op on unsupported mounts. Default. |
| `on` | Service | Force versioning; unsupported mounts return errors for version operations. |
| `off` | Service | Disable versioning globally. |

## Basic operations

List versions:

```sh
curl -fsS -H 'Authorization: Bearer dev-token' \
  http://127.0.0.1:8080/v1/nodes/<node-id>/versions
```

Create a manual snapshot:

```sh
curl -fsS -X POST \
  -H 'Authorization: Bearer dev-token' \
  -H 'Content-Type: application/json' \
  -d '{"label":"before migration"}' \
  http://127.0.0.1:8080/v1/nodes/<node-id>/versions/snapshot
```

Restore as a new file:

```sh
curl -fsS -X POST \
  -H 'Authorization: Bearer dev-token' \
  -H 'Content-Type: application/json' \
  -d '{"asNewFile":true,"name":"restored.txt"}' \
  http://127.0.0.1:8080/v1/nodes/<node-id>/versions/<version-id>/restore
```

## Retention buckets

Retention buckets define age windows and maximum retained counts inside each window.

```yaml
versioning:
  retention_buckets:
    - keep_for: "1h"
      max_count: -1
    - keep_for: "24h"
      max_count: 24
    - keep_for: "720h"
      max_count: 30
```

| Field | Type | Scope | Meaning |
|---|---|---:|---|
| `keep_for` | duration | Retention bucket | Age window from now. |
| `max_count` | integer | Retention bucket | Versions retained in the window. `-1` means unlimited. |

Pinned versions are protected until the configured pin cap or post-delete grace rules apply.
