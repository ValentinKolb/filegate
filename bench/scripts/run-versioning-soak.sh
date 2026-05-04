#!/usr/bin/env bash
set -euo pipefail

# Versioning soak. Drives auto-capture, snapshots, pin/unpin,
# restores, and delete+recreate against a configurable file pool for a
# configurable window, then asserts blob/metadata consistency and the
# pruner kept per-file version counts bounded.
#
# Tunables:
#   FILEGATE_VERSIONING_SOAK_DURATION   (default 30s)
#   FILEGATE_VERSIONING_SOAK_FILE_POOL  (default 8 — try 64+ for stress)
#   FILEGATE_VERSIONING_SOAK_RACE       (default off — set =1 for -race)
#   FILEGATE_VERSIONING_SOAK_TIMEOUT    (default 30m wall clock)
#   FILEGATE_VERSIONING_DOCKER_IMAGE    (default golang:1.25)

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$ROOT_DIR"

DURATION="${FILEGATE_VERSIONING_SOAK_DURATION:-30s}"
FILE_POOL="${FILEGATE_VERSIONING_SOAK_FILE_POOL:-8}"
DOCKER_IMAGE="${FILEGATE_VERSIONING_DOCKER_IMAGE:-golang:1.25}"
TIMEOUT="${FILEGATE_VERSIONING_SOAK_TIMEOUT:-30m}"

RACE_FLAG=""
if [[ "${FILEGATE_VERSIONING_SOAK_RACE:-0}" == "1" ]]; then
  RACE_FLAG="-race"
fi

docker run --rm \
  -e FILEGATE_VERSIONING_SOAK=1 \
  -e FILEGATE_VERSIONING_SOAK_DURATION="$DURATION" \
  -e FILEGATE_VERSIONING_SOAK_FILE_POOL="$FILE_POOL" \
  -v "$ROOT_DIR":/src \
  -w /src \
  "$DOCKER_IMAGE" \
  sh -c "go test ./cli -run 'TestVersioningSoak' -count=1 -v -timeout $TIMEOUT $RACE_FLAG"
