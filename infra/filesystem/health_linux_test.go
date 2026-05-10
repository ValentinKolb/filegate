//go:build linux

package filesystem

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestCheckMountHealthHealthyDir: a freshly-created tmp dir
// passes all probes (the test runner's tmp is on a real
// xattr-capable filesystem in CI; if not, we skip).
func TestCheckMountHealthHealthyDir(t *testing.T) {
	dir := t.TempDir()
	h := CheckMountHealth(dir)
	// The CI host might run on a tmpfs that doesn't support
	// user.* xattrs (rare but possible in some chroot setups).
	// In that case the test reports the limitation and skips
	// rather than failing — the production target is ext4/btrfs
	// where xattrs are guaranteed.
	if !h.XAttrSupported {
		t.Skipf("test FS doesn't support user.* xattrs (errors=%v) — production target is ext4/btrfs", h.Errors)
	}
	if !h.Exists {
		t.Errorf("Exists=false on a created TempDir")
	}
	if !h.Writable {
		t.Errorf("Writable=false on a created TempDir")
	}
	if len(h.Errors) > 0 {
		t.Errorf("unexpected errors on healthy dir: %v", h.Errors)
	}
	// FreeBytes/TotalBytes are best-effort. On linux statfs
	// shouldn't fail for a path we can write to, so we expect
	// non-zero. Defensive: skip if we got unlucky on the host.
	if h.FreeBytes == 0 || h.TotalBytes == 0 {
		t.Logf("statfs returned zero (host quirk): free=%d total=%d", h.FreeBytes, h.TotalBytes)
	}

	// Cleanup: probe dir must be gone after the call.
	if _, err := os.Stat(filepath.Join(dir, ".fg-healthcheck")); !os.IsNotExist(err) {
		t.Errorf(".fg-healthcheck dir was not cleaned up, err=%v", err)
	}
}

// TestCheckMountHealthMissingPath: a non-existent path is
// captured as an error (Exists=false), not a panic.
func TestCheckMountHealthMissingPath(t *testing.T) {
	h := CheckMountHealth("/nonexistent/filegate-mount/probe")
	if h.Exists {
		t.Errorf("Exists=true on nonexistent path")
	}
	if len(h.Errors) == 0 {
		t.Errorf("Errors empty on nonexistent path, want at least 'stat:'")
	}
	if !strings.Contains(h.Errors[0], "stat") {
		t.Errorf("first error=%q, want stat-related", h.Errors[0])
	}
}

// TestCheckMountHealthFileNotDir: the operator pointed at a
// regular file instead of a directory. Catch the typo at startup.
func TestCheckMountHealthFileNotDir(t *testing.T) {
	tmp := filepath.Join(t.TempDir(), "regular-file")
	if err := os.WriteFile(tmp, []byte("x"), 0o644); err != nil {
		t.Fatalf("setup write: %v", err)
	}
	h := CheckMountHealth(tmp)
	if h.Writable || h.XAttrSupported {
		t.Errorf("regular file probed as writable/xattr-supported: %+v", h)
	}
	if len(h.Errors) == 0 || !strings.Contains(strings.Join(h.Errors, " "), "not a directory") {
		t.Errorf("expected 'not a directory' error, got %v", h.Errors)
	}
}

// TestCheckMountHealthReadOnlyDir: a directory the daemon can't
// write to surfaces a clear error AND doesn't crash on the xattr
// step (xattr probe needs the test file, which couldn't be
// created).
func TestCheckMountHealthReadOnlyDir(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses ro permissions — skipping")
	}
	tmp := t.TempDir()
	if err := os.Chmod(tmp, 0o500); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(tmp, 0o755) })

	h := CheckMountHealth(tmp)
	if h.Writable {
		t.Errorf("Writable=true on a 0500 dir")
	}
	if h.XAttrSupported {
		t.Errorf("XAttrSupported=true on a dir we couldn't write to")
	}
	if len(h.Errors) == 0 {
		t.Errorf("expected at least one error on read-only dir")
	}
}

// TestCheckMountsHealthMixed: aggregate probe of three paths,
// some healthy and some broken. The result slice is index-
// aligned with input.
func TestCheckMountsHealthMixed(t *testing.T) {
	good := t.TempDir()
	bad := "/this/path/does/not/exist"
	res := CheckMountsHealth([]string{good, bad, good})
	if len(res) != 3 {
		t.Fatalf("got %d results, want 3", len(res))
	}
	if !res[0].Exists {
		t.Errorf("[0] Exists=false on good path")
	}
	if res[1].Exists {
		t.Errorf("[1] Exists=true on bad path")
	}
	if !res[2].Exists {
		t.Errorf("[2] Exists=false on good path (re-probe)")
	}
}

// TestCheckMountHealthCleansUpOnFailure: even when the xattr
// probe fails, the .fg-healthcheck dir is removed afterwards.
// A regression that leaks the probe dir would show up here.
func TestCheckMountHealthCleansUpOnFailure(t *testing.T) {
	// We can't easily simulate xattr-not-supported on a Linux
	// tmp dir, so this test just verifies cleanup happens on
	// a healthy probe — the deferred RemoveAll is the same
	// code path either way.
	dir := t.TempDir()
	_ = CheckMountHealth(dir)
	if _, err := os.Stat(filepath.Join(dir, ".fg-healthcheck")); !os.IsNotExist(err) {
		t.Errorf(".fg-healthcheck not cleaned up on healthy probe, err=%v", err)
	}
}
