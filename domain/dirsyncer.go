package domain

import (
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
)

type dirSyncFlight struct {
	done chan struct{}
	err  error
	// joiners counts callers that attached to this in-flight sync instead of
	// starting a new one. Tests use it to detect when all expected joiners
	// have arrived before unblocking the underlying syncFn.
	joiners atomic.Int32
}

type coalescedDirSyncer struct {
	mu       sync.Mutex
	inflight map[string]*dirSyncFlight
	syncFn   func(string) error
}

func newDirSyncer() *coalescedDirSyncer {
	return &coalescedDirSyncer{
		inflight: make(map[string]*dirSyncFlight),
		syncFn:   syncDirPath,
	}
}

func (s *coalescedDirSyncer) Sync(dir string) error {
	dir = filepath.Clean(strings.TrimSpace(dir))
	if dir == "" {
		return ErrInvalidArgument
	}

	s.mu.Lock()
	if flight, ok := s.inflight[dir]; ok {
		flight.joiners.Add(1)
		done := flight.done
		s.mu.Unlock()
		<-done
		return flight.err
	}
	flight := &dirSyncFlight{done: make(chan struct{})}
	s.inflight[dir] = flight
	s.mu.Unlock()

	err := s.syncFn(dir)

	s.mu.Lock()
	flight.err = err
	close(flight.done)
	delete(s.inflight, dir)
	s.mu.Unlock()
	return err
}

// inflightFor returns the current flight for dir, if any. Tests use this to
// observe the join state deterministically.
func (s *coalescedDirSyncer) inflightFor(dir string) (*dirSyncFlight, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, ok := s.inflight[dir]
	return f, ok
}
