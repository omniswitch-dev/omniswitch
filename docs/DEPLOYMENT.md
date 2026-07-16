# Deployment

Sentinel is designed to run as a single self-hosted binary.

## Build Binaries

```bash
go build -o bin/sentinel ./cmd/sentinel
go build -o bin/gateway ./cmd/gateway
go build -o bin/proxy ./cmd/proxy
```

## Run the Gateway

```bash
SENTINEL_CONFIG=examples/gateway-config.yaml \
SENTINEL_AUTH=true \
SENTINEL_BOOTSTRAP_API_KEY=replace-with-a-secret-manager-value \
SENTINEL_VAULT_KEY=replace-with-a-persistent-secret \
OPENAI_API_KEY=... \
./bin/gateway
```

## Run with Docker

```bash
docker build -t sentinel-ai-gateway .
docker run --rm -p 8080:8080 \
  -e OPENAI_API_KEY=... \
  -e SENTINEL_AUTH=true \
  -e SENTINEL_BOOTSTRAP_API_KEY=replace-with-a-secret-manager-value \
  -e SENTINEL_CONFIG=examples/gateway-config.yaml \
  sentinel-ai-gateway
```

## Persistence

Set `SENTINEL_DATA` or `gateway.data_dir` to a persistent directory. SQLite stores request logs, API keys, prompts, feedback, cache entries, and shadow logs.

## Production Notes

- Set `SENTINEL_AUTH=true`. For an empty database, also provide
  `SENTINEL_BOOTSTRAP_API_KEY`; Sentinel fails closed instead of starting an
  unmanageable authenticated gateway. Rotate the bootstrap key into a normal
  owner/admin key and remove the bootstrap secret after provisioning.
- Use the default `cache_scope=api_key` (or `workspace` when deliberate cache
  sharing is required); never use `global` for tenant-sensitive traffic.
- Leave `SENTINEL_LOG_PAYLOADS=false` unless storing prompt and response bodies
  is explicitly approved.
- Set an explicit `SENTINEL_CORS_ORIGINS` allow-list for browser clients.
- Keep provider credentials in environment variables or your secret manager.
- Set a stable `SENTINEL_VAULT_KEY` before creating virtual provider keys.
- Mount policy files read-only.
- Back up the SQLite data directory.
- Use a reverse proxy for TLS.
- `/metrics` is Prometheus text format by default; scrape it with an
  authenticated viewer-or-higher key when auth is enabled.
- Run multiple replicas only when each replica has its own SQLite database, or place Sentinel behind a queue/sticky routing layer until external storage is implemented. The in-memory rate limiter is local to each replica.
