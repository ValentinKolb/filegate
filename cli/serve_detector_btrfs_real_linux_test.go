//go:build linux

package cli

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/valentinkolb/filegate/domain"
	"github.com/valentinkolb/filegate/infra/detect"
	"github.com/valentinkolb/filegate/infra/eventbus"
	"github.com/valentinkolb/filegate/infra/filesystem"
	indexpebble "github.com/valentinkolb/filegate/infra/pebble"
)

// setupRealBTRFSSubvol gates on FILEGATE_BTRFS_REAL=1 + FILEGATE_BTRFS_REAL_ROOT
// being set, then creates a fresh subvolume under the configured btrfs root and
// returns its path along with a cleanup function. Used by every real-btrfs
// edge-case test in this file so the gate logic stays in one place.
func setupRealBTRFSSubvol(t *testing.T) string {
	t.Helper()
	if os.Getenv("FILEGATE_BTRFS_REAL") != "1" {
		t.Skip("set FILEGATE_BTRFS_REAL=1 to run real btrfs test")
	}
	btrfsRoot := strings.TrimSpace(os.Getenv("FILEGATE_BTRFS_REAL_ROOT"))
	if btrfsRoot == "" {
		t.Skip("FILEGATE_BTRFS_REAL_ROOT is required")
	}
	if _, err := exec.LookPath("btrfs"); err != nil {
		t.Skip("btrfs command not found")
	}

	subvol := filepath.Join(btrfsRoot, fmt.Sprintf("filegate-it-%d", time.Now().UnixNano()))
	if out, err := exec.Command("btrfs", "subvolume", "create", subvol).CombinedOutput(); err != nil {
		t.Skipf("cannot create btrfs subvolume %q: %v (%s)", subvol, err, strings.TrimSpace(string(out)))
	}
	t.Cleanup(func() {
		_, _ = exec.Command("btrfs", "subvolume", "delete", subvol).CombinedOutput()
		_ = os.RemoveAll(subvol)
	})
	return subvol
}

// startRealBTRFSDetector wires up a service rooted at the given subvol with a
// real btrfs detector and runs the consumer goroutine. Returns the service,
// the mount name for ResolvePath, and the eventbus so tests that need to
// observe domain events can subscribe. Cancellation is automatic via
// t.Cleanup.
func startRealBTRFSDetector(t *testing.T, subvol string) (*domain.Service, string, domain.EventBus) {
	t.Helper()
	idx, err := indexpebble.Open(t.TempDir(), 32<<20)
	if err != nil {
		t.Fatalf("open index: %v", err)
	}
	bus := eventbus.New()
	svc, err := domain.NewService(idx, filesystem.New(), bus, []string{subvol}, 20000)
	if err != nil {
		_ = idx.Close()
		t.Fatalf("new service: %v", err)
	}
	t.Cleanup(func() { _ = idx.Close() })
	rootName := mustMountNameByPath(t, svc, subvol)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	runner, err := detect.New("btrfs", []string{subvol}, 40*time.Millisecond)
	if err != nil {
		t.Fatalf("new btrfs detector: %v", err)
	}
	runner.Start(ctx)
	t.Cleanup(func() { runner.Close() })
	go consumeDetectorEvents(ctx, svc, runner.Events())

	return svc, rootName, bus
}

