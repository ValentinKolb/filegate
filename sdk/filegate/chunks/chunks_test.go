package chunks

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestTotalChunks(t *testing.T) {
	cases := []struct {
		size, chunkSize int64
		want            int
	}{
		{0, 4, 0},
		{4, 0, 0},
		{-1, 4, 0},
		{4, 4, 1},
		{5, 4, 2},
		{8, 4, 2},
		{1, 4, 1},
	}
	for _, c := range cases {
		if got := TotalChunks(c.size, c.chunkSize); got != c.want {
			t.Errorf("TotalChunks(%d, %d) = %d, want %d", c.size, c.chunkSize, got, c.want)
		}
	}
}

func TestBoundsCoversWholeFile(t *testing.T) {
	const size, chunkSize = int64(10), int64(4)
	got := make([]byte, 0, size)
	want := make([]byte, size)
	for i := range want {
		want[i] = byte(i)
	}
	src := bytes.Clone(want)

	for i := 0; i < TotalChunks(size, chunkSize); i++ {
		start, end, err := Bounds(i, size, chunkSize)
		if err != nil {
			t.Fatalf("Bounds(%d): %v", i, err)
		}
		got = append(got, src[start:end]...)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("reassembled bytes mismatch: got %v want %v", got, want)
	}
}

func TestBoundsRejectsBadIndex(t *testing.T) {
	if _, _, err := Bounds(-1, 10, 4); err == nil {
		t.Fatal("Bounds(-1, ...) should error")
	}
	if _, _, err := Bounds(99, 10, 4); err == nil {
		t.Fatal("Bounds(99, ...) should error")
	}
}

func TestSHA256Bytes(t *testing.T) {
	// Known vector: sha256("hello")
	got := SHA256Bytes([]byte("hello"))
	want := "sha256:2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824"
	if got != want {
		t.Fatalf("SHA256Bytes(\"hello\") = %s, want %s", got, want)
	}
}

func TestSHA256ReaderMatchesBytes(t *testing.T) {
	data := []byte("the quick brown fox jumps over the lazy dog")
	want := SHA256Bytes(data)
	got, err := SHA256Reader(strings.NewReader(string(data)))
	if err != nil {
		t.Fatalf("SHA256Reader: %v", err)
	}
	if got != want {
		t.Fatalf("reader=%s bytes=%s", got, want)
	}
}

func TestSHA256ReaderRejectsNil(t *testing.T) {
	_, err := SHA256Reader(nil)
	if err == nil {
		t.Fatal("SHA256Reader(nil) should error")
	}
}

type errReader struct{ err error }

func (e errReader) Read(_ []byte) (int, error) { return 0, e.err }

func TestSHA256ReaderPropagatesError(t *testing.T) {
	want := errors.New("boom")
	if _, err := SHA256Reader(errReader{err: want}); !errors.Is(err, want) {
		t.Fatalf("err=%v want %v", err, want)
	}
}
