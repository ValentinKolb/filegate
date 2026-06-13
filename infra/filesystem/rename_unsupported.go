//go:build !linux

package filesystem

import "fmt"

// renameNoReplace requires renameat2(RENAME_NOREPLACE), which only
// exists on Linux. Filegate's serve path is Linux-only; this stub keeps
// darwin/test builds compiling, mirroring xattr_unsupported.go.
func renameNoReplace(_, _ string) error {
	return fmt.Errorf("atomic no-replace rename only supported on linux builds")
}
