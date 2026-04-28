# Benchmarks

This repository provides reproducible benchmark entry points for:

- microbenchmarks (Go test benchmark framework)
- HTTP end-to-end load benchmarks
- compose-based benchmark runs

## Prerequisites

- Go (as defined by `go.mod` toolchain)
- Docker + Docker Compose (for compose benchmark profile)
- curl

## Commands

From repo root:

```bash
make test-detector-linux
make test-detector-soak
make test-detector-chaos
make test-detector-btrfs-real   # requires real btrfs host path
make bench-go
make bench-http      # requires a running filegate endpoint
make bench-compose   # starts compose.test.yml automatically
```

`make test-detector-linux` runs linux-only detector/index sync tests (poll/ext4-like path, btrfs-like unknown fallback path, and duplicate-event stress).
`make test-detector-soak` runs long-running external-change sync validation.
`make test-detector-chaos` runs burst/overlap stress (duplicates + frequent unknown/rescan).
`make test-detector-btrfs-real` runs a real btrfs end-to-end detector/index sync test on host.

## Go Microbenchmarks

Script: [`bench/scripts/run-go-benches.sh`](https://github.com/ValentinKolb/filegate/blob/main/bench/scripts/run-go-benches.sh)

Covers:

- `infra/fgbin` codec benchmarks
- `infra/pebble` hot metadata lookup benchmarks

Output files are written to `bench/results/`.

## HTTP Load Benchmarks

Tooling:

- Go load generator: [`cmd/filegate-bench/main.go`](https://github.com/ValentinKolb/filegate/blob/main/cmd/filegate-bench/main.go)
- matrix script: [`bench/scripts/run-http-bench.sh`](https://github.com/ValentinKolb/filegate/blob/main/bench/scripts/run-http-bench.sh)

Default matrix scenarios:

- `metadata-path`
- `metadata-id`
- `read-4k`
- `read-1m`
- `write-4k`
- `write-1m`
- `mixed`

Metrics per run:

- `ops/s`
- `mbit/s`
- error rate
- avg/p50/p95/p99 latency

Environment variables used by scripts:

- `FILEGATE_BASE_URL`
- `FILEGATE_TOKEN`
- `FILEGATE_PATH_BASE`
- `FILEGATE_BENCH_DURATION`
- `FILEGATE_CLIENT_MATRIX` (e.g. `\"1 8 32 128\"`)

## Compose Profile

Script: [`bench/scripts/run-http-bench-compose.sh`](https://github.com/ValentinKolb/filegate/blob/main/bench/scripts/run-http-bench-compose.sh)

This script:

1. Builds and starts `compose.test.yml`
2. Waits for `/health`
3. Runs benchmark matrix
4. Stops compose stack

## Notes

- `metadata-*` scenarios benchmark API metadata routes, not content download.
- For fair before/after comparisons, keep:
  - same host
  - same filesystem
  - same client matrix
  - same duration and payload sizes
- Nightly detector pipeline is defined in `.github/workflows/detector-nightly.yml`.
