# Per-file Versioning

Filegate captures point-in-time copies of files written through its
HTTP API. Older versions are listable, downloadable, and restorable —
either back into the live file or as a fresh sibling.

The feature is **HTTP-only** and **btrfs-only**. Writes that bypass the
HTTP layer (`cp` / `rsync` / SSH / shell into a container) are NOT
captured because there is no point at which Filegate can save the
"before" bytes. btrfs is required because the implementation relies on
reflinks (FICLONE) to make storage cheap; on other filesystems the
feature is silently disabled per mount.

## Configuration

```yaml
versioning:
  enabled: auto                    # auto | on | off
  cooldown: "15m"                  # min between auto-captures per file
  min_size_for_auto_v1: 65536      # 64 KiB floor for V1-on-create
  pruner_interval: "5m"            # background bucket-pruner cadence
  max_pinned_per_file: 100         # safety cap on operator pins
  pinned_grace_after_delete: "720h" # 30d for orphan recovery
  max_label_bytes: 2048
  retention_buckets:
    - { keep_for: "1h",  max_count: -1 }    # keep all in last hour
    - { keep_for: "24h", max_count: 24 }    # ~hourly in last day
    - { keep_for: "30d", max_count: 30 }    # ~daily in last month
    - { keep_for: "1y",  max_count: 12 }    # ~monthly in last year
```

`enabled: auto` only turns the feature on if every base path is btrfs.
`enabled: on` skips the check; `enabled: off` is a hard kill switch.

`retention_buckets` is intentionally empty by default: with no buckets
configured, no live versions are pruned (operator opt-in for retention).

## Lifecycle

**Auto-capture on write.** Every HTTP write to an existing file
(`PUT /v1/paths/...`, `PUT /v1/nodes/{id}/content`, ReplaceFile via
chunked upload finalize) snapshots the existing bytes first — but only
if the last captured version is older than `cooldown`. Within the
cooldown window the write proceeds without producing a new version.

**Auto-V1 on create.** A newly-uploaded file above
`min_size_for_auto_v1` gets an immediate V1 captured right after it
lands. Files below the floor are not auto-versioned (they're config
files / dotfiles that would just churn the version store).

**Manual snapshots.** `POST /v1/nodes/{id}/versions/snapshot` captures
the current bytes immediately, ignoring cooldown and the size floor.
Snapshots are pinned by default and carry an optional opaque label
(plain text or JSON, capped at `max_label_bytes`).

**Pin / unpin.** Pinned versions are exempt from the bucket pruner.
`max_pinned_per_file` caps the number of pinned versions per file —
operators can re-pin an existing pinned version to update its label
without consuming an additional slot.

**Restore.** Two flavors:
- *In-place* (default): the current bytes are first snapshotted (so a
  misclick is undoable), then replaced by the target version's bytes.
  File ID, parent, and Name are preserved.
- *As-new*: the version's bytes are placed in a fresh sibling file
  named `<base>-restored<ext>` (or a user-provided name), with `-N`
  suffix on conflict. The source file is untouched.

Restore is atomic per file via a per-file mutation lock so a concurrent
write can't slip between the snapshot-current and load-target steps.

## Retention algorithm

For each file with versions, the background pruner makes one of the
following decisions per version:

- **Pinned (live)**: kept always. Only removed by a manual
  `DELETE /v1/nodes/{id}/versions/{vid}`.
- **Live, unpinned**: subjected to the bucketed exponential-decay
  algorithm.
- **Orphan within grace**: kept until
  `pinned_grace_after_delete` elapses since the source file was
  deleted.
- **Orphan past grace**: purged regardless of pin status.

The bucket algorithm processes `retention_buckets` newest-window-first.
Each bucket's window is non-overlapping with newer buckets — a version
is considered by at most one bucket. Inside a bucket, `max_count: -1`
keeps everything; otherwise `max_count` evenly-spaced target points
are placed across the bucket window and the nearest unkept version to
each target is selected.

Versions older than every bucket window are unprotected and get pruned
on the next pass.

## API

| Method | Path | Body / Result |
|---|---|---|
| `GET` | `/v1/nodes/{id}/versions` | Cursor-paginated list. `?cursor=<vid>&limit=<n>` |
| `GET` | `/v1/nodes/{id}/versions/{vid}/content` | Streams version bytes (`application/octet-stream`) |
| `POST` | `/v1/nodes/{id}/versions/snapshot` | Body `{ "label"?: "..." }` → 201 + version meta. Pinned. |
| `POST` | `/v1/nodes/{id}/versions/{vid}/pin` | Body `{ "label"?: "..." \| null }` → 200 + version meta. Idempotent. |
| `POST` | `/v1/nodes/{id}/versions/{vid}/unpin` | → 200 + version meta. Label preserved. Idempotent. |
| `POST` | `/v1/nodes/{id}/versions/{vid}/restore` | Body `{ "asNewFile"?: bool, "name"?: string }` → 200 `{ node, asNew }` |
| `DELETE` | `/v1/nodes/{id}/versions/{vid}` | → 204. Works on any version including pinned. |

Status codes:
- `200 OK` / `201 Created` / `204 No Content` on success
- `400 Bad Request` on invalid version ID, oversized label, or missing
  required field
- `404 Not Found` when the file or version doesn't exist, or when
  versioning is disabled on this mount
- `409 Conflict` when `max_pinned_per_file` would be exceeded, or when
  `restore --asNewFile` exhausts the `-1..-999` suffix space

## Storage layout

Version bytes live in `<mount>/.fg-versions/<file-id>/<version-id>.bin`,
linked via `FICLONE` from the source file (or from the prior live
bytes, on overwrite). Reflinked blobs share extents with their source
on btrfs — multiple versions of an unchanged region cost one copy.

Version metadata (timestamp, size, mode, pinned flag, label,
`DeletedAt`) is stored in the Pebble index under a dedicated keyspace
(`0x04`). Listing is index-only; the filesystem is touched only when
fetching version content or restoring.

## Caveats

1. External writes are not versioned. Files modified by `cp`, `rsync`,
   or any non-HTTP path bypass the capture point.
2. The cooldown window can swallow rapid edits. A file edited at T+14m
   and again at T+16m (with the default 15m cooldown) only captures
   the T+16m state — the T+14m bytes are lost. Lower the cooldown for
   more granular history at the cost of storage growth.
3. `enabled: on` against a non-btrfs mount falls back to byte copies
   for capture. Storage usage will balloon proportional to the number
   of versions × file size; only use this configuration on small files
   or with aggressive retention.
4. Pinned versions outlive the bucket policy but not the source file's
   `pinned_grace_after_delete` window. The grace exists so an
   accidental `DELETE` is recoverable; after the window every version
   of the deleted file is purged.

## Operator runbook

- **Disable for a mount**: set `versioning.enabled: off` and restart.
  Existing version blobs and Pebble entries remain untouched on disk;
  re-enabling later restores access to them.
- **Reclaim version storage**: lower `retention_buckets` `max_count`
  values or drop a bucket entirely; the next pruner pass enforces the
  new policy. The pruner does not delete pinned versions even when
  retention contracts.
- **Inspect version storage usage**: `du -sh /<mount>/.fg-versions/`
  for a per-mount total; per-file is `du -sh
  /<mount>/.fg-versions/<file-id>/`.
- **Force a prune now**: there's no admin endpoint yet — restart the
  daemon or wait for the next `pruner_interval` tick.
