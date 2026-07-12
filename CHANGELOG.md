# Changelog

All notable changes to Sentinel will be documented in this file.

The format is based on Keep a Changelog, and this project follows semantic versioning once tagged releases begin.

## [Unreleased]

### Added

- OpenAI-compatible AI gateway with streaming chat completions.
- Provider adapters for OpenAI, Anthropic, Google Gemini, and Groq.
- Generic OpenAI-compatible custom provider support for Azure OpenAI, Ollama, vLLM, LiteLLM, DeepSeek, Together, Fireworks, and similar endpoints.
- Provider catalog aliases such as `@openai-prod/gpt-4o-mini`.
- Encrypted provider credential vault with virtual providers, rotation, and revoke workflows.
- Exact and semantic caching backed by SQLite.
- Config-as-code with `sentinel.dev/v1` `GatewayConfig`.
- OpenTelemetry trace export for gateway and provider spans.
- Circuit breakers, retries, fallbacks, A/B routing, and shadow routing.
- Cost and token budgets for API keys.
- Feedback API tied to `trace_id` and `request_id`.
- Raw request/response bodies in capped observability logs.
- Organization, workspace, user, membership, role, and workspace-scoped API key foundations.
- Prompt version history for immutable prompt revisions.
- Policy replay eval endpoint for batch allow/deny simulation.
- OpenAI-style multimodal message content compatibility.
- MCP policy proxy mounted in the gateway.
- CEL-backed policy engine and Git-like policy CLI.
- Local dashboard for logs, metrics, prompts, providers, and guardrails.
- Python and Node.js SDK wrappers for the OpenAI-compatible gateway.

### Security

- Apache-2.0 license, security policy, and secret-handling guidance.
