#!/usr/bin/env bash
# CI and local checks share the same Helm rendering cases.
set -euo pipefail

CHART=deploy/helm/omniswitch
DIRECTORY=$(mktemp -d)
trap 'rm -rf "$DIRECTORY"' EXIT

assert_absent() {
  if grep -q "$1" "$2"; then
    echo "Unexpected pattern '$1' in $2" >&2
    return 1
  fi
}

helm lint "$CHART"

helm template test "$CHART" > "$DIRECTORY/defaults.yaml"
grep -q '^kind: PersistentVolumeClaim$' "$DIRECTORY/defaults.yaml"
grep -q '^kind: ServiceAccount$' "$DIRECTORY/defaults.yaml"
assert_absent 'name: OMNISWITCH_DATABASE_URL' "$DIRECTORY/defaults.yaml"
assert_absent 'prometheus.io/scrape:' "$DIRECTORY/defaults.yaml"
grep -q 'startupProbe:' "$DIRECTORY/defaults.yaml"
echo "OK (a) defaults: single-replica SQLite, startup probe, private metrics"

helm template test "$CHART" \
  --set-string 'database.url=postgres://omniswitch:ci-placeholder@postgres:5432/omniswitch?sslmode=require' \
  --set replicaCount=3 --set podDisruptionBudget.enabled=true > "$DIRECTORY/postgres.yaml"
assert_absent '^kind: PersistentVolumeClaim$' "$DIRECTORY/postgres.yaml"
assert_absent 'persistentVolumeClaim:' "$DIRECTORY/postgres.yaml"
grep -q '^kind: PodDisruptionBudget$' "$DIRECTORY/postgres.yaml"
grep -q 'replicas: 3' "$DIRECTORY/postgres.yaml"
grep -A 5 'name: OMNISWITCH_DATABASE_URL' "$DIRECTORY/postgres.yaml" | grep -q 'secretKeyRef:'
grep -A 5 'name: OMNISWITCH_DATABASE_URL' "$DIRECTORY/postgres.yaml" | grep -q 'name: test-omniswitch-database'
grep -A 5 'name: OMNISWITCH_DATABASE_URL' "$DIRECTORY/postgres.yaml" | grep -q 'key: database-url'
echo "OK (b) Postgres + three replicas + PDB: Secret reference, no PVC"

if helm template test "$CHART" --set replicaCount=2 > "$DIRECTORY/invalid.yaml" 2> "$DIRECTORY/invalid.log"; then
  echo "Expected multiple SQLite replicas to fail rendering" >&2
  exit 1
fi
grep -q 'replicaCount > 1 requires database.url or database.existingSecret' "$DIRECTORY/invalid.log"
echo "OK (c) multiple replicas without a database fail rendering"

helm template test "$CHART" --set database.existingSecret=external-database \
  --set database.existingSecretKey=dsn --set replicaCount=3 > "$DIRECTORY/existing.yaml"
assert_absent '^kind: PersistentVolumeClaim$' "$DIRECTORY/existing.yaml"
assert_absent '# Source: omniswitch/templates/database-secret.yaml' "$DIRECTORY/existing.yaml"
grep -A 5 'name: OMNISWITCH_DATABASE_URL' "$DIRECTORY/existing.yaml" | grep -q 'name: external-database'
grep -A 5 'name: OMNISWITCH_DATABASE_URL' "$DIRECTORY/existing.yaml" | grep -q 'key: dsn'
echo "OK existing database Secret and custom key"

helm template test "$CHART" --set observability.prometheus.public=true \
  --set serviceMonitor.enabled=true > "$DIRECTORY/public.yaml"
grep -q 'prometheus.io/scrape: "true"' "$DIRECTORY/public.yaml"
grep -q '^kind: ServiceMonitor$' "$DIRECTORY/public.yaml"
grep -A 1 'name: OMNISWITCH_METRICS_PUBLIC' "$DIRECTORY/public.yaml" | grep -q 'value: "true"'
helm template test "$CHART" --set serviceMonitor.enabled=true \
  --set serviceMonitor.bearerTokenSecret.name=metrics-token \
  --set serviceMonitor.bearerTokenSecret.key=token > "$DIRECTORY/private.yaml"
grep -q 'bearerTokenSecret:' "$DIRECTORY/private.yaml"
assert_absent 'prometheus.io/scrape:' "$DIRECTORY/private.yaml"
if helm template test "$CHART" --set serviceMonitor.enabled=true >/dev/null 2> "$DIRECTORY/metrics-invalid.log"; then
  echo "Expected private ServiceMonitor without a token to fail rendering" >&2
  exit 1
fi
grep -q 'private metrics require' "$DIRECTORY/metrics-invalid.log"
echo "OK public scraping and token-authenticated private ServiceMonitor"

helm template test "$CHART" --set persistence.enabled=false \
  --set serviceAccount.create=false --set startupProbe=null \
  --set readinessProbe.periodSeconds=7 > "$DIRECTORY/ephemeral.yaml"
grep -q 'emptyDir: {}' "$DIRECTORY/ephemeral.yaml"
grep -q 'serviceAccountName: default' "$DIRECTORY/ephemeral.yaml"
assert_absent 'startupProbe:' "$DIRECTORY/ephemeral.yaml"
grep -q 'periodSeconds: 7' "$DIRECTORY/ephemeral.yaml"
helm template test "$CHART" --set podDisruptionBudget.enabled=true \
  --set podDisruptionBudget.maxUnavailable=0 > "$DIRECTORY/pdb.yaml"
grep -q 'maxUnavailable: 0' "$DIRECTORY/pdb.yaml"
assert_absent 'minAvailable:' "$DIRECTORY/pdb.yaml"
echo "OK ephemeral SQLite, configurable probes, default ServiceAccount, maxUnavailable=0"
