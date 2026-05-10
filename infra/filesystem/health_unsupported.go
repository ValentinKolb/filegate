//go:build !linux

package filesystem

// fillFreeSpace is a no-op on non-Linux platforms. Filegate is
// Linux-only in production; this stub exists so darwin/test
// builds compile without pulling in unix-statfs.
func fillFreeSpace(_ *MountHealth, _ string) {}
