//go:build linux

// Extended real-btrfs coverage. Each test in this file exercises a common
// filesystem operation that production deployments will see: atomic-replace
// from editors and upload pipelines, recursive deletes, cross-mount moves,
// xattr tampering, hard links, btrfs reflinks, etc.
//
// Tests are grouped into TIER 1 (high-severity real-world failure modes that
// almost every Filegate operator will hit) and TIER 2 (medium-severity
// edge cases that matter for completeness). The setup helpers
// (setupRealBTRFSSubvol, startRealBTRFSDetector) live in
// serve_detector_btrfs_real_linux_test.go.

package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/valentinkolb/filegate/domain"
)

// runCmd executes a command and fatals with combined output on failure.
// Used for setfattr / cp / btrfs invocations that must succeed for the test
// to be meaningful (vs the "skip if tool missing" pattern used at setup
// time).
func runCmd(t *testing.T, name string, args ...string) {
	t.Helper()
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		t.Fatalf("%s %s failed: %v (%s)", name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
}

// requireBin skips the test if the named binary isn't on PATH. Lets the
// test live alongside others that don't need it without breaking the suite
// when a CI image happens to omit it.
func requireBin(t *testing.T, bin string) {
	t.Helper()
	if _, err := exec.LookPath(bin); err != nil {
		t.Skipf("binary %q not found", bin)
	}
}

// =============================================================================
// TIER 1 — HIGH severity real-world operations
// =============================================================================

// TestBTRFSRealAtomicReplaceViaTempRename exercises the editor / upload
// pattern: write content into a temporary file then rename it over the
// existing target. The destination's inode changes because the rename
// replaces the underlying directory entry. Filegate must end up with the
// new content size and a coherent index entry; the old (now orphaned)
// inode metadata must not surface in any path lookup.
func TestBTRFSRealAtomicReplaceViaTempRename(t *testing.T) {
	subvol := setupRealBTRFSSubvol(t)
	svc, rootName, _ := startRealBTRFSDetector(t, subvol)

	target := filepath.Join(subvol, "atomic.txt")
	if err := os.WriteFile(target, []byte("original-content"), 0o644); err != nil {
		t.Fatalf("seed target: %v", err)
	}
	waitForResolveWithStimulus(t, 15*time.Second, 150*time.Millisecond, func() {
		_ = os.WriteFile(target, []byte("original-content"), 0o644)
	}, func() bool {
		_, err := svc.ResolvePath(rootName + "/atomic.txt")
		return err == nil
	})

	// Atomic-replace: write tmp, then rename over target.
	tmp := filepath.Join(subvol, "atomic.txt.tmp")
	if err := os.WriteFile(tmp, []byte("replaced-with-longer-content"), 0o644); err != nil {
		t.Fatalf("write tmp: %v", err)
	}
	if err := os.Rename(tmp, target); err != nil {
		t.Fatalf("atomic rename: %v", err)
	}

	wantSize := int64(len("replaced-with-longer-content"))
	waitUntil(t, 15*time.Second, func() bool {
		id, err := svc.ResolvePath(rootName + "/atomic.txt")
		if err != nil {
			return false
		}
		meta, err := svc.GetFile(id)
		return err == nil && meta.Size == wantSize
	})
}

// TestBTRFSRealDirectoryMoveWithContents verifies that renaming a populated
// directory updates the index for both the directory entry and all its
// descendants. Filegate stores parent-child relationships by ID, so the
// directory's own rename should be enough — descendant paths resolve
// transitively. The test pins this assumption.
func TestBTRFSRealDirectoryMoveWithContents(t *testing.T) {
	subvol := setupRealBTRFSSubvol(t)
	svc, rootName, _ := startRealBTRFSDetector(t, subvol)

	srcDir := filepath.Join(subvol, "old-dir")
	if err := os.MkdirAll(filepath.Join(srcDir, "sub"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	leaves := []string{"a.txt", "b.txt", "sub/c.txt"}
	for _, name := range leaves {
		if err := os.WriteFile(filepath.Join(srcDir, name), []byte(name), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	waitForResolveWithStimulus(t, 15*time.Second, 150*time.Millisecond, func() {
		_ = os.WriteFile(filepath.Join(srcDir, "a.txt"), []byte("a.txt"), 0o644)
	}, func() bool {
		for _, name := range leaves {
			if _, err := svc.ResolvePath(rootName + "/old-dir/" + name); err != nil {
				return false
			}
		}
		return true
	})

	dstDir := filepath.Join(subvol, "new-dir")
	if err := os.Rename(srcDir, dstDir); err != nil {
		t.Fatalf("rename dir: %v", err)
	}

	// New tree resolves.
	waitUntil(t, 15*time.Second, func() bool {
		for _, name := range leaves {
			if _, err := svc.ResolvePath(rootName + "/new-dir/" + name); err != nil {
				return false
			}
		}
		return true
	})
	// Old tree gone.
	waitUntil(t, 15*time.Second, func() bool {
		for _, name := range leaves {
			if _, err := svc.ResolvePath(rootName + "/old-dir/" + name); err != domain.ErrNotFound {
				return false
			}
		}
		_, err := svc.ResolvePath(rootName + "/old-dir")
		return err == domain.ErrNotFound
	})
}

// TestBTRFSRealRecursiveDirectoryDelete verifies that a recursive rm
// (directory plus contents disappearing in one shot) is handled cleanly.
// Differs from BulkDelete because the parent directory itself goes away
// in the same generation.
func TestBTRFSRealRecursiveDirectoryDelete(t *testing.T) {
	subvol := setupRealBTRFSSubvol(t)
	svc, rootName, _ := startRealBTRFSDetector(t, subvol)

	dir := filepath.Join(subvol, "doomed")
	if err := os.MkdirAll(filepath.Join(dir, "nested"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	for i := 0; i < 8; i++ {
		p := filepath.Join(dir, fmt.Sprintf("f-%02d.txt", i))
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "nested", "deep.txt"), []byte("d"), 0o644); err != nil {
		t.Fatalf("nested: %v", err)
	}
	waitForResolveWithStimulus(t, 15*time.Second, 150*time.Millisecond, func() {
		_ = os.WriteFile(filepath.Join(dir, "f-00.txt"), []byte("x"), 0o644)
	}, func() bool {
		_, errA := svc.ResolvePath(rootName + "/doomed/f-00.txt")
		_, errB := svc.ResolvePath(rootName + "/doomed/nested/deep.txt")
		return errA == nil && errB == nil
	})

	if err := os.RemoveAll(dir); err != nil {
		t.Fatalf("remove all: %v", err)
	}

	waitUntil(t, 20*time.Second, func() bool {
		// Pin a few sentinel paths instead of scanning everything; if these
		// are gone the rest almost certainly is too.
		for _, p := range []string{"doomed", "doomed/f-00.txt", "doomed/f-07.txt", "doomed/nested/deep.txt"} {
			if _, err := svc.ResolvePath(rootName + "/" + p); err != domain.ErrNotFound {
				return false
			}
		}
		return true
	})
}

// TestBTRFSRealMoveOutOfMount covers a file that leaves the watched mount
// entirely (cross-filesystem move = copy + unlink on the source side). The
// detector should observe the source-side unlink and clear the index entry.
func TestBTRFSRealMoveOutOfMount(t *testing.T) {
	subvol := setupRealBTRFSSubvol(t)
	svc, rootName, _ := startRealBTRFSDetector(t, subvol)

	src := filepath.Join(subvol, "leaving.txt")
	if err := os.WriteFile(src, []byte("bye"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	waitForResolveWithStimulus(t, 15*time.Second, 150*time.Millisecond, func() {
		_ = os.WriteFile(src, []byte("bye"), 0o644)
	}, func() bool {
		_, err := svc.ResolvePath(rootName + "/leaving.txt")
		return err == nil
	})

	// Use t.TempDir() — guaranteed to be on a different filesystem than the
	// btrfs loopback mount (it's the container's overlay/tmpfs).
	dest := filepath.Join(t.TempDir(), "out.txt")
	// Cross-fs rename will fall back to copy+remove behavior depending on
	// the runtime; we don't care which, only that the source disappears.
	if err := os.Rename(src, dest); err != nil {
		// rename(2) returns EXDEV across filesystems. Fall back to manual
		// copy+unlink which is what `mv` does in that case.
		if data, rerr := os.ReadFile(src); rerr == nil {
			if werr := os.WriteFile(dest, data, 0o644); werr == nil {
				_ = os.Remove(src)
			} else {
				t.Fatalf("manual copy fallback failed: %v", werr)
			}
		} else {
			t.Fatalf("read src for fallback: %v", rerr)
		}
	}

	waitUntil(t, 15*time.Second, func() bool {
		_, err := svc.ResolvePath(rootName + "/leaving.txt")
		return err == domain.ErrNotFound
	})
}

// TestBTRFSRealMoveIntoMount covers a file arriving into the watched mount
// from outside (cross-fs move = copy + unlink on source). The destination
// receives a new inode with no Filegate xattr; the detector must index it
// as a fresh file with a fresh ID.
func TestBTRFSRealMoveIntoMount(t *testing.T) {
	subvol := setupRealBTRFSSubvol(t)
	svc, rootName, _ := startRealBTRFSDetector(t, subvol)

	src := filepath.Join(t.TempDir(), "incoming.txt")
	if err := os.WriteFile(src, []byte("hello-from-outside"), 0o644); err != nil {
		t.Fatalf("seed external: %v", err)
	}
	dest := filepath.Join(subvol, "arrived.txt")
	if err := os.Rename(src, dest); err != nil {
		// Cross-fs fallback (see MoveOutOfMount for why).
		data, rerr := os.ReadFile(src)
		if rerr != nil {
			t.Fatalf("read src for fallback: %v", rerr)
		}
		if werr := os.WriteFile(dest, data, 0o644); werr != nil {
			t.Fatalf("manual copy: %v", werr)
		}
		_ = os.Remove(src)
	}

	waitForResolveWithStimulus(t, 15*time.Second, 150*time.Millisecond, func() {
		_ = os.WriteFile(dest, []byte("hello-from-outside"), 0o644)
	}, func() bool {
		id, err := svc.ResolvePath(rootName + "/arrived.txt")
		if err != nil {
			return false
		}
		meta, err := svc.GetFile(id)
		return err == nil && meta.Size == int64(len("hello-from-outside"))
	})
}

// TestBTRFSRealTruncate exercises truncate(2) — file size shrinks without
// going through the normal write path. The index size must follow.
func TestBTRFSRealTruncate(t *testing.T) {
	subvol := setupRealBTRFSSubvol(t)
	svc, rootName, _ := startRealBTRFSDetector(t, subvol)

	target := filepath.Join(subvol, "truncate.txt")
	if err := os.WriteFile(target, []byte("AAAAAAAAAAAAAAAAAAAA"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	waitForResolveWithStimulus(t, 15*time.Second, 150*time.Millisecond, func() {
		_ = os.WriteFile(target, []byte("AAAAAAAAAAAAAAAAAAAA"), 0o644)
	}, func() bool {
		id, err := svc.ResolvePath(rootName + "/truncate.txt")
		if err != nil {
			return false
		}
		meta, err := svc.GetFile(id)
		return err == nil && meta.Size == 20
	})

	if err := os.Truncate(target, 5); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	waitUntil(t, 15*time.Second, func() bool {
		id, err := svc.ResolvePath(rootName + "/truncate.txt")
		if err != nil {
			return false
		}
		meta, err := svc.GetFile(id)
		return err == nil && meta.Size == 5
	})
}

// TestBTRFSRealExternalXattrRemoval covers an admin or backup tool stripping
// the user.filegate.id xattr off a file. Filegate's GetID will return
// "no xattr" on the next sync; a fresh ID gets assigned. The old entity is
// orphaned but the path must still resolve to the new ID.
func TestBTRFSRealExternalXattrRemoval(t *testing.T) {
	requireBin(t, "setfattr")
	subvol := setupRealBTRFSSubvol(t)
	svc, rootName, _ := startRealBTRFSDetector(t, subvol)

	target := filepath.Join(subvol, "xattr-strip.txt")
	if err := os.WriteFile(target, []byte("payload"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	waitForResolveWithStimulus(t, 15*time.Second, 150*time.Millisecond, func() {
		_ = os.WriteFile(target, []byte("payload"), 0o644)
	}, func() bool {
		_, err := svc.ResolvePath(rootName + "/xattr-strip.txt")
		return err == nil
	})

	originalID, err := svc.ResolvePath(rootName + "/xattr-strip.txt")
	if err != nil {
		t.Fatalf("resolve before strip: %v", err)
	}

	runCmd(t, "setfattr", "-x", domain.XAttrIDKey(), target)

	// Touch to force a generation bump so the detector re-syncs the path.
	if err := os.WriteFile(target, []byte("touched"), 0o644); err != nil {
		t.Fatalf("touch: %v", err)
	}

	waitUntil(t, 15*time.Second, func() bool {
		id, err := svc.ResolvePath(rootName + "/xattr-strip.txt")
		if err != nil {
			return false
		}
		// Fresh ID assigned (different from before the strip).
		return id != originalID
	})
}

// TestBTRFSRealCorruptedXattrValue covers an admin / disk tool putting a
// non-UUID byte sequence into user.filegate.id. The path must continue to
// resolve: Filegate's getID returns ErrNotExist when the xattr isn't
// exactly 16 bytes, so syncSingle treats it as missing and reissues a
// fresh UUID. We pin that the path is still resolvable (with a new ID)
// after the corruption is healed by the next sync.
func TestBTRFSRealCorruptedXattrValue(t *testing.T) {
	requireBin(t, "setfattr")
	subvol := setupRealBTRFSSubvol(t)
	svc, rootName, _ := startRealBTRFSDetector(t, subvol)

	target := filepath.Join(subvol, "xattr-garbage.txt")
	if err := os.WriteFile(target, []byte("data"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	waitForResolveWithStimulus(t, 15*time.Second, 150*time.Millisecond, func() {
		_ = os.WriteFile(target, []byte("data"), 0o644)
	}, func() bool {
		_, err := svc.ResolvePath(rootName + "/xattr-garbage.txt")
		return err == nil
	})

	originalID, err := svc.ResolvePath(rootName + "/xattr-garbage.txt")
	if err != nil {
		t.Fatalf("resolve before corrupt: %v", err)
	}

	runCmd(t, "setfattr", "-n", domain.XAttrIDKey(), "-v", "definitely-not-a-uuid", target)
	// Bump generation so the detector picks up the file with the corrupted
	// xattr in place.
	if err := os.WriteFile(target, []byte("data-after-corrupt"), 0o644); err != nil {
		t.Fatalf("touch: %v", err)
	}

	// Filegate's syncSingle should treat the bad xattr as missing,
	// generate a fresh UUID, and re-index the path. The new ID will
	// differ from the original.
	waitUntil(t, 15*time.Second, func() bool {
		id, err := svc.ResolvePath(rootName + "/xattr-garbage.txt")
		return err == nil && id != originalID
	})
}

// TestBTRFSRealDuplicateIDViaCpA covers `cp -a` which preserves xattrs by
// design — the copy claims the same Filegate stable ID as the source.
// Filegate must produce a deterministic outcome: both paths resolve via
// some valid ID, and the index isn't corrupted.
//
// Documented expectation as of writing: the second sync wins because
// PutEntity overwrites the entity record by ID. The source path's child
// entry gets cleaned up by the rename-detection logic, leaving only the
// copy resolvable. This is suboptimal but not crashy; a proper fix would
// detect xattr duplication and reissue an ID for one of the paths.
func TestBTRFSRealDuplicateIDViaCpA(t *testing.T) {
	requireBin(t, "cp")
	subvol := setupRealBTRFSSubvol(t)
	svc, rootName, _ := startRealBTRFSDetector(t, subvol)

	src := filepath.Join(subvol, "original.txt")
	if err := os.WriteFile(src, []byte("orig"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	waitForResolveWithStimulus(t, 15*time.Second, 150*time.Millisecond, func() {
		_ = os.WriteFile(src, []byte("orig"), 0o644)
	}, func() bool {
		_, err := svc.ResolvePath(rootName + "/original.txt")
		return err == nil
	})

	dst := filepath.Join(subvol, "copy.txt")
	runCmd(t, "cp", "-a", src, dst)

	// Wait until either path's existence stabilises. We don't assert which
	// one wins — only that the index converges and Filegate is still
	// operational. A panic in syncSingle / reconcile would surface as a
	// goroutine fatal somewhere; this test is mostly a smoke for the
	// duplicate-ID corner case.
	waitUntil(t, 15*time.Second, func() bool {
		_, errA := svc.ResolvePath(rootName + "/original.txt")
		_, errB := svc.ResolvePath(rootName + "/copy.txt")
		return errA == nil || errB == nil
	})
	// At least one of the two must be resolvable for Filegate to be useful.
	_, errA := svc.ResolvePath(rootName + "/original.txt")
	_, errB := svc.ResolvePath(rootName + "/copy.txt")
	if errA != nil && errB != nil {
		t.Fatalf("after cp -a, neither path resolves: original=%v copy=%v", errA, errB)
	}
}

// TestBTRFSRealSymlinkCreateDeleteRename verifies the symlink-rejection
// invariant: Filegate refuses to index symlinks (syncSingle returns
// ErrForbidden) but must not crash or stall the detector loop when one
// appears. The regular file the symlink points at must remain indexed.
func TestBTRFSRealSymlinkCreateDeleteRename(t *testing.T) {
	subvol := setupRealBTRFSSubvol(t)
	svc, rootName, _ := startRealBTRFSDetector(t, subvol)

	target := filepath.Join(subvol, "real-target.txt")
	if err := os.WriteFile(target, []byte("real"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	waitForResolveWithStimulus(t, 15*time.Second, 150*time.Millisecond, func() {
		_ = os.WriteFile(target, []byte("real"), 0o644)
	}, func() bool {
		_, err := svc.ResolvePath(rootName + "/real-target.txt")
		return err == nil
	})

	link := filepath.Join(subvol, "the-link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	// Stimulate to make sure the detector tick that sees the symlink runs.
	if err := os.WriteFile(target, []byte("touched-after-link"), 0o644); err != nil {
		t.Fatalf("touch: %v", err)
	}

	// Real target must continue to resolve normally.
	waitUntil(t, 10*time.Second, func() bool {
		id, err := svc.ResolvePath(rootName + "/real-target.txt")
		if err != nil {
			return false
		}
		meta, err := svc.GetFile(id)
		return err == nil && meta.Size == int64(len("touched-after-link"))
	})

	// The symlink itself should NOT appear in the index (rejected).
	if _, err := svc.ResolvePath(rootName + "/the-link"); !errors.Is(err, domain.ErrNotFound) && !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("symlink should be unresolvable, got err=%v", err)
	}

	// Remove and recreate with a different target — Filegate must still not
	// index it and the real file must remain unaffected.
	if err := os.Remove(link); err != nil {
		t.Fatalf("remove link: %v", err)
	}
	other := filepath.Join(subvol, "other.txt")
	if err := os.WriteFile(other, []byte("o"), 0o644); err != nil {
		t.Fatalf("seed other: %v", err)
	}
	if err := os.Symlink(other, link); err != nil {
		t.Fatalf("re-symlink: %v", err)
	}
	if _, err := svc.ResolvePath(rootName + "/real-target.txt"); err != nil {
		t.Fatalf("real target lost during symlink churn: %v", err)
	}
}

// TestBTRFSRealUnlinkOneOfHardLinks verifies that removing one path of a
// hard-linked pair leaves the other path fully usable. The shared entity
// (with the surviving inode) MUST stay valid; reading content via the
// surviving path must work.
//
// Known limitation: the unlinked alias's child entry may linger in the
// index. Filegate has no reverse "child entries by entity ID" lookup, so
// stale child entries pointing at a still-living shared entity are not
// proactively cleaned. The next time something forces a syncSingle on the
// alias path, the missing-on-disk file will be cleaned up via reconcile,
// but ambient detector events alone don't trigger that. The test below
// pins what we DO guarantee — primary continues to work — and notes the
// gap explicitly so the next reader doesn't think this is fully solved.
func TestBTRFSRealUnlinkOneOfHardLinks(t *testing.T) {
	subvol := setupRealBTRFSSubvol(t)
	svc, rootName, _ := startRealBTRFSDetector(t, subvol)

	primary := filepath.Join(subvol, "hl-keep.txt")
	if err := os.WriteFile(primary, []byte("shared"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	waitForResolveWithStimulus(t, 15*time.Second, 150*time.Millisecond, func() {
		_ = os.WriteFile(primary, []byte("shared"), 0o644)
	}, func() bool {
		_, err := svc.ResolvePath(rootName + "/hl-keep.txt")
		return err == nil
	})

	alias := filepath.Join(subvol, "hl-drop.txt")
	if err := os.Link(primary, alias); err != nil {
		t.Fatalf("hardlink: %v", err)
	}
	if err := svc.Rescan(); err != nil {
		t.Fatalf("rescan: %v", err)
	}
	waitUntil(t, 5*time.Second, func() bool {
		_, errA := svc.ResolvePath(rootName + "/hl-keep.txt")
		_, errB := svc.ResolvePath(rootName + "/hl-drop.txt")
		return errA == nil && errB == nil
	})

	if err := os.Remove(alias); err != nil {
		t.Fatalf("unlink alias: %v", err)
	}
	// Stimulate so detector picks up the inode generation change (nlink
	// dropped from 2 to 1).
	if err := os.WriteFile(primary, []byte("after-unlink"), 0o644); err != nil {
		t.Fatalf("touch primary: %v", err)
	}

	// Surviving path must remain fully usable: resolves, reads back, and
	// reflects the content we just wrote.
	waitUntil(t, 15*time.Second, func() bool {
		id, err := svc.ResolvePath(rootName + "/hl-keep.txt")
		if err != nil {
			return false
		}
		meta, err := svc.GetFile(id)
		return err == nil && meta.Size == int64(len("after-unlink"))
	})
}

// TestBTRFSRealReflinkCopy exercises btrfs's `cp --reflink=always` which
// shares content extents but allocates a fresh inode for the copy. Default
// `cp` (no `-a`) does not preserve xattrs, so the copy gets a fresh
// Filegate ID — the two paths should resolve to different IDs, both with
// the original content.
func TestBTRFSRealReflinkCopy(t *testing.T) {
	requireBin(t, "cp")
	subvol := setupRealBTRFSSubvol(t)
	svc, rootName, _ := startRealBTRFSDetector(t, subvol)

	src := filepath.Join(subvol, "reflink-src.txt")
	if err := os.WriteFile(src, []byte("shared-extents"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	waitForResolveWithStimulus(t, 15*time.Second, 150*time.Millisecond, func() {
		_ = os.WriteFile(src, []byte("shared-extents"), 0o644)
	}, func() bool {
		_, err := svc.ResolvePath(rootName + "/reflink-src.txt")
		return err == nil
	})

	dst := filepath.Join(subvol, "reflink-clone.txt")
	out, err := exec.Command("cp", "--reflink=always", src, dst).CombinedOutput()
	if err != nil {
		t.Skipf("cp --reflink not supported on this kernel/btrfs build: %v (%s)",
			err, strings.TrimSpace(string(out)))
	}

	waitForResolveWithStimulus(t, 15*time.Second, 150*time.Millisecond, func() {
		_ = os.WriteFile(dst, []byte("shared-extents"), 0o644)
	}, func() bool {
		_, err := svc.ResolvePath(rootName + "/reflink-clone.txt")
		return err == nil
	})

	srcID, err := svc.ResolvePath(rootName + "/reflink-src.txt")
	if err != nil {
		t.Fatalf("resolve src: %v", err)
	}
	dstID, err := svc.ResolvePath(rootName + "/reflink-clone.txt")
	if err != nil {
		t.Fatalf("resolve clone: %v", err)
	}
	if srcID == dstID {
		t.Fatalf("reflink copy must have its own ID; got identical %v for both paths", srcID)
	}
}

// =============================================================================
// TIER 2 — MEDIUM severity edge cases
// =============================================================================

// TestBTRFSRealChmodMetadataOnly verifies that a chmod-only change (no
// content, no rename) is reflected in the index Mode. Btrfs bumps the
// inode generation on metadata changes, so find-new should emit.
func TestBTRFSRealChmodMetadataOnly(t *testing.T) {
	subvol := setupRealBTRFSSubvol(t)
	svc, rootName, _ := startRealBTRFSDetector(t, subvol)

	target := filepath.Join(subvol, "chmod.txt")
	if err := os.WriteFile(target, []byte("data"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	waitForResolveWithStimulus(t, 15*time.Second, 150*time.Millisecond, func() {
		_ = os.WriteFile(target, []byte("data"), 0o644)
	}, func() bool {
		id, err := svc.ResolvePath(rootName + "/chmod.txt")
		if err != nil {
			return false
		}
		meta, err := svc.GetFile(id)
		return err == nil && meta.Mode == 0o644
	})

	if err := os.Chmod(target, 0o600); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	waitUntil(t, 15*time.Second, func() bool {
		id, err := svc.ResolvePath(rootName + "/chmod.txt")
		if err != nil {
			return false
		}
		meta, err := svc.GetFile(id)
		return err == nil && meta.Mode == 0o600
	})
}

// TestBTRFSRealTouchMtimeOnly verifies that a `touch` (mtime bump, no
// content change) is detected and the index mtime advances. find-new is
// generation-based, and utimensat does bump the inode generation on btrfs.
func TestBTRFSRealTouchMtimeOnly(t *testing.T) {
	subvol := setupRealBTRFSSubvol(t)
	svc, rootName, _ := startRealBTRFSDetector(t, subvol)

	target := filepath.Join(subvol, "touch.txt")
	if err := os.WriteFile(target, []byte("static"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	waitForResolveWithStimulus(t, 15*time.Second, 150*time.Millisecond, func() {
		_ = os.WriteFile(target, []byte("static"), 0o644)
	}, func() bool {
		_, err := svc.ResolvePath(rootName + "/touch.txt")
		return err == nil
	})

	id, err := svc.ResolvePath(rootName + "/touch.txt")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	originalMeta, err := svc.GetFile(id)
	if err != nil {
		t.Fatalf("get: %v", err)
	}

	// Sleep just long enough that the mtime can advance past the
	// filesystem's mtime granularity (millisecond on btrfs/ext4). This is a
	// genuine mtime-resolution wait, not a goroutine sync.
	time.Sleep(15 * time.Millisecond)
	now := time.Now()
	if err := os.Chtimes(target, now, now); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	waitUntil(t, 15*time.Second, func() bool {
		meta, err := svc.GetFile(id)
		return err == nil && meta.Mtime > originalMeta.Mtime
	})
}

// TestBTRFSRealFilenameEdgeCases verifies that names with spaces, unicode,
// and a leading dot/dash all index correctly. Newlines in filenames are
// not exercised here because btrfs find-new output is line-oriented and
// newlines would be a known-broken parsing case.
func TestBTRFSRealFilenameEdgeCases(t *testing.T) {
	subvol := setupRealBTRFSSubvol(t)
	svc, rootName, _ := startRealBTRFSDetector(t, subvol)

	names := []string{
		"with spaces.txt",
		"datei mit umlauts äöü.txt",
		".leading-dot",
		"-leading-dash",
		"emoji-😀.txt",
	}
	for _, name := range names {
		if err := os.WriteFile(filepath.Join(subvol, name), []byte(name), 0o644); err != nil {
			t.Fatalf("seed %q: %v", name, err)
		}
	}
	// Stimulate using the first name to walk past the init race.
	waitForResolveWithStimulus(t, 15*time.Second, 150*time.Millisecond, func() {
		_ = os.WriteFile(filepath.Join(subvol, names[0]), []byte(names[0]), 0o644)
	}, func() bool {
		_, err := svc.ResolvePath(rootName + "/" + names[0])
		return err == nil
	})

	for _, name := range names {
		if _, err := svc.ResolvePath(rootName + "/" + name); err != nil {
			t.Fatalf("name %q didn't resolve: %v", name, err)
		}
	}
}

// TestBTRFSRealRenameOneHardLinkAlias verifies that renaming one alias of
// a hard-linked pair leaves the surviving primary intact and that the
// renamed name becomes resolvable after a rescan. As with
// UnlinkOneOfHardLinks, the old alias name's stale child entry is a
// known limitation — Filegate has no proactive child-by-entity-id sweep,
// so renaming a hard-link's directory entry does not auto-clean the old
// name. We seed the new name with an explicit Rescan (link(2)/rename
// don't bump inode generation) and assert the contract we DO promise:
// primary works, renamed works.
func TestBTRFSRealRenameOneHardLinkAlias(t *testing.T) {
	subvol := setupRealBTRFSSubvol(t)
	svc, rootName, _ := startRealBTRFSDetector(t, subvol)

	primary := filepath.Join(subvol, "hl-stable.txt")
	if err := os.WriteFile(primary, []byte("shared"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	waitForResolveWithStimulus(t, 15*time.Second, 150*time.Millisecond, func() {
		_ = os.WriteFile(primary, []byte("shared"), 0o644)
	}, func() bool {
		_, err := svc.ResolvePath(rootName + "/hl-stable.txt")
		return err == nil
	})

	alias := filepath.Join(subvol, "hl-rename-me.txt")
	if err := os.Link(primary, alias); err != nil {
		t.Fatalf("link: %v", err)
	}
	if err := svc.Rescan(); err != nil {
		t.Fatalf("rescan after link: %v", err)
	}

	renamed := filepath.Join(subvol, "hl-renamed.txt")
	if err := os.Rename(alias, renamed); err != nil {
		t.Fatalf("rename alias: %v", err)
	}
	// Force-walk the FS so the new dir entry is indexed. Ambient detector
	// events on hard-link rename within a subvol are not guaranteed.
	if err := svc.Rescan(); err != nil {
		t.Fatalf("rescan after rename: %v", err)
	}

	if _, err := svc.ResolvePath(rootName + "/hl-stable.txt"); err != nil {
		t.Fatalf("primary lost when alias was renamed: %v", err)
	}
	if _, err := svc.ResolvePath(rootName + "/hl-renamed.txt"); err != nil {
		t.Fatalf("renamed alias should resolve after rescan: %v", err)
	}
}

// TestBTRFSRealSymlinkTargetReplacement exercises delete+recreate of a
// symlink with a different target. Filegate skips symlinks, so neither
// the original nor the replacement should appear in the index, and the
// real files referenced should remain unaffected.
func TestBTRFSRealSymlinkTargetReplacement(t *testing.T) {
	subvol := setupRealBTRFSSubvol(t)
	svc, rootName, _ := startRealBTRFSDetector(t, subvol)

	a := filepath.Join(subvol, "target-a.txt")
	b := filepath.Join(subvol, "target-b.txt")
	if err := os.WriteFile(a, []byte("a"), 0o644); err != nil {
		t.Fatalf("seed a: %v", err)
	}
	if err := os.WriteFile(b, []byte("b"), 0o644); err != nil {
		t.Fatalf("seed b: %v", err)
	}
	waitForResolveWithStimulus(t, 15*time.Second, 150*time.Millisecond, func() {
		_ = os.WriteFile(a, []byte("a"), 0o644)
	}, func() bool {
		_, e1 := svc.ResolvePath(rootName + "/target-a.txt")
		_, e2 := svc.ResolvePath(rootName + "/target-b.txt")
		return e1 == nil && e2 == nil
	})

	link := filepath.Join(subvol, "switching-link")
	if err := os.Symlink(a, link); err != nil {
		t.Fatalf("symlink a: %v", err)
	}
	if err := os.Remove(link); err != nil {
		t.Fatalf("remove link: %v", err)
	}
	if err := os.Symlink(b, link); err != nil {
		t.Fatalf("symlink b: %v", err)
	}

	// Real files must remain — symlink churn must not poison them.
	if _, err := svc.ResolvePath(rootName + "/target-a.txt"); err != nil {
		t.Fatalf("target-a lost during symlink replacement: %v", err)
	}
	if _, err := svc.ResolvePath(rootName + "/target-b.txt"); err != nil {
		t.Fatalf("target-b lost during symlink replacement: %v", err)
	}
}

// TestBTRFSRealOpenWriteUnlinkWithFDOpen exercises the
// open-write-unlink-while-fd-still-held idiom (Linux semantics: the file
// data lives until the last fd closes, but the namespace entry is gone).
// The detector should index the unlink as a delete; an open fd elsewhere
// must not keep a stale entry alive.
func TestBTRFSRealOpenWriteUnlinkWithFDOpen(t *testing.T) {
	subvol := setupRealBTRFSSubvol(t)
	svc, rootName, _ := startRealBTRFSDetector(t, subvol)

	target := filepath.Join(subvol, "fd-pinned.txt")
	if err := os.WriteFile(target, []byte("alive"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	waitForResolveWithStimulus(t, 15*time.Second, 150*time.Millisecond, func() {
		_ = os.WriteFile(target, []byte("alive"), 0o644)
	}, func() bool {
		_, err := svc.ResolvePath(rootName + "/fd-pinned.txt")
		return err == nil
	})

	// Open a long-lived fd; cancel via context so the deferred close runs
	// even if the test panics.
	_, cancel := context.WithCancel(context.Background())
	defer cancel()
	rawFD, err := os.Open(target)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = rawFD.Close() }()
	// Do something with the fd so the compiler doesn't optimize the open
	// away (and to confirm it's actually usable).
	if _, err := io.Copy(io.Discard, rawFD); err != nil {
		t.Fatalf("read fd: %v", err)
	}

	if err := os.Remove(target); err != nil {
		t.Fatalf("unlink while fd open: %v", err)
	}

	waitUntil(t, 15*time.Second, func() bool {
		_, err := svc.ResolvePath(rootName + "/fd-pinned.txt")
		return err == domain.ErrNotFound
	})
}
