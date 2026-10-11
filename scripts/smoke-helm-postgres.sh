#!/usr/bin/env bash
# Run only in a disposable kind cluster after loading omniswitch:kind.
set -euo pipefail

NAMESPACE=omniswitch-smoke
PORT="${OMNISWITCH_PORT:-18100}"
FORWARD_PID=""
DIRECTORY=$(mktemp -d)

cleanup() {
  local status=$?
  if [ "$status" -ne 0 ]; then
    kubectl -n "$NAMESPACE" get pods >&2 || true
    kubectl -n "$NAMESPACE" get events --sort-by=.lastTimestamp >&2 || true
    kubectl -n "$NAMESPACE" logs -l app.kubernetes.io/component=gateway --all-containers --prefix --tail=100 >&2 || true
    kubectl -n "$NAMESPACE" logs deployment/postgres --tail=100 >&2 || true
    cat "$DIRECTORY/port-forward.log" >&2 2>/dev/null || true
  fi
  if [ -n "$FORWARD_PID" ]; then
    kill "$FORWARD_PID" 2>/dev/null || true
    wait "$FORWARD_PID" 2>/dev/null || true
  fi
  rm -rf "$DIRECTORY"
  return "$status"
}
trap cleanup EXIT
trap 'exit 1' HUP INT TERM

kubectl create namespace "$NAMESPACE"
password=$(openssl rand -hex 24)
if [ "${GITHUB_ACTIONS:-}" = true ]; then
  echo "::add-mask::${password}"
fi
kubectl -n "$NAMESPACE" create secret generic postgres --from-literal="password=${password}"
kubectl -n "$NAMESPACE" create secret generic omniswitch-database \
  --from-literal="database-url=postgres://omniswitch:${password}@postgres:5432/omniswitch?sslmode=disable"
kubectl -n "$NAMESPACE" apply -f deploy/helm/omniswitch/ci/postgres.yaml
kubectl -n "$NAMESPACE" rollout status deployment/postgres --timeout=180s

helm upgrade --install smoke deploy/helm/omniswitch --namespace "$NAMESPACE" \
  --set image.repository=omniswitch --set image.tag=kind --set image.pullPolicy=Never \
  --set replicaCount=3 --set database.existingSecret=omniswitch-database \
  --set config.gateway.auth=false --set-string secrets.OMNISWITCH_ALLOW_NO_PROVIDERS=true \
  --set podDisruptionBudget.enabled=true --wait --timeout=6m
kubectl -n "$NAMESPACE" rollout status deployment/smoke-omniswitch --timeout=180s

kubectl -n "$NAMESPACE" port-forward service/smoke-omniswitch \
  --address 127.0.0.1 "${PORT}:8080" > "$DIRECTORY/port-forward.log" 2>&1 &
FORWARD_PID=$!
for attempt in $(seq 1 30); do
  if curl -fsS --max-time 3 "http://127.0.0.1:${PORT}/readyz"; then
    echo "OK three Postgres-backed gateway replicas and GET /readyz -> 200"
    exit 0
  fi
  sleep 1
done
echo "GET /readyz did not return 200" >&2
exit 1
