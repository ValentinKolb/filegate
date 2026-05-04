package domain

import (
	"errors"
	"log"
	"os"
	"sort"
	"time"
)

// PruneStats summarises one pruner pass.
type PruneStats struct {
	FilesScanned    int
	VersionsKept    int
	VersionsDeleted int
	OrphansPurged   int
	BlobsDeleted    int
	Errors          int
}

// PruneVersions runs one full pruning pass across every file with at
// least one version. Safe to call from a background goroutine on a
// fixed cadence; the work is idempotent and bounded by the number of
// versions stored.
//
// Two policies apply per file:
//
//  1. Live versions (DeletedAt == 0): retained according to the
//     bucketed exponential-decay algorithm. Pinned versions are exempt.
//
//  2. Orphan versions (DeletedAt > 0): retained until
//     pinned_grace_after_delete elapses, then purged regardless of
//     pin status.
//
// Returns a snapshot of work done. Per-file errors are counted in
// Stats.Errors and logged; the pass continues so one broken entry
// doesn't poison the rest of the index.
func (s *Service) PruneVersions() (PruneStats, error) {
	if !s.VersioningEnabled() {
		return PruneStats{}, nil
	}
	cfg := s.versioningSnapshot()
	stats := PruneStats{}
	now := time.Now().UnixMilli()

	err := s.idx.ForEachFileVersions(func(fileID FileID, versions []VersionMeta) error {
		stats.FilesScanned++
		toDelete := pruneDecisions(versions, cfg, now)
		stats.VersionsKept += len(versions) - len(toDelete)
		for _, v := range toDelete {
			if v.IsOrphan() {
				stats.OrphansPurged++
			}
			if err := s.deleteVersionBlobAndRecord(v); err != nil {
				stats.Errors++
				log.Printf("[filegate] versioning prune: delete %s/%s failed: %v",
					fileID, v.VersionID, err)
				continue
			}
			stats.VersionsDeleted++
			stats.BlobsDeleted++
		}
		return nil
	})
	return stats, err
}

// DeleteVersion removes a single version (blob + Pebble entry). Works on
// any version, including pinned ones — manual delete is the operator
// override.
func (s *Service) DeleteVersion(fileID FileID, versionID VersionID) error {
	if !s.VersioningEnabled() {
		return ErrUnsupportedFS
	}
	mu := s.versionLocks.Acquire(fileID)
	mu.Lock()
	defer mu.Unlock()

	meta, err := s.idx.GetVersion(fileID, versionID)
	if err != nil {
		return err
	}
	return s.deleteVersionBlobAndRecord(*meta)
}

// deleteVersionBlobAndRecord removes the on-disk blob and the Pebble
// entry. Blob deletion is best-effort: a missing blob doesn't block the
// index cleanup (the version is then "already gone" from the user's
// perspective and the next snapshot reshuffles state correctly).
func (s *Service) deleteVersionBlobAndRecord(meta VersionMeta) error {
	srcAbs, err := s.ResolveAbsPath(meta.FileID)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return err
	}
	if srcAbs != "" {
		_, blobPath, perr := s.versionStoragePath(meta.FileID, srcAbs, meta.VersionID)
		if perr == nil {
			if rmErr := os.Remove(blobPath); rmErr != nil && !errors.Is(rmErr, os.ErrNotExist) {
				log.Printf("[filegate] versioning: blob remove %s failed: %v", blobPath, rmErr)
			}
		}
	}
	return s.idx.Batch(func(b Batch) error {
		b.DelVersion(meta.FileID, meta.VersionID)
		return nil
	})
}

