// Package filegate provides a stateless Go client SDK for the Filegate HTTP API.
// It offers scoped namespaces for all API endpoints with both typed and raw
// response methods suitable for direct use and relay/proxy patterns.
//
// The package focuses on:
//   - Typed JSON responses for metadata, search, and upload operations.
//   - Raw HTTP responses for streaming content and relay scenarios.
//   - Chunked upload helpers with progress and completion tracking.
//
// Key Components:
//
//   - Filegate: top-level client with scoped namespace fields.
//   - PathsClient: virtual path operations (get, put, putRaw).
//   - NodesClient: ID-oriented CRUD, content streaming, thumbnails.
//   - ChunkedUploadClient: resumable chunked upload lifecycle.
//   - SearchClient: glob-based file search.
//   - IndexClient: index maintenance and path/ID resolution.
//   - StatsClient: runtime statistics retrieval.
//
// Pure helpers live in dedicated subpackages so callers without an HTTP
// client can use them:
//
//   - sdk/filegate/chunks: chunk math + sha256 in Filegate's checksum format.
//   - sdk/filegate/relay: HTTP response passthrough for proxy handlers.
//
// Related Packages:
//
//   - api/v1: canonical type definitions aliased by this package.
package filegate
