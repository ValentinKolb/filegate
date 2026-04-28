// Package fgbin defines the binary codec for Filegate index records.
// It encodes and decodes entity and child records into compact byte
// representations stored in the Pebble key-value index.
//
// The package focuses on:
//   - Fixed-width header fields (ID, parent, size, timestamps, permissions).
//   - Variable-length name and MIME type encoding.
//   - Extension fields for optional payloads such as EXIF data.
//   - Deterministic encoding via canonical extension ordering.
//
// Key Components:
//
//   - Entity: full metadata record for a file or directory.
//   - Child: compact listing entry for directory children.
//   - EncodeEntity / DecodeEntity: entity codec pair.
//   - EncodeChild / DecodeChild: child record codec pair.
//
// Related Packages:
//
//   - infra/pebble: stores encoded records as Pebble values.
//   - domain: defines the metadata types that records represent.
package fgbin
