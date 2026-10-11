# OmniSwitch Helm chart

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
The local semantic cache remains ephemeral. Redis is needed for shared rate
limits; set `redis.enabled=true` for the bundled instance or `redis.url` for an
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
