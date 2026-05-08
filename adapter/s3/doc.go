// Package s3 implements an S3-compatible HTTP frontend for filegate.
// It runs as a separate listener from the existing REST API and shares
// the same underlying domain.Service — so a file written via REST is
// readable via S3 with the same ETag, and vice versa.
//
// The package is path-style only ("/{bucket}/{key}"), single-tenant
// in M1 (one access key with full access), and verifies SigV4 in
// header, query, and streaming-chunked variants. Bucket names map
// 1:1 to filegate mount names; CreateBucket/DeleteBucket are
// rejected since buckets come from filegate's storage.base_paths
// config.
//
// Detailed design notes live in dex epic y0zjz8bi (the original
// markdown plan was relocated into dex tracking and is no longer in
// the repo).
package s3
