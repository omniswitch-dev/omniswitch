# Bifrost (Maxim AI) -- feature-parity decisions for the harness

- **Custom provider pattern.** Both cells use Bifrost's documented
  `custom_provider_config.base_provider_type: "openai"` plus
  `network_config.base_url` to point at the mock upstream, per
  https://docs.getbifrost.ai/deployment-guides/config-json/providers.
- **Governance/auth.** `governance.api_keys` in the production cell is a
  best-effort match for "auth on" -- the exact schema field name/shape for
  Bifrost's API-key gating was not independently re-verified against the
  current schema (`https://www.getbifrost.ai/schema`) in this session.
  **Action before the first measured run:** fetch that JSON schema and
  validate both config files against it.
- **Retry/timeout.** `network_config.max_retries` / `timeout_seconds` in
  the production cell are a best-effort match to the harness's 2-retry/
  10s-timeout parity rule; not independently confirmed as the correct field
  names. Bifrost's per-status-code retry behavior (429 vs 5xx) and its
  documented fallback-chain config (multiple providers/models in priority
  order) are not yet represented here; add them once the schema is
  confirmed, since fallback chains are one of Bifrost's headline claims.
- **Cache/guardrails.** Not configured in either cell pending schema
  confirmation for Bifrost's semantic-cache and guardrail blocks (both are
  documented product features per `docs/comparison/claims.yaml`, but this
  session did not find a concrete config example for either). S6 and S8
  are `n/a` for Bifrost until that config is added and verified against a
  running container.
- **Not yet execution-validated.** These configs have not been run against
  a live Bifrost container with the mock upstream in this environment.
