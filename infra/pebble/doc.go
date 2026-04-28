// Package pebble implements the domain.Index interface using CockroachDB's Pebble
// embedded key-value store. It persists file and directory metadata with a compact
// binary encoding and supports versioned index formats.
//
// The package focuses on:
//   - Indexed entity storage keyed by FileID.
//   - Parent-child relationships with deterministic child ordering.
//   - Batch writes for efficient bulk index updates.
//   - Format versioning with explicit migration guards.
//
// Key Components:
//
//   - Index: thread-safe wrapper around a Pebble database.
//   - Open: constructor that initializes or validates the index format.
//
// Related Packages:
//
//   - domain: defines the Index port interface this package fulfills.
//   - infra/fgbin: binary codec for entity and child records.
package pebble
