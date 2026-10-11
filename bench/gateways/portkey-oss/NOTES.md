# Portkey Gateway OSS -- feature-parity decisions for the harness

- **Config delivery model is different from every other gateway in this
  harness.** Portkey's OSS gateway takes its routing config per-request
  (`x-portkey-config` header or request-body `config` field), not a
  gateway-wide config file. `forwarding.json` / `production-default.json`
  here are the Config *documents* that the k6/mock-client harness sends as
  that header on every request -- they are not files Portkey reads off
  disk. Documented at
  https://docs.portkey.ai/docs/product/ai-gateway/configs.
- **Gateway-level auth vs target auth.** The `production-default.json`
  retry object matches the harness's 2-retry / 429,500,502,503,504 parity
  rule. It does **not** yet add a Portkey-side API-key requirement at the
  gateway ingress (separate from the `api_key` field used to reach the
  mock upstream) -- Portkey OSS's own gateway-auth toggle was not
  independently confirmed in this session. Before the first measured
  production-default run, confirm whether Portkey OSS enforces an
  `x-portkey-api-key` (or similar) header by default and, if so, add it to
  both the container env and this note.
- **Cache/guardrails.** Portkey documents a semantic + simple cache and
  guardrails product, but the exact Config schema fields for either were
  not confirmed against a running OSS container in this session. S6/S8 are
  `n/a` for portkey-oss until that's verified.
- **Weighted load balancing / fallback strategy.** `strategy.mode` (e.g.
  `"fallback"`, `"loadbalance"`) is documented and real, but this harness
  only exercises a single target per cell (S1/S2/S4/S5), so multi-target
  strategy config is deferred to a later conformance pass, not included
  here.
- **Not yet execution-validated** against a live `portkeyai/gateway`
  container with the mock upstream in this environment.
