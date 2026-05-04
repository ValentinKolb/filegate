//go:build linux

package filesystem

import (
	"os"
	"path/filepath"
	"testing"
)

// CloneFile is exercised here against tmpfs/ext4 so we always hit the
// fallback path. The reflink-success path is exercised end-to-end in
// the real-btrfs CI job (cli/serve_detector_btrfs_real_*). Locally we
// only need to verify the fallback copies bytes correctly and refuses
// to overwrite.

func TestCloneFileFallbackCopiesBytes(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.bin")
	dst := filepath.Join(dir, "dst.bin")
	payload := []byte("hello reflink fallback world\n")
	if err := os.WriteFile(src, payload, 0o644); err != nil {
		t.Fatalf("seed src: %v", err)
	}

	used, err := CloneFile(src, dst)
	if err != nil {
		t.Fatalf("CloneFile: %v", err)
	}
	// On btrfs CI this would be true; on tmpfs/ext4 it falls back.
	// We don't assert which — just that bytes match.
	_ = used

	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("read dst: %v", err)
	}
	if string(got) != string(payload) {
		t.Fatalf("bytes mismatch: got %q, want %q", got, payload)
	}
}

func TestCloneFileRefusesToOverwrite(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.bin")
	dst := filepath.Join(dir, "dst.bin")
	if err := os.WriteFile(src, []byte("payload"), 0o644); err != nil {
		t.Fatalf("seed src: %v", err)
	}
	if err := os.WriteFile(dst, []byte("existing"), 0o644); err != nil {
		t.Fatalf("seed dst: %v", err)
	}

	if _, err := CloneFile(src, dst); err == nil {
		t.Fatalf("expected error when dst already exists")
	}
	// Existing dst untouched.
	got, _ := os.ReadFile(dst)
	if string(got) != "existing" {
		t.Fatalf("dst was clobbered: %q", got)
	}
}

func TestCloneFileSrcMissingErrors(t *testing.T) {
	dir := t.TempDir()
	if _, err := CloneFile(filepath.Join(dir, "nope"), filepath.Join(dir, "dst")); err == nil {
		t.Fatalf("expected error for missing src")
	}
}
