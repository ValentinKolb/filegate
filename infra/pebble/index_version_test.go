package pebble

import (
	"errors"
	"testing"
)

func TestOpenSetsIndexFormatVersion(t *testing.T) {
	idx, err := Open(t.TempDir(), 16<<20)
	if err != nil {
		t.Fatalf("open index: %v", err)
	}
	defer idx.Close()

	value, closer, err := idx.db.Get(indexFormatVersionKey)
	if err != nil {
		t.Fatalf("read index format version: %v", err)
	}
	defer closer.Close()

	version, err := decodeFormatVersion(value)
	if err != nil {
		t.Fatalf("decode version: %v", err)
	}
	if version != currentIndexFormatVersion {
		t.Fatalf("version=%d, want %d", version, currentIndexFormatVersion)
	}
}

func TestOpenRejectsUnsupportedIndexFormatVersion(t *testing.T) {
	path := t.TempDir()
	idx, err := Open(path, 16<<20)
	if err != nil {
		t.Fatalf("open index: %v", err)
	}

	if err := idx.db.Set(indexFormatVersionKey, encodeFormatVersion(currentIndexFormatVersion+1), nil); err != nil {
		_ = idx.Close()
		t.Fatalf("set mismatched version: %v", err)
	}
	if err := idx.Close(); err != nil {
		t.Fatalf("close index: %v", err)
	}

	_, err = Open(path, 16<<20)
	if err == nil {
		t.Fatalf("expected version mismatch error")
	}
	if !errors.Is(err, ErrUnsupportedIndexFormat) {
		t.Fatalf("unexpected error: %v", err)
	}
}
