// Package eventbus implements the domain.EventBus interface with an in-memory
// publish/subscribe mechanism. Handlers run asynchronously with bounded
// parallelism and panic recovery.
//
// Key Components:
//
//   - InMemory: thread-safe event bus with configurable handler concurrency.
//   - New: constructor returning a ready-to-use InMemory instance.
//
// Related Packages:
//
//   - domain: defines the EventBus port interface and event types.
package eventbus
