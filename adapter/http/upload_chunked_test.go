package httpadapter

import (
	"errors"
	"testing"
	"time"

	"github.com/valentinkolb/filegate/domain"
)

func TestChunkedManagerCloseStopsCleanupLoop(t *testing.T) {
	m := newChunkedManager(nil, time.Hour, 10*time.Millisecond, 1024, 1024, 2048, 0, 0)

	done := make(chan struct{})
	go func() {
		m.Close()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatalf("chunked manager close timed out")
	}

	// Close must be safe to call multiple times.
	m.Close()
}

func TestChunkedManagerCloseWithoutCleanupLoop(t *testing.T) {
	m := newChunkedManager(nil, time.Hour, 0, 1024, 1024, 2048, 0, 0)
	m.Close()
	m.Close()
}

func TestChunkedManagerUsesUploadLimitAsFallback(t *testing.T) {
	m := newChunkedManager(nil, time.Hour, 0, 1024, 1234, 0, 0, 0)
	if m.maxChunkedUploadBytes != 1234 {
		t.Fatalf("maxChunkedUploadBytes=%d, want=1234", m.maxChunkedUploadBytes)
	}
	m.Close()
}

func TestChunkedManagerEnsuresSpaceForUpload(t *testing.T) {
	m := newChunkedManager(nil, time.Hour, 0, 1024, 1234, 1234, 0, 0)
	t.Cleanup(m.Close)
	if err := m.ensureSpaceForUpload(t.TempDir(), 1<<62); !errors.Is(err, domain.ErrInsufficientStorage) {
		t.Fatalf("err=%v, want ErrInsufficientStorage", err)
	}
}
