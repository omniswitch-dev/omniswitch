# Kong AI Gateway -- feature-parity decisions and Enterprise-vs-OSS boundary

- **OSS-only harness.** This cell uses Kong Gateway 3.9 OSS with the
  community `ai-proxy` plugin only. Kong markets an "AI Gateway" product
  line that includes several Enterprise-only plugins; per plan risk
  mitigation, those are explicitly **not tested** here rather than
  silently approximated:
    - Fallback / multi-provider load balancing → **AI Proxy Advanced**
      (Enterprise).
    - Token-aware rate limiting → **AI Rate Limiting Advanced**
      (Enterprise).
    - Semantic cache → separate **AI Semantic Cache** plugin, tier
      unclear, not available in an OSS-only image.
  Any published comparison must state Kong's cells were run OSS-only and
  that Enterprise plugins exist for these capabilities but were not
  measured.
- **route_type / provider fields.** `route_type: llm/v1/chat`,
  `model.provider: openai`, `model.options.upstream_url` are documented
  and confirmed via
  https://docs.konghq.com/hub/kong-inc/ai-proxy/how-to/basic-example/.
- **Auth parity.** Kong's OSS `key-auth` plugin (not an AI-specific
  feature) is used to approximate "auth on" for the production-default
  cell, since `ai-proxy` OSS itself has no bundled API-key/auth toggle
  beyond forwarding a bearer token upstream.
- **Retry parity gap.** The harness's parity rule is 2 retries on
  429/500/502/503/504 with 10s timeout. Kong OSS's `ai-proxy` plugin does
  not expose a per-status-code retry config; `services[].retries: 2` here
  is Kong's core proxy-level retry (applies to connection failures, not
  necessarily to upstream 4xx/5xx application responses). This is a
  **known asymmetry**, not a hidden one -- mark the retry-parity cell
  `n/a`/caveat rather than claiming full parity in the published report.
- **Not yet execution-validated** against a live `kong:3.9` container
  with the mock upstream and DB-less config in this environment.
