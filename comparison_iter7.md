# OmniSwitch vs Competitors - Current State (After Iteration 8)

| Feature | OmniSwitch | LiteLLM | Portkey | agentgateway | Bifrost | Kong AI Gateway |
|---|---|---|---|---|---|---|
| Shared state/HA | Postgres + SQLite | Postgres | Managed | K8s CRDs | No | Kong |
| Provider breadth | 15+ presets | 100+ | 1600+ | 10+ | 12+ | Major only |
| MCP: OpenAPI conversion | Yes | No | Partial | Yes | No | No |
| MCP: trace propagation | Yes SEP-414 | No | Yes | Yes | No | No |
| MCP: auto handshake (stdio) | Yes | No | No | Yes | No | No |
| MCP: OAuth client flows / token exchange / ID-JAG | Yes (RFC 8693/7523) | No | No | Yes Full | No | No |
| MCP stateless server mode | Partial (auto-handshake) | No | No | Yes | No | No |
| A2A task lifecycle | Full + SSE | No | No | Yes | No | No |
| Guardrails | Fallback/reroute, webhook retries, Rust-WASM accel | Basic | Advanced + retry/fallback | Regex + external | Built-in | Plugin |
| Guardrail: retry/reroute on provider errors | Yes (fallback reroute) | No | Yes | No | No | No |
| Evals/experiments | Policy replay only | No | Full | No | No | No |
| Published benchmarks | Tool exists, no cross-gateway run | Blog | Blog | None | Blog (11us) | Blog |
| Provider count | 15+ presets + custom | 100+ | 1600+ | 10+ | 12+ | Major |
| Release engineering | goreleaser, GHCR, Homebrew | Docker | SaaS | Helm | Binary | Kong |
| HA control plane | Postgres only | Postgres | Managed | K8s CRDs | No | Kong |
| SSO/SAML/SCIM/mTLS | No | Enterprise | Yes | No | No | Yes |
| Helm chart | Yes | Helm | Managed | Helm | Binary | Kong |
| Evals/experiments | Policy replay only | No | Full | No | No | No |

## Top Remaining Gaps (by impact)
1. **Evals/experiments** - Portkey/TensorZero moat
2. **Published cross-gateway benchmarks** - Bifrost/LiteLLM marketing
3. **A2A push notifications** - agentgateway
4. **MCP stateless server mode** - agentgateway (partially done - auto-handshake done)
5. **Guardrail retry/reroute on provider errors** - Partially done (fallback reroute done)
5. **SSO/SAML/SCIM/mTLS** - Enterprise requirement
6. **MCP stateless server mode** - agentgateway (partially done)