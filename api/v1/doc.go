// Package v1 defines the versioned API request and response types for Filegate.
// These types form the JSON contract between the HTTP transport layer and clients,
// including the Go and TypeScript SDKs.
//
// The package focuses on:
//   - Node metadata and directory listing response shapes.
//   - Chunked upload request/status/progress types.
//   - Transfer, search, index, and stats contracts.
//   - Ownership and error envelope definitions.
//
// Related Packages:
//
//   - adapter/http: HTTP handlers that produce and consume these types.
//   - sdk/filegate: Go SDK that aliases these types for external consumers.
package v1
