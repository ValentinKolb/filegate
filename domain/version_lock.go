package domain

import "sync"

// fileLockMap serializes mutations on a per-file basis. The versioning
// subsystem uses it to make multi-step operations atomic (snapshot the
// current bytes, replace them, update the index, emit the event) without
// blocking unrelated files.
//
// Why not reuse domain.coalescedDirSyncer? That coalesces directory
// fsyncs — its purpose is to skip redundant fsyncs when many writes hit
// the same dir at once. It does not exclude callers from each other; two
// concurrent Sync(dir) calls run their syncFn one after the other but
// still let arbitrary other code mutate state in between. We need
// proper mutual exclusion here.
//
// The implementation is a sync.Map keyed by FileID with lazy mutex
// allocation. The map never shrinks, but FileIDs are bounded by the
// universe of indexed files so in practice growth tracks file count
// rather than operation count.
type fileLockMap struct {
	m sync.Map // FileID -> *sync.Mutex
}

func newFileLockMap() *fileLockMap { return &fileLockMap{} }

// Acquire returns the mutex for fileID, lazily creating it on first use.
// Callers MUST call Lock/Unlock on the returned mutex; Acquire itself
// does not lock so the caller can defer the unlock at the right scope.
func (l *fileLockMap) Acquire(fileID FileID) *sync.Mutex {
	if existing, ok := l.m.Load(fileID); ok {
		return existing.(*sync.Mutex)
	}
	created := &sync.Mutex{}
	actual, _ := l.m.LoadOrStore(fileID, created)
	return actual.(*sync.Mutex)
}

// With runs fn while holding the per-file lock. Convenience wrapper for
// the common case where the entire critical section fits in one closure.
func (l *fileLockMap) With(fileID FileID, fn func() error) error {
	mu := l.Acquire(fileID)
	mu.Lock()
	defer mu.Unlock()
	return fn()
}
