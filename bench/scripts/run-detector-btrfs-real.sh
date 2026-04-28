#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$ROOT_DIR"

BTRFS_ROOT="${FILEGATE_BTRFS_REAL_ROOT:-}"
if [[ -z "$BTRFS_ROOT" ]]; then
  echo "FILEGATE_BTRFS_REAL_ROOT is required (must be a writable path on btrfs)." >&2
  exit 1
fi

# Real btrfs E2E detector/index sync test.
# Requires host btrfs tooling and subvolume create/delete permissions.
FILEGATE_BTRFS_REAL=1 FILEGATE_BTRFS_REAL_ROOT="$BTRFS_ROOT" \
  go test ./cli -run "TestConsumeDetectorEventsWithRealBTRFS" -count=1 -v
