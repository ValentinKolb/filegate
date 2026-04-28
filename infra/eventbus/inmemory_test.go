package eventbus

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/valentinkolb/filegate/domain"
)

func TestPublishRecoversPanicsInAsyncHandlers(t *testing.T) {
	b := New()

	var called int32
	var wg sync.WaitGroup
	wg.Add(2)

	b.Subscribe(domain.EventCreated, func(domain.Event) {
		panic("boom")
	})
	b.Subscribe(domain.EventCreated, func(domain.Event) {
		atomic.AddInt32(&called, 1)
		wg.Done()
	})
	b.Subscribe(domain.EventCreated, func(domain.Event) {
		atomic.AddInt32(&called, 1)
		wg.Done()
	})

	b.Publish(domain.Event{Type: domain.EventCreated, At: time.Now()})

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for non-panicking handlers")
	}

	if got := atomic.LoadInt32(&called); got != 2 {
		t.Fatalf("called=%d, want=2", got)
	}
}

func TestPublishRecoversPanicsInInlineFallback(t *testing.T) {
	b := &InMemory{
		subs:        make(map[domain.EventType][]func(domain.Event)),
		parallelism: make(chan struct{}), // unbuffered: always fallback to inline path
	}

	var called int32
	b.Subscribe(domain.EventUpdated, func(domain.Event) {
		panic("boom-inline")
	})
	b.Subscribe(domain.EventUpdated, func(domain.Event) {
		atomic.AddInt32(&called, 1)
	})

	b.Publish(domain.Event{Type: domain.EventUpdated, At: time.Now()})

	if got := atomic.LoadInt32(&called); got != 1 {
		t.Fatalf("called=%d, want=1", got)
	}
}

func TestCloseDrainsAsyncHandlersBeforeReturning(t *testing.T) {
	bus := New()

	releaseHandler := make(chan struct{})
	handlerStarted := make(chan struct{})
	var handlerFinished atomic.Int32

	bus.Subscribe(domain.EventUpdated, func(_ domain.Event) {
		close(handlerStarted)
		<-releaseHandler
		handlerFinished.Add(1)
	})

	bus.Publish(domain.Event{Type: domain.EventUpdated, ID: domain.FileID{0x01}})
	<-handlerStarted

	closeReturned := make(chan struct{})
	go func() {
		bus.Close()
		close(closeReturned)
	}()

	// Close must NOT return while a handler is still running.
	select {
	case <-closeReturned:
		t.Fatalf("Close returned before handler finished")
	case <-time.After(50 * time.Millisecond):
	}

	close(releaseHandler)

	select {
	case <-closeReturned:
	case <-time.After(2 * time.Second):
		t.Fatalf("Close did not return after handler finished")
	}

	if handlerFinished.Load() != 1 {
		t.Fatalf("handler finish counter=%d, want 1", handlerFinished.Load())
	}
}

func TestPublishAfterCloseIsNoop(t *testing.T) {
	bus := New()
	var called atomic.Int32
	bus.Subscribe(domain.EventUpdated, func(_ domain.Event) { called.Add(1) })

	bus.Close()

	// Calling Publish after Close must be safe and a no-op — the handler
	// must NOT fire. Lifecycle code that publishes during shutdown
	// shouldn't have to coordinate with whoever called Close.
	bus.Publish(domain.Event{Type: domain.EventUpdated})
	bus.Publish(domain.Event{Type: domain.EventUpdated})

	if got := called.Load(); got != 0 {
		t.Fatalf("handler fired %d times after Close", got)
	}

	// Close is idempotent.
	bus.Close()
}
