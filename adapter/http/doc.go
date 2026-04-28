// Package httpadapter provides the HTTP transport layer for the Filegate API.
// It maps incoming HTTP requests to domain service calls and encodes responses
// as JSON or streaming content.
//
// The package focuses on:
//   - RESTful routing for paths, nodes, uploads, transfers, search, and index endpoints.
//   - Bearer-token authentication middleware.
//   - Chunked upload lifecycle management with concurrent write control.
//   - On-demand thumbnail generation with LRU caching.
//
// Key Components:
//
//   - NewRouter: constructs the HTTP handler tree with all middleware and routes.
//   - RouterOptions: configuration struct for router initialization.
//   - chunkedManager: manages resumable chunked upload sessions.
//   - thumbnailer: generates and caches image thumbnails via a job scheduler.
//
// Related Packages:
//
//   - domain: business logic and service interface consumed by handlers.
//   - api/v1: shared request/response type definitions.
//   - infra/jobs: bounded worker pool used for thumbnail and EXIF jobs.
package httpadapter
