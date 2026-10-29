# Known Limitations

OmniSwitch is in public beta. This page lists limitations that are current, known, and intentional (either not yet built, or a deliberate scope decision) so you can decide whether they matter for your use case. If you hit something not listed here, please [file a bug](https://github.com/omniswitch-dev/omniswitch/issues/new/choose) — it may be a real defect rather than a documented limitation.

## Control plane storage

- The default and fully supported storage is **single-replica SQLite**. Running more than one gateway replica against the same SQLite file is not supported.
- A **PostgreSQL storage driver exists but is experimental**: it is not yet the multi-writer, horizontally-scaled control plane described in the roadmap. Treat it as suitable for evaluation, not as a production multi-replica setup, until it graduates out of experimental status.

## API surface

- **Responses API and Anthropic Messages API support a documented subset**, not full parity with the upstream vendor APIs (see `CHANGELOG.md` for the passthrough-fidelity work).
  - `/v1/responses` accepts message input items with text and image content, function tools and `tool_choice`. It returns and streams text and function calls. It rejects built-in tools (for example `web_search`), input items other than messages, and audio or file content with HTTP 400. Because it rejects `function_call_output` items, a client cannot send tool results back through `/v1/responses`. It ignores other fields, for example `previous_response_id`, `store`, `background`, `text.format` and `reasoning`: there is no stored response state, background mode or structured output.
  - `/v1/messages` accepts `model`, `messages`, `system` (a string), `max_tokens`, `temperature`, `top_p` and `stream`. It ignores client `tools`.
- **A2A (Agent-to-Agent) support covers the v1 task lifecycle** (create, poll, cancel) with single-shot SSE: a streaming request gets status and artifact frames for one completed response, not incremental tokens. There is no `tasks/resubscribe`. Tasks are kept in memory (at most 10,000 in total and 1,000 per API key and workspace, for one hour), so they are lost on restart and are not shared between replicas. Push notifications and an outbound A2A client (OmniSwitch acting as an A2A caller rather than callee) are not implemented.
- **MCP token exchange covers RFC 8693 only.** `mcp.targets[].token_exchange` exchanges the OIDC caller's token for HTTP targets. There is no RFC 7523 JWT bearer grant, no client-credentials flow for API-key callers, and no exchange for OpenAPI targets or stdio targets.
- **Non-default streamable-HTTP MCP targets** that require their own MCP session are not initialized by the gateway. The client's `Mcp-Session-Id` is forwarded only to the first target.

## Identity and transport security

- **No mTLS.** Client/server mutual TLS is not implemented; use a reverse proxy or service mesh in front of OmniSwitch if you need it.
- **No SAML/SCIM.** Identity is API-key based or OIDC/JWKS-based; there is no SAML SSO or SCIM provisioning integration.

## Guardrails and streaming

- **`guardrails.stream_buffer` delays the first streamed token by default.** When guardrails are enabled on a streaming response, OmniSwitch buffers output to scan it before forwarding, which adds latency to time-to-first-token compared to guardrails-off streaming. This is a deliberate safety/latency tradeoff; disable stream buffering only if you understand the guardrail is then scanning already-sent output.

## Caching

- **Semantic cache is token-frequency cosine similarity, not embeddings.** OmniSwitch's semantic cache matches requests using cosine similarity over role- and order-aware token and bigram vectors of the conversation — it does not call an embedding model. Prompts with images, audio, other non-text parts or tool calls match only prompts with exactly the same media and tool calls. This is cheaper and dependency-free, but it is a weaker notion of "semantic" than a vector-embedding-based cache: it will miss paraphrases that an embedding model would catch, and can occasionally match on superficial token overlap rather than true meaning. Treat it as an exact-ish cache with some tolerance for whitespace/formatting differences, not as embedding-quality semantic matching.

## Authorization

- **`authorization.external` is not implemented.** If you set this option in `GatewayConfig`, config validation now fails explicitly with a "not implemented" error instead of silently accepting the setting and doing nothing (see the sibling first-run/config-truth work tracked in `ROADMAP.md`). Use the built-in API key or OIDC/JWKS + CEL policy authorization instead.

## Reporting something not listed here

If you find a gap not covered above, it's either a bug (please file one) or something we haven't documented yet (please file a docs issue) — either way, we'd rather hear about it than have it stay a silent surprise.
