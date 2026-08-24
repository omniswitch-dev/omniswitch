# OmniSwitch vs Competitors - Current State (After Iteration 5)

| Feature | OmniSwitch | LiteLLM | Portkey | agentgateway | Bifrost | Kong AI Gateway |
|---|---|---|---|---|---|---|
| Shared state/HA | Postgres + SQLite | Postgres | Managed | K8s CRDs | No | Kong |
| Provider breadth | 15+ presets | 100+ | 1600+ | 10+ | 12+ | Major only |
| MCP: OpenAPI conversion | Yes | No | Partial | Yes | No | No |
| MCP: trace propagation | Yes SEP-414 | No | Yes | Yes | No | No |
| MCP: auto handshake (stdio) | Yes | No | No | Yes | No | No |
| MCP: OAuth/token exchange/ID-JAG | No | No | No | Yes Full | No | No |
| MCP stateless server mode | Partial (auto-handshake) | No | No | Yes | No | No |
| A2A task lifecycle | Full + SSE | No | No | Yes | No | No |
| Guardrails | Fallback/reroute, webhook retries, Rust-WASM accel | Basic | Advanced + retry/fallback | Regex + external | Built-in | Plugin |
| Guardrail: retry/reroute on provider error | No | No | Yes | No | No | No |
| Evals/experiments | Policy replay only | No | Full | No | No | No |
| Published benchmarks | Tool exists, no cross-gateway run | Blog | Blog | None | Blog (11us) | Blog |
| Provider count | 15+ presets + custom | 100+ | 1600+ | 10+ | 12+ | Major |
| Release engineering | goreleaser, GHCR, Homebrew | Docker | SaaS | Helm | Binary | Kong |
| HA control plane | Postgres only | Postgres | Managed | K8s CRDs | No | Kong |
| SSO/SAML/SCIM/mTLS | No | Enterprise | Yes | No | No | Yes |
| Helm chart | Yes | Helm | Managed | Helm | Binary | Yes |
| Evals/experiments | Policy replay only | No | Full | No | No | No |

## Top Remaining Gaps (by impact)
1. MCP OAuth client flows / token exchange / ID-JAG - agentgateway's moat
2. Evals/experiments - Portkey/TensorZero moat
3. Published cross-gateway benchmarks - Bifrost/LiteLLM marketing
4. Guardrail retry/reroute on provider errors - Portkey feature
5. A2A push notifications - agentgateway
6. MCP OAuth client flows - agentgateway enterprise