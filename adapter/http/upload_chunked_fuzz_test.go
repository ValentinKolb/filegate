package httpadapter

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"github.com/valentinkolb/filegate/domain"
)

func FuzzChunkExpectedSize(f *testing.F) {
	f.Add(int64(10), int64(4), int(0))
	f.Add(int64(10), int64(4), int(2))
	f.Add(int64(10), int64(4), int(3))

	f.Fuzz(func(t *testing.T, size int64, chunkSize int64, idx int) {
		meta := &chunkedUploadMeta{
			Size:        size,
			ChunkSize:   chunkSize,
			TotalChunks: 0,
		}
		if chunkSize > 0 && size > 0 {
			meta.TotalChunks = int((size + chunkSize - 1) / chunkSize)
		}
		got, err := chunkExpectedSize(meta, idx)
		if err == nil && got <= 0 {
			t.Fatalf("expected positive chunk size, got=%d", got)
		}
	})
}

func FuzzHashChunkFromReader(f *testing.F) {
	f.Add([]byte("hello"), int64(5), int64(10))
	f.Add([]byte("a"), int64(1), int64(1))
	f.Add([]byte{}, int64(0), int64(1))

	f.Fuzz(func(t *testing.T, data []byte, expectedSize int64, maxChunkBytes int64) {
		if len(data) > 1<<16 {
			t.Skip()
		}
		if expectedSize < 0 {
			expectedSize = -expectedSize
		}
		if maxChunkBytes < 0 {
			maxChunkBytes = -maxChunkBytes
		}
		if maxChunkBytes > 1<<20 {
			maxChunkBytes = 1 << 20
		}
		_, _ = hashChunkFromReader(expectedSize, maxChunkBytes, bytes.NewReader(data))
	})
}

func FuzzWriteChunkAtPath(f *testing.F) {
	f.Add([]byte("abc"), int64(0), int64(3), int64(16))
	f.Add([]byte(""), int64(0), int64(0), int64(16))
	f.Add([]byte("123456"), int64(2), int64(3), int64(16))

	f.Fuzz(func(t *testing.T, data []byte, offset int64, expectedSize int64, maxChunkBytes int64) {
		if len(data) > 1<<16 {
			t.Skip()
		}
		if offset < 0 {
			offset = -offset
		}
		if expectedSize < 0 {
			expectedSize = -expectedSize
		}
		if maxChunkBytes < 0 {
			maxChunkBytes = -maxChunkBytes
		}
		if maxChunkBytes == 0 {
			maxChunkBytes = 1
		}
		if maxChunkBytes > 1<<20 {
			maxChunkBytes = 1 << 20
		}
		if expectedSize > maxChunkBytes {
			expectedSize = maxChunkBytes
		}
		if offset > 1<<20 {
			offset = 1 << 20
		}

		partPath := filepath.Join(t.TempDir(), "data.part")
		// Preallocate enough space for valid writes.
		prealloc := offset + expectedSize + 1
		if prealloc < 1 {
			prealloc = 1
		}
		fh, err := os.OpenFile(partPath, os.O_CREATE|os.O_RDWR, 0o644)
		if err != nil {
			t.Fatalf("open part file: %v", err)
		}
		if err := fh.Truncate(prealloc); err != nil {
			_ = fh.Close()
			t.Fatalf("truncate: %v", err)
		}
		_ = fh.Close()

		sum, err := writeChunkAtPath(partPath, offset, expectedSize, maxChunkBytes, bytes.NewReader(data))
		if err != nil {
			return
		}

		if int64(len(data)) != expectedSize {
			t.Fatalf("write succeeded with unexpected size: len(data)=%d expected=%d", len(data), expectedSize)
		}
		wantHash := sha256.Sum256(data)
		want := "sha256:" + hex.EncodeToString(wantHash[:])
		if sum != want {
			t.Fatalf("checksum mismatch got=%s want=%s", sum, want)
		}
		raw, err := os.ReadFile(partPath)
		if err != nil {
			t.Fatalf("read back: %v", err)
		}
		start := int(offset)
		end := int(offset + expectedSize)
		if end > len(raw) || start < 0 || start > end {
			t.Fatalf("written region out of file bounds")
		}
		if !bytes.Equal(raw[start:end], data) {
			t.Fatalf("written bytes mismatch")
		}
	})
}

func FuzzDeterministicUploadID(f *testing.F) {
	f.Add("file.txt", "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	f.Fuzz(func(t *testing.T, filename, checksum string) {
		id := deterministicUploadID(domain.FileID{}, filename, checksum)
		if len(id) != 16 {
			t.Fatalf("unexpected upload id length: %d", len(id))
		}
	})
}
