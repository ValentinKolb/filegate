package filesystem

import (
	"os"
	"path/filepath"
	"syscall"
)

// FreeBytes returns the bytes available to non-privileged users on
// the filesystem containing path. Used by upload-staging to refuse
// new uploads that would exhaust the filesystem.
func FreeBytes(path string) (uint64, error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return 0, err
	}
	return stat.Bavail * uint64(stat.Bsize), nil
}

// WriteFileAtomic writes payload to path via the standard tmp+fsync
// +rename pattern. The destination either reflects the new payload
// fully or is unchanged — there is no partial-write window visible
// to other readers. Used for small JSON manifests and similar config
// blobs where the caller already has the bytes in memory.
//
// For large or streamed payloads, the byte-streaming variant inside
// the domain package (Service.writeFileAtomic) is the right tool —
// it tees through MD5 during the copy and handles ownership/xattr
// metadata. This helper is the no-frills atomic-write building
// block for callers that don't need any of that.
func WriteFileAtomic(path string, payload []byte) error {
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.Write(payload); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return SyncDir(filepath.Dir(path))
}

// SyncDir fsyncs a directory. Used after a rename so the directory
// entry change reaches durable storage — without this, a crash
// after a successful rename(2) can leave the new entry invisible
// on remount.
func SyncDir(path string) error {
	d, err := os.Open(path)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
