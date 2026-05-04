//go:build !linux

package filesystem

import (
	"io"
	"os"
)

// CloneFile on non-Linux platforms always falls back to a byte copy.
// Returns false (not a reflink) and any I/O error. Filegate is
// Linux-first; this stub exists so the package compiles cross-platform
// for tooling.
func CloneFile(srcPath, dstPath string) (bool, error) {
	src, err := os.Open(srcPath)
	if err != nil {
		return false, err
	}
	defer src.Close()
	dst, err := os.OpenFile(dstPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return false, err
	}
	if _, err := io.Copy(dst, src); err != nil {
		_ = dst.Close()
		_ = os.Remove(dstPath)
		return false, err
	}
	if err := dst.Close(); err != nil {
		return false, err
	}
	return false, nil
}
