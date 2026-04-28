package eventbus

import (
	"log"
	"sync"
	"sync/atomic"

	"github.com/valentinkolb/filegate/domain"
)

// InMemory is an in-memory event bus with bounded asynchronous handler dispatch.
type InMemory struct {
	mu   sync.RWMutex
	subs map[domain.EventType][]func(domain.Event)

	parallelism chan struct{}

	// activeHandlers tracks in-flight async handler goroutines. Close
	// observes it via wg.Wait to drain cleanly.
	wg sync.WaitGroup

	closed atomic.Bool
}

// New creates an InMemory event bus ready for use.
func New() *InMemory {
	return &InMemory{
		subs:        make(map[domain.EventType][]func(domain.Event)),
		parallelism: make(chan struct{}, 64),
	}
}

// Publish dispatches event to all registered handlers. After Close has been
// called Publish becomes a no-op and is safe to call (callers do not need
// to coordinate with shutdown).
func (b *InMemory) Publish(event domain.Event) {
	if b.closed.Load() {
		return
	}
	b.mu.RLock()
	handlers := append([]func(domain.Event){}, b.subs[event.Type]...)
	handlers = append(handlers, b.subs["*"]...)
	b.mu.RUnlock()

	for _, h := range handlers {
		select {
		case b.parallelism <- struct{}{}:
			b.wg.Add(1)
			go func(handler func(domain.Event)) {
				defer b.wg.Done()
				defer func() { <-b.parallelism }()
				safeInvoke(event, handler)
			}(h)
		default:
			// Backpressure fallback: run inline when the async budget is saturated.
			safeInvoke(event, h)
		}
	}
}

func safeInvoke(event domain.Event, handler func(domain.Event)) {
	defer func() {
		if rec := recover(); rec != nil {
			log.Printf("[filegate] event handler panic type=%s: %v", event.Type, rec)
		}
	}()
	handler(event)
}

// Subscribe registers handler for events of the given type. Use the empty
// string (the "*" sentinel) to subscribe to all events.
func (b *InMemory) Subscribe(eventType domain.EventType, handler func(domain.Event)) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.subs[eventType] = append(b.subs[eventType], handler)
}

// Close marks the bus as closed (Publish becomes a no-op) and waits for
// any in-flight async handler goroutines to finish. Idempotent.
func (b *InMemory) Close() {
	if !b.closed.CompareAndSwap(false, true) {
		return
	}
	b.wg.Wait()
}
