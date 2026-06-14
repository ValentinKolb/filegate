// Package segments contains pure helpers for resumable uploads.
//
// These helpers do not depend on the SDK client, an HTTP token, or a network
// connection. They are safe to use anywhere upload segment math or Filegate's
// canonical sha256:<hex> checksum format is needed.
package segments

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
)

type Segment struct {
	Index  int
	Offset int64
	Size   int64
}

func Count(size, segmentSize int64) int {
	if size <= 0 || segmentSize <= 0 {
		return 0
	}
	n := size / segmentSize
	if size%segmentSize != 0 {
		n++
	}
	return int(n)
}

func Bounds(index int, size, segmentSize int64) (start, end int64, err error) {
	if index < 0 {
		return 0, 0, errors.New("index must be >= 0")
	}
	total := Count(size, segmentSize)
	if index >= total {
		return 0, 0, errors.New("index out of range")
	}
	start = int64(index) * segmentSize
	end = start + segmentSize
	if end > size {
		end = size
	}
	return start, end, nil
}

func Plan(size, segmentSize int64) ([]Segment, error) {
	total := Count(size, segmentSize)
	out := make([]Segment, 0, total)
	for i := 0; i < total; i++ {
		start, end, err := Bounds(i, size, segmentSize)
		if err != nil {
			return nil, err
		}
		out = append(out, Segment{Index: i, Offset: start, Size: end - start})
	}
	return out, nil
}

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

func SHA256Bytes(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}