// TestBTRFSRealBurstCreate verifies that 50 files written in a tight burst all
// reach the index. find-new reports many inodes per generation; this catches
// any batching/coalescing bug in the detector or consumer that would drop
// events under load.
func TestBTRFSRealBurstCreate(t *testing.T) {
	subvol := setupRealBTRFSSubvol(t)
	svc, rootName, _ := startRealBTRFSDetector(t, subvol)

	const n = 50
	// Write a sentinel first to walk past the loopback-btrfs init race window.
	sentinel := filepath.Join(subvol, "sentinel.txt")
	if err := os.WriteFile(sentinel, []byte("s"), 0o644); err != nil {
		t.Fatalf("sentinel write: %v", err)
	}
	waitForResolveWithStimulus(t, 15*time.Second, 150*time.Millisecond, func() {
		_ = os.WriteFile(sentinel, []byte("s"), 0o644)
	}, func() bool {
		_, err := svc.ResolvePath(rootName + "/sentinel.txt")
		return err == nil
	})

	for i := 0; i < n; i++ {
		p := filepath.Join(subvol, fmt.Sprintf("burst-%03d.txt", i))
		if err := os.WriteFile(p, []byte(fmt.Sprintf("payload-%d", i)), 0o644); err != nil {
			t.Fatalf("burst write %d: %v", i, err)
		}
	}

	waitUntil(t, 30*time.Second, func() bool {
		for i := 0; i < n; i++ {
			if _, err := svc.ResolvePath(fmt.Sprintf("%s/burst-%03d.txt", rootName, i)); err != nil {
				return false
			}
		}
		return true
	})
}

// TestBTRFSRealRenameWithinSubvol verifies that renaming a file within the
// same subvolume is reflected in the index: the old path becomes unresolvable
// and the new path resolves. Btrfs keeps the same inode across an in-subvol
// rename, so this exercises a path that pure inode-based tracking would miss.
func TestBTRFSRealRenameWithinSubvol(t *testing.T) {
	subvol := setupRealBTRFSSubvol(t)
	svc, rootName, _ := startRealBTRFSDetector(t, subvol)

	src := filepath.Join(subvol, "rename-src.txt")
	if err := os.WriteFile(src, []byte("hello"), 0o644); err != nil {
		t.Fatalf("write src: %v", err)
	}
	waitForResolveWithStimulus(t, 15*time.Second, 150*time.Millisecond, func() {
		_ = os.WriteFile(src, []byte("hello"), 0o644)
	}, func() bool {
		_, err := svc.ResolvePath(rootName + "/rename-src.txt")
		return err == nil
	})

	dst := filepath.Join(subvol, "rename-dst.txt")
	if err := os.Rename(src, dst); err != nil {
		t.Fatalf("rename: %v", err)
	}

	// Rename must produce both effects: new path appears, old path disappears.
	// We poll on each independently so the failure message identifies which side
	// regressed.
	waitUntil(t, 15*time.Second, func() bool {
		_, err := svc.ResolvePath(rootName + "/rename-dst.txt")
		return err == nil
	})
	waitUntil(t, 15*time.Second, func() bool {
		_, err := svc.ResolvePath(rootName + "/rename-src.txt")
		return err == domain.ErrNotFound
	})
}

