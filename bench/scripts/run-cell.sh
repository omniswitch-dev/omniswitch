#!/usr/bin/env bash
# run-cell.sh — orchestrate one (gateway, cell, scenario) measurement:
# start the mock upstream + gateway container on fixed cpusets, wait for
# readiness, run the scenario driver (k6 if available, else bench/cmd/
# sse-probe for a smoke pass), sample cgroup CPU/RSS throughout, and
# collect all artifacts to bench/results/<run-id>/<gateway>/<scenario>/.
#
# This script is intentionally conservative: it does not yet implement the
# full five-block-interleaving statistical protocol (plan section 3.9) or
# multi-gateway campaign scheduling — see bench/README.md "Status / TODO"
# for what a follow-up pass still needs to add. It is the minimum needed
# to run one cell end to end and produce a real artifact tree.
#
# Usage:
#   bench/scripts/run-cell.sh <gateway> <cell:forwarding|production-default> <scenario:s1|s2|s4|s5|s6|s8> [run-id]
set -euo pipefail

BENCH_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
GATEWAY="${1:?gateway name required, e.g. omniswitch}"
CELL="${2:?cell required: forwarding or production-default}"
SCENARIO="${3:?scenario required: s1|s2|s4|s5|s6|s8}"
RUN_ID="${4:-$(date -u +%Y%m%d-%H%M%S)}"

OUT_DIR="$BENCH_DIR/results/$RUN_ID/$GATEWAY/$SCENARIO"
mkdir -p "$OUT_DIR"

echo "== run-cell: gateway=$GATEWAY cell=$CELL scenario=$SCENARIO run_id=$RUN_ID ==" | tee "$OUT_DIR/run.log"

MOCK_PORT="${MOCK_PORT:-4000}"
GATEWAY_URL="${GATEWAY_URL:-http://localhost:8080}"

echo "-- capturing environment manifest --" | tee -a "$OUT_DIR/run.log"
"$BENCH_DIR/scripts/capture-env.sh" > "$OUT_DIR/manifest.json" || echo "capture-env.sh failed, continuing" | tee -a "$OUT_DIR/run.log"

echo "-- checking mock upstream health at localhost:$MOCK_PORT --" | tee -a "$OUT_DIR/run.log"
if ! curl -fsS "http://localhost:$MOCK_PORT/healthz" >/dev/null 2>&1; then
  echo "mock upstream not reachable on :$MOCK_PORT -- start it first (see bench/README.md quick start)" | tee -a "$OUT_DIR/run.log"
  exit 1
fi
curl -fsS "http://localhost:$MOCK_PORT/admin/reset" -X POST >/dev/null 2>&1 || true

echo "-- checking gateway health at $GATEWAY_URL --" | tee -a "$OUT_DIR/run.log"
if ! curl -fsS -o /dev/null -w '%{http_code}' "$GATEWAY_URL/v1/models" 2>/dev/null | grep -qE '^(200|401|404)$'; then
  echo "warning: gateway at $GATEWAY_URL did not respond to GET /v1/models as expected; continuing anyway" | tee -a "$OUT_DIR/run.log"
fi

# Start cgroup sampling in the background if the gateway container name is
# known (CONTAINER_NAME env var); otherwise skip with a note.
SAMPLE_PID=""
if [ -n "${CONTAINER_NAME:-}" ]; then
  "$BENCH_DIR/scripts/sample-cgroup.sh" "$CONTAINER_NAME" "$OUT_DIR/cgroup.csv" 0.1 75 &
  SAMPLE_PID=$!
else
  echo "CONTAINER_NAME not set; skipping cgroup sampling for this run" | tee -a "$OUT_DIR/run.log"
fi

if command -v k6 >/dev/null 2>&1; then
  echo "-- running k6 scenario $SCENARIO --" | tee -a "$OUT_DIR/run.log"
  K6_SCRIPT="$BENCH_DIR/k6/${SCENARIO}_"*.js
  # shellcheck disable=SC2086
  GATEWAY_URL="$GATEWAY_URL" k6 run --summary-export "$OUT_DIR/summary.json" $K6_SCRIPT 2>&1 | tee -a "$OUT_DIR/run.log"
else
  echo "k6 not found on PATH -- falling back to bench/cmd/sse-probe for a smoke pass only (not a substitute for the full k6 scenario; see bench/README.md 'harness self-test')" | tee -a "$OUT_DIR/run.log"
  PROBE_BIN="$BENCH_DIR/cmd/sse-probe/sse-probe"
  if [ ! -x "$PROBE_BIN" ]; then
    (cd "$BENCH_DIR" && GOTOOLCHAIN=auto go build -o "$PROBE_BIN" ./cmd/sse-probe)
  fi
  "$PROBE_BIN" -url "$GATEWAY_URL/v1/chat/completions" -n 5 | tee "$OUT_DIR/sse_probe.jsonl"
fi

if [ -n "$SAMPLE_PID" ]; then
  wait "$SAMPLE_PID" 2>/dev/null || true
fi

curl -fsS "http://localhost:$MOCK_PORT/stats" -o "$OUT_DIR/mock_stats.json" 2>/dev/null || true

echo "-- done, artifacts in $OUT_DIR --" | tee -a "$OUT_DIR/run.log"