// pruneDecisions decides which versions to delete for one file. Pure
// function (no I/O) so it's straightforward to unit-test against
// synthetic distributions.
//
// Decision tree per version:
//   - Orphan + grace expired       -> delete
//   - Orphan + within grace        -> keep
//   - Pinned (live)                -> keep (always)
//   - Live unpinned + bucket-kept  -> keep
//   - Live unpinned + not picked   -> delete
func pruneDecisions(versions []VersionMeta, cfg VersioningConfig, now int64) []VersionMeta {
	if len(versions) == 0 {
		return nil
	}
	graceMs := cfg.PinnedGraceAfterDelete.Milliseconds()
	var live, orphan []VersionMeta
	for _, v := range versions {
		if v.IsOrphan() {
			orphan = append(orphan, v)
		} else {
			live = append(live, v)
		}
	}

	toDelete := make([]VersionMeta, 0)
	for _, v := range orphan {
		if graceMs > 0 && now-v.DeletedAt < graceMs {
			continue // still within grace
		}
		toDelete = append(toDelete, v)
	}

	keep := bucketKeepSet(live, cfg.RetentionBuckets, now)
	for _, v := range live {
		if v.Pinned {
			continue
		}
		if !keep[v.VersionID] {
			toDelete = append(toDelete, v)
		}
	}
	return toDelete
}

// bucketKeepSet runs the bucketed exponential-decay retention algorithm.
// Buckets are processed newest-window-first; each bucket's effective
// window is non-overlapping with newer buckets so a single version is
// considered by at most one bucket.
//
// Within a bucket window, MaxCount=-1 keeps everything, otherwise we
// place MaxCount evenly-spaced target points across the window and pick
// the nearest version to each target.
//
// Pinned versions are layered on top by the caller — this function
// only computes the bucket-driven keepers.
func bucketKeepSet(versions []VersionMeta, buckets []RetentionBucketConfig, now int64) map[VersionID]bool {
	keep := make(map[VersionID]bool, len(versions))
	if len(versions) == 0 {
		return keep
	}
	if len(buckets) == 0 {
		// No retention policy = retain everything indefinitely.
		for _, v := range versions {
			keep[v.VersionID] = true
		}
		return keep
	}

	sortedBuckets := append([]RetentionBucketConfig(nil), buckets...)
	sort.Slice(sortedBuckets, func(i, j int) bool {
		return sortedBuckets[i].KeepFor < sortedBuckets[j].KeepFor
	})

	newerEdge := now
	for _, bucket := range sortedBuckets {
		olderEdge := now - bucket.KeepFor.Milliseconds()
		var inWindow []VersionMeta
		for _, v := range versions {
			if keep[v.VersionID] {
				continue
			}
			if v.Timestamp > olderEdge && v.Timestamp <= newerEdge {
				inWindow = append(inWindow, v)
			}
		}

		if bucket.MaxCount < 0 || len(inWindow) <= bucket.MaxCount {
			for _, v := range inWindow {
				keep[v.VersionID] = true
			}
		} else {
			targets := evenlySpacedTargets(olderEdge, newerEdge, bucket.MaxCount)
			for _, t := range targets {
				bestIdx := -1
				bestDist := int64(-1)
				for i, v := range inWindow {
					if keep[v.VersionID] {
						continue
					}
					dist := absDiff(v.Timestamp, t)
					if bestDist < 0 || dist < bestDist {
						bestDist = dist
						bestIdx = i
					}
				}
				if bestIdx >= 0 {
					keep[inWindow[bestIdx].VersionID] = true
				}
			}
		}

		newerEdge = olderEdge
	}
	return keep
}

// evenlySpacedTargets places count target points across the [start, end]
// window. The first and last targets land on the boundaries so the
// extremes of each bucket window get represented; intermediate points
// are evenly distributed.
func evenlySpacedTargets(start, end int64, count int) []int64 {
	if count <= 0 || start >= end {
		return nil
	}
	if count == 1 {
		return []int64{(start + end) / 2}
	}
	out := make([]int64, count)
	step := (end - start) / int64(count-1)
	for i := 0; i < count; i++ {
		out[i] = start + int64(i)*step
	}
	return out
}

func absDiff(a, b int64) int64 {
	if a > b {
		return a - b
	}
	return b - a
}
