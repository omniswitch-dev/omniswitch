# Deployment

OmniSwitch is designed to run as a single self-hosted binary.

## Build Binaries

```bash
go build -o bin/omniswitch ./cmd/omniswitch
go build -o bin/gateway ./cmd/gateway
go build -o bin/proxy ./cmd/proxy
```

## Run the Gateway

```bash
OMNISWITCH_CONFIG=examples/gateway-config.yaml \
OMNISWITCH_AUTH=true \
OMNISWITCH_BOOTSTRAP_API_KEY=replace-with-a-secret-manager-value \
OMNISWITCH_VAULT_KEY=replace-with-a-persistent-secret \
OPENAI_API_KEY=... \
./bin/gateway
```

## Run with Docker

```bash
docker build -t omniswitch-ai-gateway .
docker run --rm -p 8080:8080 \
  -e OPENAI_API_KEY=... \
  -e OMNISWITCH_AUTH=true \
  -e OMNISWITCH_BOOTSTRAP_API_KEY=replace-with-a-secret-manager-value \
  -e OMNISWITCH_CONFIG=examples/gateway-config.yaml \
  omniswitch-ai-gateway
```

## Run on Kubernetes

The repository includes a Kustomize baseline in `deploy/kubernetes`. It creates
the gateway Deployment, ClusterIP Service, ConfigMap, Secret example, persistent
SQLite volume, and a Redis instance for shared request-rate limits.

```bash
kubectl apply -k deploy/kubernetes
```

Before production use, replace values in
`deploy/kubernetes/secret.example.yaml` through your cluster secret manager or
an overlay. Keep `replicas: 1` while using the bundled SQLite control-plane
store. Redis lets multiple gateway pods share request quotas, but API keys,
logs, prompts, budgets, and cache entries remain on the mounted SQLite volume
until OmniSwitch is connected to a shared database or external control plane.

## Persistence

Set `OMNISWITCH_DATA` or `gateway.data_dir` to a persistent directory. SQLite stores request logs, API keys, prompts, feedback, cache entries, and shadow logs.

## Production Notes

- Set `OMNISWITCH_AUTH=true`. For an empty database, also provide
  `OMNISWITCH_BOOTSTRAP_API_KEY`; OmniSwitch fails closed instead of starting an
  unmanageable authenticated gateway. Rotate the bootstrap key into a normal
  owner/admin key and remove the bootstrap secret after provisioning.
- Use the default `cache_scope=api_key` (or `workspace` when deliberate cache
  sharing is required); never use `global` for tenant-sensitive traffic.
- Leave `OMNISWITCH_LOG_PAYLOADS=false` unless storing prompt and response bodies
  is explicitly approved.
- Set an explicit `OMNISWITCH_CORS_ORIGINS` allow-list for browser clients.
- Keep provider credentials in environment variables or your secret manager.
- Set a stable `OMNISWITCH_VAULT_KEY` before creating virtual provider keys.
- Mount policy files read-only.
- Back up the SQLite data directory.
- Use a reverse proxy for TLS.
- `/metrics` is Prometheus text format by default; scrape it with an
  authenticated viewer-or-higher key when auth is enabled.
- Use a shared Redis `rate_limit.redis_url` to enforce one quota across replicas. SQLite remains a local control-plane store; use one writer or an external database before horizontally scaling control-plane mutations.
