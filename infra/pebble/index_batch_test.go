package pebble

import (
	"errors"
	"testing"

	cpebble "github.com/cockroachdb/pebble"

	"github.com/valentinkolb/filegate/domain"
)

type failingBatchWriter struct {
	setErr error
	delErr error

	setCalls int
	delCalls int
}

func (b *failingBatchWriter) Set(_, _ []byte, _ *cpebble.WriteOptions) error {
	b.setCalls++
	return b.setErr
}

func (b *failingBatchWriter) Delete(_ []byte, _ *cpebble.WriteOptions) error {
	b.delCalls++
	return b.delErr
}

func TestBatchTracksFirstSetErrorAndStopsFurtherWrites(t *testing.T) {
	wantErr := errors.New("set failed")
	writer := &failingBatchWriter{setErr: wantErr}
	b := &batch{b: writer}

	b.PutEntity(domain.Entity{Name: "x"})
	if !errors.Is(b.err, wantErr) {
		t.Fatalf("batch err=%v, want=%v", b.err, wantErr)
	}
	if writer.setCalls != 1 {
		t.Fatalf("setCalls=%d, want=1", writer.setCalls)
	}

	b.PutChild(domain.FileID{}, "child", domain.DirEntry{})
	b.DelEntity(domain.FileID{})
	b.DelChild(domain.FileID{}, "child")
	if writer.setCalls != 1 {
		t.Fatalf("setCalls after first error=%d, want=1", writer.setCalls)
	}
	if writer.delCalls != 0 {
		t.Fatalf("delCalls after first error=%d, want=0", writer.delCalls)
	}
}

func TestBatchTracksDeleteError(t *testing.T) {
	wantErr := errors.New("delete failed")
	writer := &failingBatchWriter{delErr: wantErr}
	b := &batch{b: writer}

	b.DelEntity(domain.FileID{})
	if !errors.Is(b.err, wantErr) {
		t.Fatalf("batch err=%v, want=%v", b.err, wantErr)
	}
	if writer.delCalls != 1 {
		t.Fatalf("delCalls=%d, want=1", writer.delCalls)
	}
}
