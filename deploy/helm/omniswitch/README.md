# OmniSwitch Helm chart

## Authentication and the bootstrap key

`config.gateway.auth` defaults to `true`. With auth on and no OIDC, the gateway
refuses to start until at least one API key exists, so the first start needs
`OMNISWITCH_BOOTSTRAP_API_KEY`. Provide it in one of these ways:

- `secrets.OMNISWITCH_BOOTSTRAP_API_KEY` (rendered into the chart Secret; an
  empty value counts as not set), or
- `existingSecret`, a Secret you manage that contains an
  `OMNISWITCH_BOOTSTRAP_API_KEY` key (the chart cannot check its contents).

If you set neither, the chart generates a random key in the chart Secret
(`bootstrap.generate: true`, the default). The install NOTES print the command
that reads the generated key. Set `bootstrap.generate: false` to make rendering
fail instead when no key is supplied.

The chart does not generate a key when auth is off or when the release
authenticates through OIDC (`config.identity.oidc.jwks_url` or
`secrets.OMNISWITCH_OIDC_JWKS_URL` is set): the gateway starts without API keys
in that case, and you create keys later only if you need them.

**`helm template`, Argo CD and other GitOps tools.** The chart keeps the
generated key stable across `helm install` and `helm upgrade` by reading the
existing Secret from the cluster (`lookup`). `helm template` cannot read the
cluster, so every render produces a new random key. This affects
`helm template | kubectl apply`, Argo CD (which renders with `helm template`),
Flux with post-renderers, and any other tool that renders the chart offline.
Each sync writes a new key, the gateway rotates the stored owner key to it on
its next restart, and the key you saved stops working; Argo CD also shows the
Secret as permanently out of sync. With these tools set
`bootstrap.generate: false` and supply the key yourself through
`existingSecret` (recommended, for example from a sealed or external secret) or
`secrets.OMNISWITCH_BOOTSTRAP_API_KEY`. Keep the value unchanged afterwards:
a different value rotates the owner key.

The chart sets `config.gateway.listen: ":8080"`. Keep an explicit listen
address: without one, the gateway binds `127.0.0.1` when auth is off, and the
Service cannot reach it.

## OpenTelemetry export headers

`observability.otel.headers` is a map of OTLP export headers. The chart joins it
into one comma-separated `key=value` value, stores it in a dedicated
`<release>-otel` Secret, and passes it to the gateway as
`OMNISWITCH_OTEL_HEADERS`. Header values must not contain commas. Because Helm
keeps supplied values in its release history, prefer an existing Secret:

```yaml
observability:
  otel:
    enabled: true
    endpoint: otel-collector.observability:4317
    headersSecret:
      name: otel-headers     # stringData: { headers: "authorization=Bearer <token>" }
      key: headers           # optional; defaults to "headers"
```

`headersSecret` takes precedence over `headers`.

## Database and replicas

Defaults run one gateway replica with SQLite on a 10 GiB PVC. More than one
replica requires a shared **Postgres** database: rendering fails unless
`database.url` or `database.existingSecret` is set. The chart does not provision
Postgres. Use exactly one of these settings:

```yaml
database:
  existingSecret: omniswitch-database
  existingSecretKey: database-url
replicaCount: 3
redis:
  enabled: true
podDisruptionBudget:
  enabled: true
  minAvailable: 2
```

Create the database Secret in the release namespace through your secret manager.
Its `database-url` key contains a connection URL such as
`postgres://omniswitch:<URI-encoded-password>@postgres:5432/omniswitch?sslmode=require`.
The gateway receives `OMNISWITCH_DATABASE_URL` through `valueFrom.secretKeyRef`.
Alternatively, `database.url` creates a dedicated Secret with the same key;
remember that Helm retains supplied values in its release history. Prefer an
existing Secret in production. Provider/bootstrap/vault credentials use the
separate top-level `secrets` or `existingSecret` settings.

In Postgres mode the gateway PVC and its `/data` mount are omitted, even when
`persistence.enabled=true`. **Existing SQLite data is not migrated to Postgres.**
Exact and semantic cache entries are stored in Postgres and shared by all
replicas. Redis is needed for shared rate limits; set `redis.enabled=true` for the bundled instance or `redis.url` for an
external one. With neither configured, NOTES warns that multi-replica limits
are per pod. The bundled Redis hostname follows the Helm release name.

Without Postgres, `persistence.enabled=false` uses an ephemeral `emptyDir` and
still permits only one replica. The container runs as uid/gid **65532**. Volume
permissions must allow it to write `/data`; the default `fsGroup: 65532` handles
supported storage drivers. Prepare host-path ownership yourself; do not solve
permission failures by running the gateway as root.

## Probes and disruption budgets

`readinessProbe`, `livenessProbe`, and `startupProbe` accept Kubernetes probe
maps. Defaults use `/readyz`, `/healthz`, and `/readyz` respectively. The startup
probe allows 60 failures at five-second intervals (up to five minutes) for
first-boot migrations. Override thresholds for larger databases. Set a probe
to `null` to disable it; `{}` merges with Helm defaults and does not disable it.

`podDisruptionBudget.enabled=true` renders a `policy/v1` PDB selecting only
this release's gateway pods. `maxUnavailable`, when set, takes precedence over
`minAvailable`. Do not set `minAvailable: 1` on a single replica if voluntary
maintenance must be possible.

## Prometheus scraping and authentication

Metrics are **private by default**. `observability.prometheus.public` sets
`OMNISWITCH_METRICS_PUBLIC`; it is independent of gateway API authentication.

- Annotation-based scraping cannot supply a bearer token. The chart emits
  `prometheus.io/scrape: "true"`, the path, and port **only** when
  `observability.prometheus.enabled=true` and `.public=true`.
- For private metrics, install Prometheus Operator CRDs, enable
  `serviceMonitor`, and supply a Secret containing a viewer-or-higher gateway
  API key. A private ServiceMonitor without a token fails rendering.
- A ServiceMonitor may scrape public metrics without a token. Its selector
  matches the gateway Service, not Redis. `additionalLabels` can match your
  Prometheus instance's ServiceMonitor discovery selector.

```yaml
observability:
  prometheus:
    enabled: true
    public: false
serviceMonitor:
  enabled: true
  interval: 30s
  additionalLabels:
    release: prometheus
  bearerTokenSecret:
    name: omniswitch-metrics
    key: token
```

Keep the token Secret in the chart's namespace. Use the gateway's `/metrics`
path and the Service's named `http` port. Do not manually add scrape annotations
for private metrics: those requests would return 401. Setting `.public=true`
allows anonymous metric reads; protect the endpoint with network policy when
appropriate.

## Checks

From the repository root, `scripts/verify-helm.sh` runs lint and renders the
SQLite, three-replica Postgres/PDB, rejected multi-replica SQLite, existing
Secret, public/private metrics, and configurable probe cases.

The kind/Postgres 16 smoke test in `ci-extras.yml` runs only on a manual
`workflow_dispatch` or the nightly schedule, never on PRs. It builds the local
image, installs three replicas against an ephemeral Postgres Deployment, and
waits for `/readyz`.
