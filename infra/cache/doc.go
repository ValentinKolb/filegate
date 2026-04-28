// Package cache provides a generic LRU cache wrapper used for path-to-ID
// and ID-to-path lookups in the domain service layer.
//
// Key Components:
//
//   - LRU: nil-safe generic cache backed by hashicorp/golang-lru/v2.
//   - NewLRU: constructor with configurable capacity.
//
// Related Packages:
//
//   - domain: uses LRU caches for fast bidirectional path resolution.
package cache
