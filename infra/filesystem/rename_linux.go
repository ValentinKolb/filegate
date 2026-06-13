//go:build linux

package filesystem

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

// renameat2NoReplace is the raw syscall, stubbed in tests to simulate
// filesystems without RENAME_NOREPLACE support.
var renameat2NoReplace = func(oldPath, newPath string) error {
	return unix.Renameat2(unix.AT_FDCWD, oldPath, unix.AT_FDCWD, newPath, unix.RENAME_NOREPLACE)
}

// renameNoReplace renames oldPath to newPath and fails when newPath
// already exists. renameat2(RENAME_NOREPLACE) makes the existence check
// and the rename one atomic syscall — there is no window in which a
// concurrently-created file at newPath would be clobbered.
//
// EEXIST (and ENOTEMPTY, which some filesystems return for occupied
// directory targets) are normalized to wrap os.ErrExist so callers can
// errors.Is against a stable sentinel.
//
// Filesystems and kernels without RENAME_NOREPLACE (NFS, some FUSE)
// answer EINVAL/ENOSYS/ENOTSUP; for those we degrade to
// check-then-rename — racy, but identical to the pre-RENAME_NOREPLACE
// behavior on such mounts. Breaking every rename there would be worse.
func renameNoReplace(oldPath, newPath string) error {
	err := renameat2NoReplace(oldPath, newPath)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, unix.EEXIST), errors.Is(err, unix.ENOTEMPTY):
		return fmt.Errorf("rename %s -> %s: %w", oldPath, newPath, os.ErrExist)
	case errors.Is(err, unix.EINVAL), errors.Is(err, unix.ENOSYS), errors.Is(err, unix.ENOTSUP):
		if _, statErr := os.Lstat(newPath); statErr == nil {
			return fmt.Errorf("rename %s -> %s: %w", oldPath, newPath, os.ErrExist)
		} else if !errors.Is(statErr, os.ErrNotExist) {
			return statErr
		}
		return os.Rename(oldPath, newPath)
	default:
		return &os.LinkError{Op: "renameat2", Old: oldPath, New: newPath, Err: err}
	}
}
