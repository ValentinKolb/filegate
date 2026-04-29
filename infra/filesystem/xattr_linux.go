//go:build linux

package filesystem

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/unix"

	"github.com/valentinkolb/filegate/domain"
)

func setID(path string, id domain.FileID) error {
	if err := unix.Setxattr(path, domain.XAttrIDKey(), id[:], 0); err != nil {
		return fmt.Errorf("setxattr %s: %w", path, err)
	}
	return nil
}

func getID(path string) (domain.FileID, error) {
	var id domain.FileID
	buf := make([]byte, 16)
	n, err := unix.Getxattr(path, domain.XAttrIDKey(), buf)
	if err != nil {
		if errors.Is(err, unix.ENODATA) || errors.Is(err, unix.ENOTSUP) {
			return id, os.ErrNotExist
		}
		if errors.Is(err, unix.ENOENT) {
			return id, os.ErrNotExist
		}
		// ERANGE means the xattr exists but doesn't fit in our 16-byte
		// buffer (i.e. an admin or backup tool wrote a non-UUID value
		// over user.filegate.id). Treat it as missing so syncSingle
		// reissues a fresh ID and clobbers the malformed payload.
		// Without this the entire sync fails and the path is never
		// re-indexed.
		if errors.Is(err, unix.ERANGE) {
			return id, os.ErrNotExist
		}
		return id, fmt.Errorf("getxattr %s: %w", path, err)
	}
	if n != 16 {
		return id, os.ErrNotExist
	}
	copy(id[:], buf[:16])
	return id, nil
}
