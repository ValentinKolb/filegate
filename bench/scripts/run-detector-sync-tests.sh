#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$ROOT_DIR"

# Run linux-only detector/index sync tests in a linux Go toolchain container.
docker run --rm \
  -v "$ROOT_DIR":/src \
  -w /src \
  golang:1.25 \
  sh -c 'go test ./cli -run "Test(ConsumeDetectorEventsWithPollerSyncsExternalChanges|ApplyDetectorBatchPollLikeSyncsExternalChanges|ApplyDetectorBatchBTRFSLikeUnknownRescansOnlyAffectedMount|ConsumeDetectorEventsStressWithDuplicates|CoalesceDetectorBatchesDrainsQueue)" -count=1'
