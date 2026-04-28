// Package chunks contains pure helpers for chunked uploads against Filegate.
//
// These helpers do not depend on the SDK client, an HTTP token, or a
// network connection — they are safe to use anywhere chunk math or the
// canonical sha256:<hex> checksum format is needed (e.g. CLI tools, build
// pipelines, or environments that talk to Filegate via a proxy).
package chunks

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
)

// TotalChunks returns the number of chunks needed to cover size bytes when
// each chunk holds at most chunkSize bytes. Returns 0 for non-positive
// inputs.
func TotalChunks(size, chunkSize int64) int {
	if size <= 0 || chunkSize <= 0 {
		return 0
	}
	n := size / chunkSize
	if size%chunkSize != 0 {
		n++
	}
	return int(n)
}

// Bounds returns the [start, end) byte offsets for the chunk at index. The
// last chunk's end is clamped to size.
func Bounds(index int, size, chunkSize int64) (start, end int64, err error) {
	if index < 0 {
		return 0, 0, errors.New("index must be >= 0")
	}
	total := TotalChunks(size, chunkSize)
	if index >= total {
		return 0, 0, errors.New("index out of range")
	}
	start = int64(index) * chunkSize
	end = start + chunkSize
	if end > size {
		end = size
	}
	return start, end, nil
}

// SHA256Reader streams r and returns its sha256 digest in Filegate's
// `sha256:<hex>` checksum format.
func SHA256Reader(r io.Reader) (string, error) {
	if r == nil {
		return "", fmt.Errorf("reader is required")
	}
	h := sha256.New()
	if _, err := io.Copy(h, r); err != nil {
		return "", err
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), nil
}

// SHA256Bytes returns the sha256 digest of data in Filegate's
// `sha256:<hex>` checksum format.
func SHA256Bytes(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}
