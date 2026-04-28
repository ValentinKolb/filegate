package jobs

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func closerCtx(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func TestSchedulerDoDeduplicatesByKey(t *testing.T) {
	s := New(8, 64)
	defer func() {
		if err := s.Close(closerCtx(t)); err != nil {
			t.Errorf("close: %v", err)
		}
	}()

	var runs atomic.Int32
	release := make(chan struct{})

	const goroutines = 32
	const expectedJoiners = goroutines - 1

	var wg sync.WaitGroup
	wg.Add(goroutines)
	errs := make(chan error, goroutines)

	// Barrier: hold all goroutines at the gate, then release simultaneously.
	// The first to win s.inFlight.LoadOrStore runs the function; the rest
	// attach as joiners. We then wait deterministically until every joiner has
	// registered before unblocking the function — that is what guarantees no
	// late arriver can start a second invocation.
	var ready sync.WaitGroup
	ready.Add(goroutines)
	gate := make(chan struct{})

	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			ready.Done()
			<-gate
			_, err := s.Do(context.Background(), "same-key", func(context.Context) (any, error) {
				runs.Add(1)
				<-release
				return "ok", nil
			})
			errs <- err
		}()
	}
	ready.Wait()
	close(gate)

	deadline := time.Now().Add(5 * time.Second)
	for {
		call, ok := s.inFlight.Load("same-key")
		if ok && call.joiners.Load() >= expectedJoiners {
			break
		}
		if time.Now().After(deadline) {
			joiners := int32(-1)
			if ok {
				joiners = call.joiners.Load()
			}
			t.Fatalf("joiners did not reach %d in time (got %d)", expectedJoiners, joiners)
		}
		runtime.Gosched()
	}

	close(release)
	wg.Wait()
	close(errs)

	for err := range errs {
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if got := runs.Load(); got != 1 {
		t.Fatalf("runs=%d, want=1", got)
	}
}

// TestSchedulerCloseRejectsRacingSubmissions guards the previously-broken
// path where a submitter could land a call in the queue between Close
// observing it as not-closed (RLock) and Close draining the queue. Such a
// call would never be processed and Do would block forever. Now Close holds
// the write lock against in-progress getOrSubmit critical sections, so a
// racing submitter must either land before Close (and drain with ErrClosed)
// or see ErrClosed itself.
func TestSchedulerCloseRejectsRacingSubmissions(t *testing.T) {
	const submitters = 32
	for attempt := 0; attempt < 10; attempt++ {
		s := New(2, submitters)

		var ready sync.WaitGroup
		ready.Add(submitters)
		gate := make(chan struct{})

		results := make(chan error, submitters)
		for i := 0; i < submitters; i++ {
			i := i
			go func() {
				ready.Done()
				<-gate
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				_, err := s.Do(ctx, fmt.Sprintf("race-%d-%d", attempt, i), func(context.Context) (any, error) {
					return "ok", nil
				})
				results <- err
			}()
		}
		ready.Wait()
		close(gate)

		// Race Close against the submitters. Under the old code, some
		// submissions could be silently orphaned; their Do call would then
		// time out via the per-call context above instead of returning
		// promptly with ErrClosed.
		closeErr := s.Close(closerCtx(t))
		if closeErr != nil && !errors.Is(closeErr, ErrCloseTimeout) {
			t.Fatalf("close err=%v", closeErr)
		}

		for i := 0; i < submitters; i++ {
			err := <-results
			// Acceptable outcomes: nil (job ran), ErrClosed (rejected at
			// submit), ErrQueueFull (rejected at submit). Anything else —
			// most notably context.DeadlineExceeded — means a job was
			// silently orphaned.
			if err == nil || errors.Is(err, ErrClosed) || errors.Is(err, ErrQueueFull) {
				continue
			}
			t.Fatalf("submitter saw err=%v (expected nil, ErrClosed, or ErrQueueFull) — close vs submit race resurfaced", err)
		}
	}
}

func TestSchedulerCloseTimesOutOnUncooperativeJob(t *testing.T) {
	s := New(1, 1)
	stuck := make(chan struct{})
	defer close(stuck)

	started := make(chan struct{})
	go func() {
		_, _ = s.Do(context.Background(), "stuck", func(jobCtx context.Context) (any, error) {
			close(started)
			// Deliberately ignore jobCtx — simulates a worker that
			// cannot be cancelled cooperatively (e.g. waiting on a slow
			// syscall). Close must still return promptly.
			<-stuck
			return nil, nil
		})
	}()

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatalf("stuck job did not start")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	err := s.Close(ctx)
	elapsed := time.Since(start)
	if !errors.Is(err, ErrCloseTimeout) {
		t.Fatalf("close err=%v want ErrCloseTimeout", err)
	}
	if elapsed > 1*time.Second {
		t.Fatalf("close took %s, want < 1s", elapsed)
	}
}

func TestSchedulerReturnsQueueFull(t *testing.T) {
	s := New(1, 1)

	block := make(chan struct{})
	// Release blocked jobs before Close runs, so the worker can exit
	// cleanly. Without this Close would have to abort via its context
	// timeout, polluting the test outcome.
	var blockOnce sync.Once
	releaseBlock := func() { blockOnce.Do(func() { close(block) }) }
	defer func() {
		releaseBlock()
		if err := s.Close(closerCtx(t)); err != nil {
			t.Errorf("close: %v", err)
		}
	}()

	firstStarted := make(chan struct{})

	// Submit "first" via the package-internal helper. This avoids racing two
	// goroutines on a 1-slot queue and gives us deterministic ordering.
	_, _, err := s.getOrSubmit("first", func(context.Context) (any, error) {
		close(firstStarted)
		<-block
		return nil, nil
	})
	if err != nil {
		t.Fatalf("submit first: %v", err)
	}

	// Wait until the worker has actually picked "first" up. After this point
	// the queue is empty again and the worker is parked in <-block.
	select {
	case <-firstStarted:
	case <-time.After(2 * time.Second):
		t.Fatalf("first job did not start in time")
	}

	// Fill the single queue slot with "second".
	_, _, err = s.getOrSubmit("second", func(context.Context) (any, error) {
		return nil, nil
	})
	if err != nil {
		t.Fatalf("submit second: %v", err)
	}

	// "third" must fail — queue is full and worker is busy.
	_, _, err = s.getOrSubmit("third", func(context.Context) (any, error) {
		return nil, nil
	})
	if !errors.Is(err, ErrQueueFull) {
		t.Fatalf("err=%v, want ErrQueueFull", err)
	}
}
