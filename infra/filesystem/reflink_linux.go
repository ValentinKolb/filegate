//go:build linux

package filesystem

import (
	"errors"
	"fmt"
	"io"
	"os"

	"golang.org/x/sys/unix"
)

// CloneFile copies srcPath into dstPath using a copy-on-write reflink
// (FICLONE ioctl) when the underlying filesystem supports it (btrfs, xfs
// with reflink=on, etc.). Falls back to a regular byte-for-byte copy
// when the filesystem rejects the ioctl with EOPNOTSUPP/EXDEV/EINVAL —
// the common cases on ext4 and across mount boundaries.
//
// dstPath must not exist; the function refuses to overwrite. Caller is
// responsible for placing dstPath in a directory that already exists.
//
// Returns true when the copy used reflink (constant-time, no extra
// storage), false when it fell back to a byte copy.
func CloneFile(srcPath, dstPath string) (bool, error) {
	src, err := os.Open(srcPath)
	if err != nil {
		return false, err
	}
	defer src.Close()

	// O_EXCL: never overwrite. Caller-side mistake should fail, not
	// silently clobber a version slot.
	dst, err := os.OpenFile(dstPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return false, err
	}
	closed := false
	defer func() {
		if !closed {
			_ = dst.Close()
		}
	}()

	if err := unix.IoctlFileClone(int(dst.Fd()), int(src.Fd())); err == nil {
		closed = true
		if cerr := dst.Close(); cerr != nil {
			return true, cerr
		}
		return true, nil
	} else if !isReflinkUnsupported(err) {
		// A "real" error (EIO, EACCES, …) — don't silently fall back to
		// copy. The caller needs to know capture failed.
		_ = os.Remove(dstPath)
		return false, fmt.Errorf("ficlone %s -> %s: %w", srcPath, dstPath, err)
	}

	// Reflink not supported here — full byte copy. Truncate first
	// because IoctlFileClone may have partially written before
	// returning the unsupported error on some kernels.
	if _, err := dst.Seek(0, io.SeekStart); err != nil {
		_ = os.Remove(dstPath)
		return false, err
	}
	if err := dst.Truncate(0); err != nil {
		_ = os.Remove(dstPath)
		return false, err
	}
	if _, err := io.Copy(dst, src); err != nil {
		_ = os.Remove(dstPath)
		return false, err
	}
	closed = true
	if cerr := dst.Close(); cerr != nil {
		return false, cerr
	}
	return false, nil
}

// isReflinkUnsupported recognises the error codes filesystems return when
// they don't support FICLONE between the given inodes. EOPNOTSUPP is the
// canonical signal; EXDEV fires across different filesystems; EINVAL is
// what some older kernels return on non-reflink-capable backings.
func isReflinkUnsupported(err error) bool {
	return errors.Is(err, unix.EOPNOTSUPP) ||
		errors.Is(err, unix.ENOTSUP) ||
		errors.Is(err, unix.EXDEV) ||
		errors.Is(err, unix.EINVAL)
}