// TestBTRFSRealNestedDirectoryCreate verifies that the detector traverses
// newly-created subdirectories rather than only emitting events for top-level
// inodes. Mirrors a common real-world pattern (extracting an archive, etc.).
func TestBTRFSRealNestedDirectoryCreate(t *testing.T) {
	subvol := setupRealBTRFSSubvol(t)
	svc, rootName, _ := startRealBTRFSDetector(t, subvol)

	// Sentinel to bypass loopback init race before exercising the nested case.
	sentinel := filepath.Join(subvol, "nested-sentinel.txt")
	if err := os.WriteFile(sentinel, []byte("s"), 0o644); err != nil {
		t.Fatalf("sentinel write: %v", err)
	}
	waitForResolveWithStimulus(t, 15*time.Second, 150*time.Millisecond, func() {
		_ = os.WriteFile(sentinel, []byte("s"), 0o644)
	}, func() bool {
		_, err := svc.ResolvePath(rootName + "/nested-sentinel.txt")
		return err == nil
	})

	deep := filepath.Join(subvol, "level1", "level2", "level3")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatalf("mkdir -p: %v", err)
	}
	leaves := []string{"a.txt", "b.txt", "c.txt"}
	for _, name := range leaves {
		if err := os.WriteFile(filepath.Join(deep, name), []byte(name), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	waitUntil(t, 20*time.Second, func() bool {
		if _, err := svc.ResolvePath(rootName + "/level1/level2/level3"); err != nil {
			return false
		}
		for _, name := range leaves {
			if _, err := svc.ResolvePath(rootName + "/level1/level2/level3/" + name); err != nil {
				return false
			}
		}
		return true
	})
}

// TestBTRFSRealBulkDelete verifies that deleting many files is reflected in the
// index. find-new does not report deletes directly (the inode is gone before
// the next generation scan); the detector must rely on a different mechanism
// (parent-dir generation change + rescan, or stat-based reconciliation) to
// notice. If this test fails, it documents a real gap rather than a flake.
func TestBTRFSRealBulkDelete(t *testing.T) {
	subvol := setupRealBTRFSSubvol(t)
	svc, rootName, _ := startRealBTRFSDetector(t, subvol)

	const n = 20
	// Sentinel to bypass loopback init race; subsequent writes ride the warm
	// detector.
	sentinel := filepath.Join(subvol, "bulk-sentinel.txt")
	if err := os.WriteFile(sentinel, []byte("s"), 0o644); err != nil {
		t.Fatalf("sentinel write: %v", err)
	}
	waitForResolveWithStimulus(t, 15*time.Second, 150*time.Millisecond, func() {
		_ = os.WriteFile(sentinel, []byte("s"), 0o644)
	}, func() bool {
		_, err := svc.ResolvePath(rootName + "/bulk-sentinel.txt")
		return err == nil
	})

	for i := 0; i < n; i++ {
		p := filepath.Join(subvol, fmt.Sprintf("bulk-%02d.txt", i))
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
	}
	waitUntil(t, 20*time.Second, func() bool {
		for i := 0; i < n; i++ {
			if _, err := svc.ResolvePath(fmt.Sprintf("%s/bulk-%02d.txt", rootName, i)); err != nil {
				return false
			}
		}
		return true
	})

	for i := 0; i < n; i++ {
		p := filepath.Join(subvol, fmt.Sprintf("bulk-%02d.txt", i))
		if err := os.Remove(p); err != nil {
			t.Fatalf("remove %d: %v", i, err)
		}
	}
	waitUntil(t, 20*time.Second, func() bool {
		for i := 0; i < n; i++ {
			_, err := svc.ResolvePath(fmt.Sprintf("%s/bulk-%02d.txt", rootName, i))
			if err != domain.ErrNotFound {
				return false
			}
		}
		return true
	})
}

// TestBTRFSRealXattrSelfLoopGuard verifies that filegate's own
// `user.filegate.id` xattr write does not feed back into the btrfs detector
// and trigger an endless cycle of re-syncs. After initial detection settles,
// any further EventUpdated traffic for the same path indicates a feedback
// loop. We measure activity over a fixed quiet window — a sleep is the
// honest tool here, since we are explicitly waiting on the absence of events.
func TestBTRFSRealXattrSelfLoopGuard(t *testing.T) {
	subvol := setupRealBTRFSSubvol(t)
	svc, rootName, bus := startRealBTRFSDetector(t, subvol)

	target := filepath.Join(subvol, "xattr-loop.txt")
	if err := os.WriteFile(target, []byte("payload"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	waitForResolveWithStimulus(t, 15*time.Second, 150*time.Millisecond, func() {
		_ = os.WriteFile(target, []byte("payload"), 0o644)
	}, func() bool {
		_, err := svc.ResolvePath(rootName + "/xattr-loop.txt")
		return err == nil
	})

	// Settle window: let any in-flight detector activity from the initial
	// detection drain before we start counting.
	time.Sleep(500 * time.Millisecond)

	var updates atomic.Int64
	bus.Subscribe(domain.EventUpdated, func(ev domain.Event) {
		// Only count events for our target path; ignore unrelated traffic so a
		// hot subvol root sync doesn't poison the count.
		if strings.Contains(ev.Path, "xattr-loop.txt") {
			updates.Add(1)
		}
	})

	// Quiet window: if the xattr write fed back into find-new we would see a
	// steady stream of events here. 2s is enough to span several detector ticks
	// at 40ms interval (~50 ticks).
	time.Sleep(2 * time.Second)

	got := updates.Load()
	if got > 2 {
		t.Fatalf("xattr self-loop suspected: %d EventUpdated for unchanged file in 2s quiet window", got)
	}
}
