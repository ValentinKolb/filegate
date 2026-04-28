#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$ROOT_DIR"

SOAK_DURATION="${FILEGATE_SOAK_DURATION:-45s}"

# Linux-only long-running detector soak test in containerized Go toolchain.
docker run --rm \
  -e FILEGATE_SOAK=1 \
  -e FILEGATE_SOAK_DURATION="$SOAK_DURATION" \
  -v "$ROOT_DIR":/src \
  -w /src \
  golang:1.25 \
  sh -c 'go test ./cli -run "TestConsumeDetectorEventsSoak" -count=1 -v'
