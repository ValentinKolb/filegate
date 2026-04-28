#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$ROOT_DIR"

CHAOS_DURATION="${FILEGATE_CHAOS_DURATION:-60s}"

# Linux-only detector chaos test (burst duplicates + frequent unknown/rescan triggers).
docker run --rm \
  -e FILEGATE_CHAOS=1 \
  -e FILEGATE_CHAOS_DURATION="$CHAOS_DURATION" \
  -v "$ROOT_DIR":/src \
  -w /src \
  golang:1.25 \
  sh -c 'go test ./cli -run "TestConsumeDetectorEventsChaos" -count=1 -v'
