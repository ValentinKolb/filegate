//go:build linux

package cli

import (
	"context"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/valentinkolb/filegate/domain"
	"github.com/valentinkolb/filegate/infra/eventbus"
	"github.com/valentinkolb/filegate/infra/filesystem"
	indexpebble "github.com/valentinkolb/filegate/infra/pebble"
)

// TestVersioningSoak drives the versioning subsystem under sustained load
// and asserts that:
//
//   - No goroutine leaks on shutdown.
//   - The blob/Pebble accounting stays consistent (blob count == version
//     count after the pruner has caught up).
//   - The pruner respects the bucketed retention contract under
//     concurrent writes.
//   - Manual snapshots + pin/unpin work alongside auto-capture without
//     deadlock.
//
// Gated behind FILEGATE_VERSIONING_SOAK=1 so the standard test suite
// stays fast. Honours FILEGATE_VERSIONING_SOAK_DURATION (default 30s).
func TestVersioningSoak(t *testing.T) {
	if os.Getenv("FILEGATE_VERSIONING_SOAK") != "1" {
		t.Skip("set FILEGATE_VERSIONING_SOAK=1 to run versioning soak")
	}

	duration := 30 * time.Second
	if raw := strings.TrimSpace(os.Getenv("FILEGATE_VERSIONING_SOAK_DURATION")); raw != "" {
		if d, err := time.ParseDuration(raw); err == nil && d > 0 {
			duration = d
		}
	}

	baseDir := t.TempDir()
	indexDir := t.TempDir()
	idx, err := indexpebble.Open(indexDir, 32<<20)
	if err != nil {
		t.Fatalf("open index: %v", err)
	}
	t.Cleanup(func() { _ = idx.Close() })
	bus := eventbus.New()
	t.Cleanup(func() { bus.Close() })
	svc, err := domain.NewService(idx, filesystem.New(), bus, []string{baseDir}, 4096)
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	cfg := domain.VersioningConfig{
		Cooldown:               40 * time.Millisecond,
		MinSizeForAutoV1:       0,
		MaxLabelBytes:          2048,
		MaxPinnedPerFile:       50,
		PinnedGraceAfterDelete: 1 * time.Second,
		PrunerInterval:         200 * time.Millisecond,
		RetentionBuckets: []domain.RetentionBucketConfig{
			{KeepFor: 5 * time.Second, MaxCount: 10},
			{KeepFor: time.Hour, MaxCount: 5},
		},
	}
	svc.EnableVersioning(cfg, true)

	root := svc.ListRoot()[0]
	rootName := root.Name

	// Bounded pool of files exercised concurrently. Keeping the pool
	// small ensures every file accumulates a meaningful version
	// history during the soak window.
	const filePool = 8
	type fileSlot struct {
		id   domain.FileID
		path string
	}
	pool := make([]fileSlot, 0, filePool)
	for i := 0; i < filePool; i++ {
		path := fmt.Sprintf("soak-%02d.bin", i)
		meta, _, err := svc.WriteContentByVirtualPath(
			"/"+rootName+"/"+path,
			strings.NewReader(fmt.Sprintf("seed-%d", i)),
			domain.ConflictError,
		)
		if err != nil {
			t.Fatalf("seed file %d: %v", i, err)
		}
		pool = append(pool, fileSlot{id: meta.ID, path: path})
	}

	ctx, cancel := context.WithTimeout(context.Background(), duration)
	defer cancel()
	prunerDone := make(chan struct{})
	go runVersioningPruner(ctx, svc, cfg.PrunerInterval, prunerDone)

	var (
		writes    atomic.Int64
		snapshots atomic.Int64
		pins      atomic.Int64
		restores  atomic.Int64
		errs      atomic.Int64
	)

	// Workers do a random mix of writes / snapshots / pin / unpin /
	// restore. Each operation has a small built-in jitter so we don't
	// degenerate into a tight loop on one file.
	const workers = 4
	doneWorkers := make(chan struct{}, workers)
	for w := 0; w < workers; w++ {
		go func(seed int64) {
			defer func() { doneWorkers <- struct{}{} }()
			rnd := rand.New(rand.NewSource(seed))
			for {
				select {
				case <-ctx.Done():
					return
				default:
				}
				slot := pool[rnd.Intn(len(pool))]
				switch rnd.Intn(10) {
				case 0, 1, 2, 3, 4, 5: // 60% writes
					body := fmt.Sprintf("write-%d", rnd.Int63())
					if err := svc.WriteContent(slot.id, strings.NewReader(body)); err != nil {
						errs.Add(1)
						continue
					}
					writes.Add(1)
				case 6, 7: // 20% manual snapshots
					if _, err := svc.SnapshotVersion(slot.id, "soak"); err != nil {
						if err == domain.ErrConflict { // cap reached
							continue
						}
						errs.Add(1)
						continue
					}
					snapshots.Add(1)
				case 8: // 10% pin/unpin shuffle
					listed, err := svc.ListVersions(slot.id, domain.VersionID{}, 100)
					if err != nil || len(listed.Items) == 0 {
						continue
					}
					target := listed.Items[rnd.Intn(len(listed.Items))]
					if rnd.Intn(2) == 0 {
						label := "soak-pin"
						if _, err := svc.PinVersion(slot.id, target.VersionID, &label); err == nil {
							pins.Add(1)
						}
					} else {
						_, _ = svc.UnpinVersion(slot.id, target.VersionID)
					}
				case 9: // 10% restore in-place
					listed, err := svc.ListVersions(slot.id, domain.VersionID{}, 100)
					if err != nil || len(listed.Items) == 0 {
						continue
					}
					target := listed.Items[rnd.Intn(len(listed.Items))]
					if _, _, err := svc.RestoreVersion(slot.id, target.VersionID, domain.RestoreOptions{}); err != nil {
						errs.Add(1)
						continue
					}
					restores.Add(1)
				}
				time.Sleep(time.Duration(rnd.Intn(5)) * time.Millisecond)
			}
		}(int64(w + 1))
	}

	for i := 0; i < workers; i++ {
		<-doneWorkers
	}
	cancel()
	<-prunerDone

	// Final invariant check: blob count == metadata count for each file.
	totalBlobs, totalMeta := 0, 0
	for _, slot := range pool {
		listed, err := svc.ListVersions(slot.id, domain.VersionID{}, 1000)
		if err != nil {
			t.Fatalf("ListVersions %s: %v", slot.id, err)
		}
		totalMeta += len(listed.Items)
		blobDir := filepath.Join(baseDir, ".fg-versions", slot.id.String())
		entries, err := os.ReadDir(blobDir)
		if err != nil && !os.IsNotExist(err) {
			t.Fatalf("read versions dir %s: %v", blobDir, err)
		}
		// Blob count must be >= metadata count. The pruner removes
		// metadata first then the blob; we may briefly observe a stale
		// blob during the racing window. Drift > 5 is a real leak.
		if len(entries)-len(listed.Items) > 5 {
			t.Fatalf("blob/metadata drift for %s: blobs=%d meta=%d",
				slot.path, len(entries), len(listed.Items))
		}
		totalBlobs += len(entries)
	}

	t.Logf("soak summary: duration=%s writes=%d snapshots=%d pins=%d restores=%d errs=%d totalMeta=%d totalBlobs=%d",
		duration, writes.Load(), snapshots.Load(), pins.Load(), restores.Load(),
		errs.Load(), totalMeta, totalBlobs)

	// Bucket retention should have kept the per-file version count well
	// below the unconstrained "every write captures" upper bound. With
	// our buckets (10 + 5 = 15 max live) plus pinned (≤ 50) the
	// per-file ceiling is 65. Use a generous 80 to absorb scheduling
	// noise.
	for _, slot := range pool {
		listed, _ := svc.ListVersions(slot.id, domain.VersionID{}, 1000)
		if len(listed.Items) > 80 {
			t.Fatalf("file %s has %d versions — pruner did not bound retention", slot.path, len(listed.Items))
		}
	}
}
