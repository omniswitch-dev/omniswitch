# Product and adoption review — 2026-10-30

Scope: OmniSwitch core repository at base commit `5df77bb`, with the changes
in this branch. This is a focused code and documentation review, not an
exhaustive feature certification, performance benchmark, or market validation.
No customer interviews, acquisition analytics, or conversion data were available.
A claim of zero users does not establish why people have not adopted it.

## What the competitors establish

Official sources checked on 2026-10-30. Product tiers and development branches
can change; hosted and enterprise capabilities are not automatically OSS features.

| Product | Relevant documented capability | Implication for OmniSwitch |
| --- | --- | --- |
| [AgentGateway](https://agentgateway.dev/docs/standalone/latest/documentation/configuration/overview/) | Standalone/Kubernetes configuration, LLM and MCP policies, identity, a LiteLLM migration path | Make the first working setup and migration understandable; do not compete only by counting proxy features |
| [LiteLLM](https://docs.litellm.ai/docs/) and [virtual keys](https://docs.litellm.ai/docs/proxy/virtual_keys) | Unified provider API, routing/fallbacks, virtual keys, spend tracking, admin UI; advanced identity is a separate enterprise surface | Reliable client compatibility and usable governance are baseline evaluation criteria |
| [Portkey OSS gateway](https://github.com/Portkey-AI/gateway/blob/main/README.md) | Fallbacks, retries, load balancing, timeouts, multimodal routing; hosted observability/prompt features are distinguished | Demonstrate a real failure/recovery and tool loop; avoid equating an API with a complete hosted workflow |
| [Kong AI Gateway](https://developer.konghq.com/index/ai-gateway/) | Provider routing, governance, streaming, transformations and enterprise operations | Clearly state supported deployment scale and protocol subsets; do not claim enterprise parity |

## Code findings and delivered work

OmniSwitch already has provider adapters/presets, fallback/retry routing, circuit
breakers, cache, key budgets, guardrails, OIDC, MCP, traces, a dashboard and a chat
playground. Duplicating these would add surface area without resolving an
observed gap. The immediate integration blockers were narrower:

| Before | Change in this branch | Evidence |
| --- | --- | --- |
| Responses rejected function results, preventing a tool loop | Accept full stateless `function_call` and string `function_call_output` history, including parallel IDs | `internal/gateway/compat_roundtrip_test.go` |
| Messages ignored tool definitions/history, lost non-streaming tool calls, and rejected SDK-native key authentication | Translate custom schemas, choices, system/text/image blocks, assistant tool uses and user results; preserve tool-use responses; accept gateway keys in `x-api-key` on this endpoint | Same HTTP-adapter round-trip tests, streaming translator tests |
| Responses silently ignored structured output/state controls | Translate `text.format`; reject unsupported stored/background requests with useful 400 errors | Controls and rejection tests |
| Responses text streams omitted final output/item lifecycle data | Emit item/content events and assemble final output with sequence numbers | `internal/gateway/compat_stream_test.go` |
| A failed first run required manually checking several endpoints | Add `omniswitch doctor`, with fixes and JSON output, without paid inference requests | `cmd/gateway/doctor_test.go` |
| README started the process but did not show an initial chat request | Add setup check, first request, and standard SDK tool-loop/stream examples | `README.md`, `docs/AGENT_WORKFLOWS.md` |

Protocol decisions follow [OpenAI function calling](https://developers.openai.com/api/docs/guides/function-calling)
and [Anthropic custom tools](https://platform.claude.com/docs/en/agents-and-tools/tool-use/define-tools).
This is custom-tool compatibility, not full vendor/API parity. See
[known limitations](../KNOWN_LIMITATIONS.md).

## Remaining gaps, ordered by likely practical value

These are recommendations from the review, not validated customer demand or
features delivered by this branch.

| Priority | Remaining work | Acceptance evidence before claiming it works |
| --- | --- | --- |
| Next | Guided migration from LiteLLM/Portkey with explicit unsupported-setting warnings | Import representative configs; no secrets in generated files; verify aliases/fallbacks and all ignored fields |
| Next | Per-provider key permissions across every dispatch | Tests covering direct calls, variants, retries, fallbacks, guardrail reroutes and shadow traffic |
| Next | Per-minute token quotas with reservations and reconciliation | Concurrent and streamed requests cannot evade limits; document local versus distributed behavior |
| Next | Prompt label promotion/rollback UI and model comparison | Workspace-isolated editing, deployment and rollback through existing prompt APIs |
| Later | Embedding-based semantic cache | Measure false hits on an evaluation set; isolate tenants, media, tool state and model settings |
| Later | Full Responses state and broader Messages semantics | Stateful/tool/stream fixtures, retention/deletion, isolation, cancellation and restart behavior |
| Later | Production multi-writer PostgreSQL and distributed budgets | Multi-replica concurrency/failure tests and migration/recovery tooling |
| Demand-led | SAML/SCIM, mTLS, cloud identity integrations, controller/CRDs, image/audio/batch endpoints | A target user's concrete requirement plus conformance and operational tests |

The existing roadmap's enterprise deferrals remain visible. Adding their names
to a settings page would not constitute implementing them.

## A measurable path to the first users

Recommended initial audience: developers or small teams who want a self-hosted
LLM/MCP gateway with a single Go binary, SQLite defaults, visible policy traces,
and existing SDKs. This is a positioning hypothesis to test, not a claim that
competitors cannot serve those users.

1. **Week 1: Observe five fresh installs.** Ask five developers running an
   OpenAI-compatible app to try the quickstart without explanation. Record time
   to first successful call, where they stop, and whether the doctor output
   resolves the failure. Aim for a first call within five minutes; record actual
   results rather than advertising that target as a measured fact.
2. **Week 2: Help three real integrations.** Start with one concrete problem per
   developer: provider failover, a tool loop, or a per-key spending limit. Gather
   reproducible failures and fix those before widening the feature list.
3. **Week 3: Publish one reproducible walkthrough.** Show setup, a successful
   tool loop, a simulated provider failure/fallback, and its trace. Use the
   existing benchmark harness for performance claims with pinned versions.
4. **Week 4: Check repeat use.** Ask which integrations remain active after
   seven days and what still requires manual work. Prioritize the next release
   from that evidence. No outreach or publishing has been performed here.

Track four simple counts manually: attempted installs, first successful calls,
real app integrations, and integrations still in use seven days later. If few
people attempt installation, investigate positioning/distribution. If attempts
fail, investigate onboarding/compatibility. If integrations stop being used,
investigate recurring value and reliability. Feature parity alone cannot
promise users or retention.
