//go:build linux

package filesystem

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestRenameNoReplace(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.txt")
	dst := filepath.Join(dir, "dst.txt")
	if err := os.WriteFile(src, []byte("payload"), 0o644); err != nil {
		t.Fatalf("write src: %v", err)
	}

	// Free target: plain atomic rename.
	if err := renameNoReplace(src, dst); err != nil {
		t.Fatalf("rename to free target: %v", err)
	}
	data, err := os.ReadFile(dst)
	if err != nil || string(data) != "payload" {
		t.Fatalf("dst content=%q err=%v", data, err)
	}

	// Occupied target: must fail with os.ErrExist and leave both
	// files untouched — this is the no-clobber guarantee UpdateNode
	// and Transfer rely on.
	src2 := filepath.Join(dir, "src2.txt")
	if err := os.WriteFile(src2, []byte("new"), 0o644); err != nil {
		t.Fatalf("write src2: %v", err)
	}
	err = renameNoReplace(src2, dst)
	if !errors.Is(err, os.ErrExist) {
		t.Fatalf("rename onto occupied target err=%v, want os.ErrExist", err)
	}
	data, err = os.ReadFile(dst)
	if err != nil || string(data) != "payload" {
		t.Fatalf("dst clobbered: content=%q err=%v", data, err)
	}
	if _, err := os.Stat(src2); err != nil {
		t.Fatalf("src2 vanished after failed rename: %v", err)
	}

	// Occupied directory target.
	srcDir := filepath.Join(dir, "srcdir")
	dstDir := filepath.Join(dir, "dstdir")
	if err := os.MkdirAll(filepath.Join(dstDir, "occupied"), 0o755); err != nil {
		t.Fatalf("mkdir dst dir: %v", err)
	}
	if err := os.Mkdir(srcDir, 0o755); err != nil {
		t.Fatalf("mkdir src dir: %v", err)
	}
	if err := renameNoReplace(srcDir, dstDir); !errors.Is(err, os.ErrExist) {
		t.Fatalf("rename onto occupied dir err=%v, want os.ErrExist", err)
	}
}

// TestRenameNoReplaceFallsBackWithoutKernelSupport pins the degradation
// path for filesystems without RENAME_NOREPLACE (NFS, some FUSE): the
// rename must still work via check-then-rename instead of failing every
// rename outright, and an occupied target still answers os.ErrExist.
func TestRenameNoReplaceFallsBackWithoutKernelSupport(t *testing.T) {
	prev := renameat2NoReplace
	t.Cleanup(func() { renameat2NoReplace = prev })
	renameat2NoReplace = func(_, _ string) error { return syscall.EINVAL }

	dir := t.TempDir()
	src := filepath.Join(dir, "src.txt")
	dst := filepath.Join(dir, "dst.txt")
	if err := os.WriteFile(src, []byte("payload"), 0o644); err != nil {
		t.Fatalf("write src: %v", err)
	}

	if err := renameNoReplace(src, dst); err != nil {
		t.Fatalf("fallback rename to free target: %v", err)
	}
	data, err := os.ReadFile(dst)
	if err != nil || string(data) != "payload" {
		t.Fatalf("dst content=%q err=%v", data, err)
	}

	src2 := filepath.Join(dir, "src2.txt")
	if err := os.WriteFile(src2, []byte("new"), 0o644); err != nil {
		t.Fatalf("write src2: %v", err)
	}
	if err := renameNoReplace(src2, dst); !errors.Is(err, os.ErrExist) {
		t.Fatalf("fallback rename onto occupied target err=%v, want os.ErrExist", err)
	}
	data, err = os.ReadFile(dst)
	if err != nil || string(data) != "payload" {
		t.Fatalf("dst clobbered via fallback: content=%q err=%v", data, err)
	}
}
