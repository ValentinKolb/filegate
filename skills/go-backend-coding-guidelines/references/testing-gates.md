# Testing and Quality Gates

Run these gates before finishing code changes.

## Mandatory Gates

1. `gofmt -w .`
2. `go test ./...`

## Mandatory for Concurrency or Shared-State Changes

1. `go test -race ./...`

## Mandatory for API/Behavior Changes

1. Add or update handler/service tests for success and expected failure paths.
2. Add or update integration tests for real persistence behavior.
3. Validate error envelope stability (`error`, `message`).

## Mandatory for Data/Infra Contract Changes

1. Add or update tests for isolation and authorization boundaries.
2. Add or update tests for migration/query behavior changes.
3. Add or update tests for idempotency/retry safety where relevant.
4. Add or update tests for metadata/context propagation into events/logs where relevant.

## Recommended Gates

1. `go vet ./...`
2. `staticcheck ./...` (if available)

If a recommended tool is missing in the environment, document the skip reason.
