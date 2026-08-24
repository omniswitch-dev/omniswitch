# Observability Egress

OmniSwitch ships with SQLite-backed logs, traces, sessions, and feedback, plus
two standard egress paths for external observability platforms.

## OpenTelemetry (traces)

Every request creates an OTel span enriched with LLM attributes (model,
provider, tokens, cache hits, decisions). Export via OTLP/HTTP:

```yaml
observability:
  otlp_endpoint: https://otel.yourcompany.com:4318
  service_name: omniswitch-prod
  headers:
    authorization: "Bearer ${OTEL_TOKEN}"
  insecure: false
  timeout: 10s
```

Environment equivalents: `OMNISWITCH_OTEL_ENDPOINT`, `OMNISWITCH_OTEL_HEADERS`,
`OMNISWITCH_OTEL_SERVICE_NAME`, `OMNISWITCH_OTEL_ENABLED`,
`OMNISWITCH_OTEL_INSECURE`, `OMNISWITCH_OTEL_TIMEOUT`.

### Langfuse

Langfuse ingests OTLP traces natively. Point the OTLP endpoint at your
Langfuse region (`https://cloud.langfuse.com/...` or self-hosted) and set the
basic-auth header pair from your Langfuse keys:

```bash
export OMNISWITCH_OTEL_ENABLED=true
export OMNISWITCH_OTEL_ENDPOINT=https://cloud.langfuse.com/api/public/otel/v1/traces
# base64("pk-...:sk-...")
export OMNISWITCH_OTEL_HEADERS="authorization=Basic <b64>"
export OMNISWITCH_OTEL_SERVICE_NAME=omniswitch
```

Traces appear in Langfuse with provider latency, guardrail decisions, and cost
attributes attached to spans.

### Jaeger / Grafana Tempo

Any OTLP/HTTP collector works. For local development:

```bash
docker run --rm -p 4318:4318 -p 16686:16686 jaegertracing/all-in-one:latest
export OMNISWITCH_OTEL_ENABLED=true
export OMNISWITCH_OTEL_ENDPOINT=http://localhost:4318
export OMNISWITCH_OTEL_INSECURE=true
```

## Prometheus (metrics)

Enable `/metrics` on the gateway port:

```bash
export OMNISWITCH_PROMETHEUS_ENABLED=true
curl -s localhost:8080/metrics | head
```

Scrape config:

```yaml
scrape_configs:
  - job_name: omniswitch
    static_configs:
      - targets: ["omniswitch.internal:8080"]
```

## Built-in trace waterfall

The dashboard renders per-request waterfalls from span data recorded in the
`request_logs.metadata` column (`_spans`): budget checks, input guardrails,
cache lookup, provider calls, and output guardrails with durations. No external
system required.
