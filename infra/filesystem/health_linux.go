//go:build linux

package filesystem

import "golang.org/x/sys/unix"

// fillFreeSpace populates h.FreeBytes + h.TotalBytes via statfs.
// Best-effort: a statfs failure is not propagated to h.Errors —
// the operator already knows the path exists (we got here past
// the stat probe), and free-space reporting is informational.
func fillFreeSpace(h *MountHealth, path string) {
	var s unix.Statfs_t
	if err := unix.Statfs(path, &s); err != nil {
		return
	}
	h.FreeBytes = uint64(s.Bavail) * uint64(s.Bsize)
	h.TotalBytes = uint64(s.Blocks) * uint64(s.Bsize)
}
