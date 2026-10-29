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

## Run with Docker Compose

`docker-compose.yml` enables authentication, so the first start needs a
bootstrap key:

```bash
export OMNISWITCH_BOOTSTRAP_API_KEY="$(openssl rand -hex 32)"
OPENAI_API_KEY=sk-... docker compose up -d --build
```

On an empty database the gateway exits with "authentication is enabled but no
API keys exist" when `OMNISWITCH_BOOTSTRAP_API_KEY` is not set; because the
service restarts unless stopped, `docker compose logs gateway` then shows that
error repeatedly. Compose itself does not require the variable, so
`docker compose config`, `ps`, `logs` and `down` work without it. After the
first start the key is stored (hashed) in the database and the variable can be
left unset. If you set it again, use the same value: a different value rotates
the bootstrap owner key and the old key stops working.

The service restarts unless stopped. To stamp the image with build metadata
(reported by `omniswitch version` and `GET /api/version`), set
`OMNISWITCH_BUILD_VERSION`, `OMNISWITCH_BUILD_COMMIT`, and
`OMNISWITCH_BUILD_DATE` before building.

## Compose with Postgres

The base compose quickstart uses SQLite. To use a shared Postgres 16 store,
set a URI-safe password in your shell or an uncommitted `.env` file, alongside
your bootstrap key and provider credentials:

```bash
export OMNISWITCH_POSTGRES_PASSWORD="$(openssl rand -hex 32)"
export OMNISWITCH_BOOTSTRAP_API_KEY="$(openssl rand -hex 32)"
docker compose -f docker-compose.yml -f docker-compose.postgres.yml up --build
```

The overlay waits for Postgres readiness before starting the gateway and keeps
Postgres data in a named volume. It exposes no database port on the host.
`sslmode=disable` is for this local compose network only; use TLS for an external
production database. Exact and semantic cache entries are stored in Postgres with the rest of the
control-plane data. Existing SQLite data is **not** copied into Postgres.
Unlike the bootstrap key, `OMNISWITCH_POSTGRES_PASSWORD` is required by every
compose command that loads the overlay, because the gateway needs the database
URL on every start.

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

For a shared Postgres database, use the example overlay:

```bash
kubectl kustomize deploy/kubernetes-postgres
# Replace the example database URL and bootstrap/vault/provider secrets first.
kubectl apply -k deploy/kubernetes-postgres
```

The overlay reads `OMNISWITCH_DATABASE_URL` from the `database-url` key in
`omniswitch-database`, removes the SQLite PVC and `/data` mount, and uses three
replicas with the base Redis service. It does not deploy Postgres or migrate
existing SQLite data. The database Secret is an example only; supply your own
through a secret manager. Mounted `/data` directories must be writable by
uid/gid **65532**; pre-provision host paths with that ownership. `fsGroup: 65532`
handles volume ownership only when the storage driver supports it.

The [Helm chart](../deploy/helm/omniswitch/README.md) provides database Secrets,
replica validation, configurable startup/readiness/liveness probes, an optional
PodDisruptionBudget, and authenticated Prometheus ServiceMonitors. Private
metrics must use a bearer token; annotation-based scraping is enabled only when
`observability.prometheus.public=true` explicitly permits anonymous reads.
With auth on (the chart default), no OIDC and no bootstrap key supplied, the
chart generates one into its Secret (`bootstrap.generate=true`) and keeps it
across `helm upgrade`. `helm template`, Argo CD and similar GitOps tools cannot
keep it stable (each render creates a new key, which rotates the owner key), so
with those tools set `bootstrap.generate=false` and supply the key through
`existingSecret` or `secrets`. OTLP export headers can come from an existing Secret through
`observability.otel.headersSecret`. See the chart README for details.

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
