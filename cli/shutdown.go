package cli

import (
	"context"
	"errors"
	"log"
	"net/http"
	"sync"
	"time"
)

// shutdownPlan groups the resources a serve-loop graceful shutdown
// needs to drain. Each field is optional — nil values are skipped
// — so the same plan shape works for partial bring-up failures
// and for tests that only need a subset.
type shutdownPlan struct {
	Timeout time.Duration

	// HTTP listeners to drain. Names are used for logging only;
	// they don't need to be unique.
	Listeners []namedListener

	// CancelBackground is the context-cancel that signals the
	// background loops (detector, version pruner, multipart
	// cleanup) to exit. Called AFTER the HTTP listeners have
	// drained, so in-flight requests can still use the service.
	CancelBackground context.CancelFunc

	// BackgroundDone is the set of done-channels every background
	// loop closes when it exits. We wait for all of them before
	// closing the index.
	BackgroundDone []chan struct{}

	// AfterDrain is the list of cleanup callbacks that run after
	// all background loops have exited. Order is preserved, so
	// dependencies (e.g. router.Close before idx.Close) can be
	// expressed by ordering.
	AfterDrain []func() error
}

// namedListener pairs an http.Server with a label for logging.
// "rest" / "s3" are the typical names.
type namedListener struct {
	Name   string
	Server *http.Server
}

// runShutdown executes the plan with proper logging at each phase.
// Returns nil when everything drained cleanly within Timeout, or
// the first non-cancellation error otherwise. Even on error the
// plan is followed to completion (force-close listeners, cancel
// background, run AfterDrain) so resources don't leak.
//
// Phase order:
//
//  1. Server.Shutdown on each listener IN PARALLEL with a shared
//     timeout. Stops accepting new connections; waits for in-flight
//     handlers up to Timeout. The S3 multipart Complete path is the
//     longest in-flight handler we know about (concat parts → fsync
//     → rename → Pebble batch); the default 60s timeout is sized
//     for it.
//
//  2. If Shutdown returned context.DeadlineExceeded for any
//     listener, force-close it via Server.Close so leaked
//     connections die immediately. Operators see a WARN.
//
//  3. Cancel the background context. Every loop sees ctx.Done()
//     on its next tick and exits via close(done).
//
//  4. Wait for all done channels. There's no separate timeout —
//     loops are short-cycle (ticker-based) and known to exit
//     promptly. A misbehaving loop would block forever; that's a
//     bug to surface, not paper over.
//
//  5. Run AfterDrain callbacks in order. Errors are joined and
//     returned but don't short-circuit subsequent callbacks.
func runShutdown(plan shutdownPlan) error {
	if plan.Timeout <= 0 {
		plan.Timeout = 60 * time.Second
	}
	log.Printf("[filegate] shutdown: draining %d listener(s) with timeout %s", len(plan.Listeners), plan.Timeout)
	ctx, cancel := context.WithTimeout(context.Background(), plan.Timeout)
	defer cancel()

	// Phase 1+2: drain listeners in parallel.
	var wg sync.WaitGroup
	listenerErrs := make([]error, len(plan.Listeners))
	for i, l := range plan.Listeners {
		i, l := i, l
		if l.Server == nil {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := l.Server.Shutdown(ctx)
			if err == nil {
				log.Printf("[filegate] shutdown: %s drained cleanly", l.Name)
				return
			}
			if errors.Is(err, context.DeadlineExceeded) {
				log.Printf("[filegate] shutdown: WARN %s did not drain in %s, force-closing", l.Name, plan.Timeout)
				if cerr := l.Server.Close(); cerr != nil {
					listenerErrs[i] = cerr
				} else {
					listenerErrs[i] = err // record the original timeout too
				}
				return
			}
			listenerErrs[i] = err
		}()
	}
	wg.Wait()

	// Phase 3: signal background loops.
	if plan.CancelBackground != nil {
		plan.CancelBackground()
	}

	// Phase 4: wait for background loops.
	for _, done := range plan.BackgroundDone {
		if done == nil {
			continue
		}
		<-done
	}
	if len(plan.BackgroundDone) > 0 {
		log.Printf("[filegate] shutdown: %d background loop(s) drained", len(plan.BackgroundDone))
	}

	// Phase 5: post-drain callbacks.
	var afterErrs []error
	for _, cb := range plan.AfterDrain {
		if cb == nil {
			continue
		}
		if err := cb(); err != nil {
			afterErrs = append(afterErrs, err)
		}
	}

	// Aggregate errors. We don't want to return on the first one
	// — every callback ran, but the operator should hear about
	// problems.
	allErrs := afterErrs
	for _, e := range listenerErrs {
		if e != nil && !errors.Is(e, context.DeadlineExceeded) {
			allErrs = append(allErrs, e)
		}
	}
	if len(allErrs) == 0 {
		log.Printf("[filegate] shutdown: complete")
		return nil
	}
	log.Printf("[filegate] shutdown: complete with %d error(s)", len(allErrs))
	return errors.Join(allErrs...)
}
