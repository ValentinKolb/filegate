#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$ROOT_DIR"

BASE_URL="${FILEGATE_BASE_URL:-http://127.0.0.1:8080}"
TOKEN="${FILEGATE_TOKEN:-}"
PATH_BASE="${FILEGATE_PATH_BASE:-data/bench}"
DURATION="${FILEGATE_BENCH_DURATION:-20s}"
CLIENT_MATRIX="${FILEGATE_CLIENT_MATRIX:-1 8 32 128}"

if [[ -z "$TOKEN" ]]; then
  echo "FILEGATE_TOKEN is required" >&2
  exit 1
fi

mkdir -p bench/results
TS="$(date +%Y%m%d_%H%M%S)"
CSV="bench/results/http-bench-${TS}.csv"

TMP_DIR="$(mktemp -d)"
trap 'rm -rf "$TMP_DIR"' EXIT

dd if=/dev/zero of="$TMP_DIR/read-4k.bin" bs=4096 count=1 status=none
dd if=/dev/zero of="$TMP_DIR/read-1m.bin" bs=1M count=1 status=none

upload_fixture() {
  local local_file="$1"
  local remote_path="$2"
  curl -fsS -X PUT \
    -H "Authorization: Bearer ${TOKEN}" \
    -H 'Content-Type: application/octet-stream' \
    --data-binary "@${local_file}" \
    "${BASE_URL}/v1/paths/${remote_path}" >/dev/null
}

echo "preparing fixtures under ${PATH_BASE}/fixtures"
upload_fixture "$TMP_DIR/read-4k.bin" "${PATH_BASE}/fixtures/read-4k.bin"
upload_fixture "$TMP_DIR/read-1m.bin" "${PATH_BASE}/fixtures/read-1m.bin"

scenarios=(metadata-path metadata-id read-4k read-1m write-4k write-1m mixed)
for scenario in "${scenarios[@]}"; do
  for clients in $CLIENT_MATRIX; do
    echo "running scenario=${scenario} clients=${clients} duration=${DURATION}"
    go run ./cmd/filegate-bench \
      --base-url "$BASE_URL" \
      --token "$TOKEN" \
      --path-base "$PATH_BASE" \
      --scenario "$scenario" \
      --clients "$clients" \
      --duration "$DURATION" \
      --output-csv "$CSV"
  done
done

echo "wrote $CSV"
