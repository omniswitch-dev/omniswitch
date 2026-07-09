# Changelog

All notable changes to Sentinel will be documented in this file.

The format is based on Keep a Changelog, and this project follows semantic versioning once tagged releases begin.

## [Unreleased]

### Added

- OpenAI-compatible AI gateway with streaming chat completions.
- Provider adapters for OpenAI, Anthropic, Google Gemini, and Groq.
- Provider catalog aliases such as `@openai-prod/gpt-4o-mini`.
- Exact and semantic caching backed by SQLite.
- Config-as-code with `sentinel.dev/v1` `GatewayConfig`.
- Circuit breakers, retries, fallbacks, A/B routing, and shadow routing.
- Cost and token budgets for API keys.
- Feedback API tied to `trace_id` and `request_id`.
- OpenAI-style multimodal message content compatibility.
- MCP policy proxy mounted in the gateway.
- CEL-backed policy engine and Git-like policy CLI.
- Local dashboard for logs, metrics, prompts, providers, and guardrails.

### Security

- Apache-2.0 license, security policy, and secret-handling guidance.
