package segments

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestCount(t *testing.T) {
	cases := []struct {
		size, segmentSize int64
		want              int
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
		if got := Count(c.size, c.segmentSize); got != c.want {
			t.Errorf("Count(%d, %d) = %d, want %d", c.size, c.segmentSize, got, c.want)
		}
	}
}

func TestPlanCoversWholeFile(t *testing.T) {
	const size, segmentSize = int64(10), int64(4)
	plan, err := Plan(size, segmentSize)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	got := make([]byte, 0, size)
	want := make([]byte, size)
	for i := range want {
		want[i] = byte(i)
	}
	src := bytes.Clone(want)

	for _, seg := range plan {
		got = append(got, src[seg.Offset:seg.Offset+seg.Size]...)
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
