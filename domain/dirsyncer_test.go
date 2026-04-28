package domain

import (
	"errors"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// waitForJoiners polls (with runtime.Gosched) until the inflight entry for dir
// has at least the expected number of joiners attached. It avoids time.Sleep
// so it adapts to scheduler load instead of guessing.
func waitForJoiners(t *testing.T, s *coalescedDirSyncer, dir string, want int32) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		f, ok := s.inflightFor(dir)
		if ok && f.joiners.Load() >= want {
			return
		}
		if time.Now().After(deadline) {
			joiners := int32(-1)
			if ok {
				joiners = f.joiners.Load()
			}
			t.Fatalf("joiners did not reach %d in time (got %d)", want, joiners)
		}
		runtime.Gosched()
	}
}

func TestDirSyncerCoalescesConcurrentSameDir(t *testing.T) {
	const workers = 32
	release := make(chan struct{})
	var calls int32
	s := &coalescedDirSyncer{
		inflight: make(map[string]*dirSyncFlight),
		syncFn: func(_ string) error {
			atomic.AddInt32(&calls, 1)
			<-release
			return nil
		},
	}

	gate := make(chan struct{})
	var ready sync.WaitGroup
	ready.Add(workers)
	var wg sync.WaitGroup
	wg.Add(workers)
	errs := make(chan error, workers)
	for i := 0; i < workers; i++ {
		go func() {
			defer wg.Done()
			ready.Done()
			<-gate
			errs <- s.Sync("/tmp/one-dir")
		}()
	}
	ready.Wait()
	close(gate)

	// Hold the syncFn until every joiner has attached. Without this the winner
	// could finish, the inflight entry could be cleared, and a late goroutine
	// would start a new flight — defeating the coalescing test.
	waitForJoiners(t, s, "/tmp/one-dir", workers-1)
	close(release)

	wg.Wait()
	close(errs)

	for err := range errs {
		if err != nil {
			t.Fatalf("sync error: %v", err)
		}
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("syncFn calls=%d, want=1", got)
	}
}

func TestDirSyncerDoesNotCoalesceDifferentDirs(t *testing.T) {
	var calls int32
	started := make(chan struct{}, 2)
	release := make(chan struct{})
	s := &coalescedDirSyncer{
		inflight: make(map[string]*dirSyncFlight),
		syncFn: func(_ string) error {
			atomic.AddInt32(&calls, 1)
			started <- struct{}{}
			<-release
			return nil
		},
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_ = s.Sync("/tmp/dir-a")
	}()
	go func() {
		defer wg.Done()
		_ = s.Sync("/tmp/dir-b")
	}()
	// Wait for both syncFn invocations to start before releasing them. This
	// proves they ran concurrently rather than coalescing.
	<-started
	<-started
	close(release)
	wg.Wait()

	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Fatalf("syncFn calls=%d, want=2", got)
	}
}

func TestDirSyncerPropagatesErrorToAllWaiters(t *testing.T) {
	wantErr := errors.New("boom")
	const waiters = 2
	release := make(chan struct{})
	var calls int32
	s := &coalescedDirSyncer{
		inflight: make(map[string]*dirSyncFlight),
		syncFn: func(_ string) error {
			atomic.AddInt32(&calls, 1)
			<-release
			return wantErr
		},
	}

	gate := make(chan struct{})
	var ready sync.WaitGroup
	ready.Add(waiters)
	var wg sync.WaitGroup
	wg.Add(waiters)
	errs := make(chan error, waiters)
	for i := 0; i < waiters; i++ {
		go func() {
			defer wg.Done()
			ready.Done()
			<-gate
			errs <- s.Sync("/tmp/one-dir")
		}()
	}
	ready.Wait()
	close(gate)
	waitForJoiners(t, s, "/tmp/one-dir", waiters-1)
	close(release)
	wg.Wait()
	close(errs)

	for err := range errs {
		if !errors.Is(err, wantErr) {
			t.Fatalf("sync error=%v, want=%v", err, wantErr)
		}
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("syncFn calls=%d, want=1", got)
	}
}
