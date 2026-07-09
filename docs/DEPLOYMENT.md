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
OPENAI_API_KEY=... \
./bin/gateway
```

## Run with Docker

```bash
docker build -t sentinel-ai-gateway .
docker run --rm -p 8080:8080 \
  -e OPENAI_API_KEY=... \
  -e SENTINEL_CONFIG=examples/gateway-config.yaml \
  sentinel-ai-gateway
```

## Persistence

Set `SENTINEL_DATA` or `gateway.data_dir` to a persistent directory. SQLite stores request logs, API keys, prompts, feedback, cache entries, and shadow logs.

## Production Notes

- Set `SENTINEL_AUTH=true` and create scoped API keys.
- Keep provider credentials in environment variables or your secret manager.
- Mount policy files read-only.
- Back up the SQLite data directory.
- Use a reverse proxy for TLS.
- Run multiple replicas only when each replica has its own SQLite database, or place Sentinel behind a queue/sticky routing layer until external storage is implemented.
