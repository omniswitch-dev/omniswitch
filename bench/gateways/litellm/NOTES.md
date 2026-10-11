# LiteLLM (Python proxy) -- feature-parity decisions for the harness

- **Worker count.** Production cell runs with `--num_workers 4` to match the
  4-core cpuset allotted to every gateway container (see
  `bench/docker-compose.yml`), per LiteLLM's own production guide
  recommendation of sizing workers to CPU count.
- **Persistence.** Forwarding cell has no `database_url` (no virtual keys,
  no spend tracking, no persistence at all). Production cell adds
  `general_settings.database_url` (Postgres), matching LiteLLM's documented
  production posture, which is Postgres-backed, not SQLite. OmniSwitch's
  production-default cell uses SQLite by default; this is a deliberate,
  disclosed asymmetry (each gateway's *own* documented default), not a
  benchmark tuning choice. Record the Postgres instance's resources in
  `manifest.json` since it adds a dependency LiteLLM needs and OmniSwitch
  does not.
- **Retries/timeouts.** `num_retries: 2`, `timeout: 10`, `retry_after: 0.5`
  per model, matching the parity matrix (2 retries, 10s timeout, 500ms
  backoff) as closely as LiteLLM's config surface allows. LiteLLM's retry
  backoff is not configured per-status-code the way OmniSwitch's
  `retry_codes` is; LiteLLM retries on 429/5xx by default, which is
  recorded as `n/a` for exact status-code parity in the comparison matrix,
  not silently matched.
- **Auth.** Forwarding cell has no `master_key`. Production cell sets
  `general_settings.master_key`, LiteLLM's documented master-key auth
  model, which is closer to OmniSwitch's single bootstrap key than to
  per-workspace virtual keys; virtual keys require the database and are a
  separate (unbenchmarked) feature, exercised only in S8 conformance
  checks, not the latency scenarios.
- **Cache.** `litellm_settings.cache: false` in both baseline cells. S6
  overlays LiteLLM's documented Redis or in-memory semantic cache
  (embedding-based, per LiteLLM's product page) with the same TTL as
  every other gateway's cache cell.
- **Rust gateway.** LiteLLM's optional `rust: true` per-model flag is
  intentionally *not* set here -- see `bench/gateways/litellm-rust/` for
  that as a separate harness entry, since at pin date it only covers a
  subset of providers/routes and is not a like-for-like substitute for the
  full Python proxy.
- **Not yet execution-validated.** These configs have not been run against
  a live LiteLLM container with the mock upstream in this environment; that
  is the next actionable step (`bench/scripts/run-cell.sh` once the mock
  upstream and docker-compose wiring are exercised end-to-end).
