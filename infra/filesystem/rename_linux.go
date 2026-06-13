//go:build linux

package filesystem

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

// renameNoReplace renames oldPath to newPath and fails when newPath
// already exists. renameat2(RENAME_NOREPLACE) makes the existence check
// and the rename one atomic syscall — there is no window in which a
// concurrently-created file at newPath would be clobbered.
//
// EEXIST (and ENOTEMPTY, which some filesystems return for occupied
// directory targets) are normalized to wrap os.ErrExist so callers can
// errors.Is against a stable sentinel.
func renameNoReplace(oldPath, newPath string) error {
	err := unix.Renameat2(unix.AT_FDCWD, oldPath, unix.AT_FDCWD, newPath, unix.RENAME_NOREPLACE)
	if err == nil {
		return nil
	}
	if errors.Is(err, unix.EEXIST) || errors.Is(err, unix.ENOTEMPTY) {
		return fmt.Errorf("rename %s -> %s: %w", oldPath, newPath, os.ErrExist)
	}
	return &os.LinkError{Op: "renameat2", Old: oldPath, New: newPath, Err: err}
}
