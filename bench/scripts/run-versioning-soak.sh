#!/usr/bin/env bash
set -euo pipefail

# Versioning soak. Drives auto-capture, snapshots, pin/unpin, and
# restores against a small file pool for a configurable window, then
# asserts the blob/metadata accounting stayed consistent and the
# pruner kept per-file version counts bounded.
#
# Tunables:
#   FILEGATE_VERSIONING_SOAK_DURATION  (default 30s)
#   FILEGATE_VERSIONING_DOCKER_IMAGE   (default golang:1.25)

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$ROOT_DIR"

DURATION="${FILEGATE_VERSIONING_SOAK_DURATION:-30s}"
DOCKER_IMAGE="${FILEGATE_VERSIONING_DOCKER_IMAGE:-golang:1.25}"

docker run --rm \
  -e FILEGATE_VERSIONING_SOAK=1 \
  -e FILEGATE_VERSIONING_SOAK_DURATION="$DURATION" \
  -v "$ROOT_DIR":/src \
  -w /src \
  "$DOCKER_IMAGE" \
  sh -c 'go test ./cli -run "TestVersioningSoak" -count=1 -v -timeout 5m'
