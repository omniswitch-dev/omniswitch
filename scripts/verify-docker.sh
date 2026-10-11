#!/usr/bin/env bash
# Usage: DOCKER="sudo -n docker" OMNISWITCH_PORT=18100 scripts/verify-docker.sh IMAGE
set -euo pipefail

read -r -a DOCKER_CMD <<< "${DOCKER:-docker}"
IMAGE="${1:-omniswitch:ci}"
PORT="${OMNISWITCH_PORT:-18100}"
START_TIMEOUT="${OMNISWITCH_START_TIMEOUT:-90}"
BOOTSTRAP_KEY=ci-docker-bootstrap-key
BASE_URL="http://127.0.0.1:${PORT}"
CONTAINERS=()
VOLUME="omniswitch-ci-data-$$-${RANDOM}"
CONTAINER_PREFIX="omniswitch-ci-$$-${RANDOM}"

# Integrator: add /metrics with OMNISWITCH_METRICS_PUBLIC=true after P2d,
# and / (the public console shell) after C lands. Keep public probes here.
PUBLIC_PROBES=(/healthz /readyz)

cleanup() {
  local status=$?
  for container in "${CONTAINERS[@]}"; do
    if [ "$status" -ne 0 ]; then
      "${DOCKER_CMD[@]}" logs "$container" >&2 || true
    fi
    "${DOCKER_CMD[@]}" rm -f "$container" >/dev/null 2>&1 || true
  done
  "${DOCKER_CMD[@]}" volume rm "$VOLUME" >/dev/null 2>&1 || true
  return "$status"
}
trap cleanup EXIT
trap 'exit 1' HUP INT TERM

check_status() {
  local path="$1" expected="$2"
  shift 2
  local actual
  actual=$(curl -sS --max-time 5 -o /dev/null -w '%{http_code}' "$@" "${BASE_URL}${path}")
  echo "GET ${path} -> ${actual} (expected ${expected})"
  if [ "$actual" != "$expected" ]; then
    return 1
  fi
}

for mode in no-volume named-volume; do
  volume_args=()
  if [ "$mode" = named-volume ]; then
    "${DOCKER_CMD[@]}" volume create "$VOLUME" >/dev/null
    volume_args=(-v "${VOLUME}:/data")
  fi
  echo "==> Checking ${IMAGE} (${mode})"
  container="${CONTAINER_PREFIX}-${mode}"
  # Track the name before run: even a failed container start needs cleanup.
  CONTAINERS+=("$container")
  "${DOCKER_CMD[@]}" run -d --name "$container" -p "127.0.0.1:${PORT}:8080" \
    -e OMNISWITCH_ALLOW_NO_PROVIDERS=true \
    -e OMNISWITCH_AUTH=true \
    -e "OMNISWITCH_BOOTSTRAP_API_KEY=${BOOTSTRAP_KEY}" \
    -e OMNISWITCH_MCP_ENABLED=false \
    "${volume_args[@]}" "$IMAGE" serve >/dev/null

  ready=0
  for ((attempt = 0; attempt < START_TIMEOUT; attempt++)); do
    if curl -fsS --max-time 2 -o /dev/null "${BASE_URL}/readyz" 2>/dev/null; then
      ready=1
      break
    fi
    if [ "$("${DOCKER_CMD[@]}" inspect --format '{{.State.Running}}' "$container")" != true ]; then
      echo "Container exited before /readyz returned 200" >&2
      exit 1
    fi
    sleep 1
  done
  if [ "$ready" -ne 1 ]; then
    echo "/readyz did not return 200 within ${START_TIMEOUT}s" >&2
    exit 1
  fi

  for path in "${PUBLIC_PROBES[@]}"; do
    check_status "$path" 200
  done
  check_status /v1/models 200 -H "Authorization: Bearer ${BOOTSTRAP_KEY}"
  check_status /v1/models 401

  "${DOCKER_CMD[@]}" rm -f "$container" >/dev/null
  CONTAINERS=()
  echo "OK ${mode}"
done
