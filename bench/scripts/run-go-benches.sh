#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$ROOT_DIR"

mkdir -p bench/results
TS="$(date +%Y%m%d_%H%M%S)"
OUT="bench/results/go-bench-${TS}.txt"

{
  echo "# Filegate Go Benchmarks"
  echo "# date: $(date -u +%Y-%m-%dT%H:%M:%SZ)"
  echo "# go: $(go version)"
  echo
  echo "## infra/fgbin"
  go test ./infra/fgbin -run '^$' -bench . -benchmem -count=3
  echo
  echo "## infra/pebble"
  go test ./infra/pebble -run '^$' -bench 'BenchmarkIndex(GetEntity|LookupChild)Hot' -benchmem -count=3
} | tee "$OUT"

echo "wrote $OUT"
