#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$ROOT_DIR"

export FILEGATE_BASE_URL="${FILEGATE_BASE_URL:-http://127.0.0.1:4111}"
export FILEGATE_TOKEN="${FILEGATE_TOKEN:-test-integration-token}"
export FILEGATE_PATH_BASE="${FILEGATE_PATH_BASE:-data/bench}"

echo "starting compose benchmark environment"
docker compose -f compose.test.yml up -d --build

cleanup() {
  docker compose -f compose.test.yml down -v >/dev/null 2>&1 || true
}
trap cleanup EXIT

echo "waiting for health endpoint"
for _ in $(seq 1 60); do
  if curl -fsS "${FILEGATE_BASE_URL}/health" >/dev/null; then
    break
  fi
  sleep 1
done

curl -fsS "${FILEGATE_BASE_URL}/health" >/dev/null

"$ROOT_DIR/bench/scripts/run-http-bench.sh"
