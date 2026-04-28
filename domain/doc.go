// Package domain provides the core business logic for the Filegate filesystem gateway.
// It defines the central data model, service orchestration, and port interfaces
// that adapters must implement.
//
// The package focuses on:
//   - File and directory metadata modeling with stable UUID v7 identities.
//   - Mount-aware virtual path resolution and ownership management.
//   - Orchestrating index, store, and event bus operations.
//
// Key Components:
//
//   - Service: stateful orchestrator managing mounts, caching, and CRUD operations.
//   - FileID: 16-byte stable identity type derived from UUID v7.
//   - Entity / FileMeta: metadata representations for indexed filesystem objects.
//   - Index, Store, EventBus: port interfaces fulfilled by infrastructure adapters.
//
// Related Packages:
//
//   - adapter/http: HTTP transport layer consuming Service.
//   - infra/pebble: Index implementation backed by Pebble KV store.
//   - infra/filesystem: Store implementation using OS filesystem calls.
//   - infra/eventbus: In-memory EventBus implementation.
package domain
