package domain

import (
	"testing"
	"time"
)

// Pure-function bucket-algorithm tests. No I/O, no service — just feed
// synthetic version distributions into pruneDecisions and assert which
// versions survive. These pin the retention contract independent of the
// pebble + filesystem plumbing.

func makeVersion(t *testing.T, ts int64, opts ...func(*VersionMeta)) VersionMeta {
	t.Helper()
	vid, err := newVersionID()
	if err != nil {
		t.Fatalf("newVersionID: %v", err)
	}
	v := VersionMeta{
		VersionID: vid,
		FileID:    FileID{0x01},
		Timestamp: ts,
		Size:      1024,
	}
	for _, opt := range opts {
		opt(&v)
	}
	return v
}

func pinned() func(*VersionMeta)  { return func(v *VersionMeta) { v.Pinned = true } }
func orphan(deletedAt int64) func(*VersionMeta) {
	return func(v *VersionMeta) { v.DeletedAt = deletedAt }
}

func TestBucketKeepsAllUnderMaxCount(t *testing.T) {
	now := time.Now().UnixMilli()
	versions := []VersionMeta{
		makeVersion(t, now-10),
		makeVersion(t, now-100),
		makeVersion(t, now-1000),
	}
	cfg := VersioningConfig{
		RetentionBuckets: []RetentionBucketConfig{
			{KeepFor: time.Hour, MaxCount: -1},
		},
	}
	got := pruneDecisions(versions, cfg, now)
	if len(got) != 0 {
		t.Fatalf("expected 0 deletions when all fit unlimited bucket, got %d", len(got))
	}
}

func TestBucketEvenlySpacedPickWhenOverCount(t *testing.T) {
	now := time.Now().UnixMilli()
	// 10 versions evenly spaced over 1h, MaxCount=3 should keep 3.
	versions := make([]VersionMeta, 0, 10)
	for i := 0; i < 10; i++ {
		// Versions ages 1m, 7m, 13m, ..., 55m
		ageMs := int64(1+i*6) * 60_000
		versions = append(versions, makeVersion(t, now-ageMs))
	}
	cfg := VersioningConfig{
		RetentionBuckets: []RetentionBucketConfig{
			{KeepFor: time.Hour, MaxCount: 3},
		},
	}
	deleted := pruneDecisions(versions, cfg, now)
	if kept := len(versions) - len(deleted); kept != 3 {
		t.Fatalf("kept=%d, want 3", kept)
	}
}

func TestBucketsLayerNonOverlapping(t *testing.T) {
	now := time.Now().UnixMilli()
	hourMs := int64(60 * 60 * 1000)
	versions := []VersionMeta{
		makeVersion(t, now-30*60*1000),     // 30 minutes ago — bucket 1h
		makeVersion(t, now-3*hourMs),       // 3h ago — bucket 24h
		makeVersion(t, now-12*hourMs),      // 12h ago — bucket 24h
		makeVersion(t, now-2*24*hourMs),    // 2d ago — bucket 30d
	}
	cfg := VersioningConfig{
		RetentionBuckets: []RetentionBucketConfig{
			{KeepFor: time.Hour, MaxCount: -1},
			{KeepFor: 24 * time.Hour, MaxCount: 24},
			{KeepFor: 30 * 24 * time.Hour, MaxCount: 30},
		},
	}
	deleted := pruneDecisions(versions, cfg, now)
	if len(deleted) != 0 {
		t.Fatalf("nothing should be deleted with these buckets: %v", deleted)
	}
}

func TestPinnedAlwaysKept(t *testing.T) {
	now := time.Now().UnixMilli()
	veryOld := now - 10*365*24*60*60*1000 // 10 years ago
	versions := []VersionMeta{
		makeVersion(t, veryOld, pinned()),
		makeVersion(t, veryOld), // unpinned, should be deleted
	}
	cfg := VersioningConfig{
		RetentionBuckets: []RetentionBucketConfig{
			{KeepFor: time.Hour, MaxCount: -1},
		},
	}
	deleted := pruneDecisions(versions, cfg, now)
	if len(deleted) != 1 {
		t.Fatalf("expected 1 deletion (the unpinned old one), got %d", len(deleted))
	}
	if deleted[0].Pinned {
		t.Fatalf("a pinned version was selected for deletion: %#v", deleted[0])
	}
}

func TestOrphanWithinGraceIsKept(t *testing.T) {
	now := time.Now().UnixMilli()
	deletedAt := now - 5*60*1000 // 5 min ago
	versions := []VersionMeta{
		makeVersion(t, deletedAt-1000, orphan(deletedAt)),
		makeVersion(t, deletedAt-2000, orphan(deletedAt), pinned()),
	}
	cfg := VersioningConfig{
		PinnedGraceAfterDelete: 30 * time.Minute,
	}
	deleted := pruneDecisions(versions, cfg, now)
	if len(deleted) != 0 {
		t.Fatalf("orphans within grace must be kept: %v", deleted)
	}
}

func TestOrphanPastGraceIsPurgedRegardlessOfPin(t *testing.T) {
	now := time.Now().UnixMilli()
	// Deleted 2h ago; grace = 1h.
	deletedAt := now - 2*60*60*1000
	versions := []VersionMeta{
		makeVersion(t, deletedAt-1000, orphan(deletedAt)),
		makeVersion(t, deletedAt-2000, orphan(deletedAt), pinned()),
	}
	cfg := VersioningConfig{
		PinnedGraceAfterDelete: time.Hour,
	}
	deleted := pruneDecisions(versions, cfg, now)
	if len(deleted) != 2 {
		t.Fatalf("both orphans (incl pinned) should be purged after grace, got %d deletions", len(deleted))
	}
}

func TestNoBucketsKeepsEverythingLive(t *testing.T) {
	now := time.Now().UnixMilli()
	versions := []VersionMeta{
		makeVersion(t, now-1000),
		makeVersion(t, now-1_000_000),
	}
	deleted := pruneDecisions(versions, VersioningConfig{}, now)
	if len(deleted) != 0 {
		t.Fatalf("with no buckets configured, nothing should be deleted: %v", deleted)
	}
}

func TestEvenlySpacedTargetsBoundary(t *testing.T) {
	got := evenlySpacedTargets(0, 100, 1)
	if len(got) != 1 || got[0] != 50 {
		t.Fatalf("count=1 should produce midpoint, got %v", got)
	}
	got = evenlySpacedTargets(0, 100, 5)
	if len(got) != 5 {
		t.Fatalf("count=5, len=%d", len(got))
	}
	if got[0] != 0 || got[4] != 100 {
		t.Fatalf("boundaries wrong: %v", got)
	}
}
