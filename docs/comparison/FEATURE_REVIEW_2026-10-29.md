# Competitive feature review — 2026-10-29

This review compares specific documented capabilities with OmniSwitch's code
at base commit `9d43201`. It prioritizes practical additions that fit the
local-first gateway. Documentation was checked on 2026-10-29; “latest” and
development-branch documentation can change. This is not a benchmark, a full
product ranking, or a claim of feature/wire-protocol parity. Hosted product
features are identified separately from OSS gateway implementations.

## Findings and implementation choices

| Project / source | Documented capability relevant to this review | OmniSwitch before this change | Outcome |
| --- | --- | --- | --- |
| [LiteLLM virtual keys](https://docs.litellm.ai/docs/proxy/virtual_keys) | Per-key model access and spend controls | Key lifecycle, quotas, and monthly budgets existed; no first-class model list on keys | Added persistent exact-name `allowed_models` on API keys, enforcement, and filtered discovery |
| [agentgateway external authorization](https://agentgateway.dev/docs/standalone/latest/documentation/configuration/security/external-authz/) | External authorization policy callouts | `authorization.external` was rejected as unimplemented | Added a bounded, fail-closed HTTP JSON callout after local authorization; this is OmniSwitch's protocol, not Envoy ext_authz compatibility |
| [agentgateway AI policies](https://agentgateway.dev/docs/standalone/latest/documentation/configuration/traffic-management/llm/) | Request defaults/overrides, model aliases, prompt enrichment | Defaults, overrides, drops, variants, and alias-backed routes already existed; prepend/append messages did not | Added text prompt enrichment before guardrails and caching; aliases now appear in model discovery |
| [Bifrost virtual keys (OSS development docs)](https://github.com/maximhq/bifrost/blob/dev/docs/features/governance/virtual-keys.mdx) | Model/provider filtering, budgets, request/token rate limits; enterprise IdP features are separately marked | Monthly budgets and request quotas existed; no key model list | Added model restrictions. Provider allowlists and per-window token rate limits remain open; no enterprise identity parity claim |
| [Portkey prompt versioning and labels (product/hosted docs)](https://portkey.ai/docs/product/prompt-engineering-studio/prompt-versioning) | Separate prompt editing from publishing, deployment labels, rollback | Immutable prompt versions and rendering by ID existed; deployment pointers did not | Added persistent label promotion/rollback and render-by-label APIs; no deployment UI or hosted product parity claim |
| [Kong AI Prompt Decorator](https://developer.konghq.com/plugins/ai-prompt-decorator/) | Prepend/append chat messages at the gateway | No route-level message enrichment | Added `prepend_messages` / `append_messages` for chat and compatible API pipelines; no Kong plugin dependency |
| [TensorZero experimentation](https://www.tensorzero.com/docs/experimentation/run-static-ab-tests) | Route inference between prompt/model variants for experiments | Weighted/conditional variants, feedback, datasets, and policy replay already existed | Retained existing routing; statistical experiment analysis and adaptive quality optimization remain separate work |
| [Helicone OSS gateway](https://github.com/Helicone/ai-gateway) | Rate limits using requests/tokens/cost, caching, tracing | Request-count quotas, monthly token/cost budgets, caching, and tracing existed | No duplicated request limiter or cache; shorter-window token quotas and distributed budget reservations remain future work |
| [Apache APISIX prompt decorator](https://apisix.apache.org/docs/apisix/plugins/ai-prompt-decorator/) | Central prompt prefix/suffix configuration | Same enrichment gap as Kong/agentgateway | Covered by the new route-level text message enrichment |

## Delivered behavior and verification

1. **Model-scoped API keys.** `POST /api/keys` accepts `allowed_models` with up
   to 256 exact client-facing IDs. Omitted/empty lists preserve old behavior.
   Schema migration keeps existing keys unrestricted. Inference admission
   checks use authenticated identity, not caller-provided restriction headers.
   `/v1/models` filters permitted models and includes configured logical routes.
   The shared chat pipeline also checks access for compatibility/A2A calls.
   Tests cover persistence, migration, corrupt stored policy, inference endpoint
   admission, and filtered discovery. This does not restrict provider or model
   destinations behind trusted operator-defined routes.

2. **External policy decisions.** Authenticated requests that pass key-model,
   CEL, and role checks can be sent to an HTTP JSON policy endpoint. The
   payload contains request method/path/model and selected identity fields;
   credentials, prompts, query strings, arbitrary headers, and JWT claims are
   excluded. Only a 2xx explicit `allowed` boolean is accepted. Denials return
   403; service failures, malformed/oversized responses, and redirects return
   503. Tests cover privacy/body preservation, denial/failure cases, cancellation,
   deadlines, and local-denial precedence. Configuration changes need restart.

3. **Prompt deployment labels.** `POST /api/prompts/labels` atomically moves
   an environment label to an immutable version; moving it back is rollback.
   `GET` lists labels, and `/api/prompts/render` accepts `name` + `label` as an
   alternative to `prompt_id`. Tests cover promotion, rollback, restart
   persistence, validation, cross-prompt assignment rejection, and unchanged
   history. Template values are substituted once and remain literal. Labels
   use the existing shared prompt registry; they are not tenant isolation.

4. **Prompt enrichment.** Routes prepend/append text messages in configured
   order after parameter shaping. The shared chat pipeline applies it before
   input guardrails and cache lookup, including compatible streaming requests.
   Tests cover order, request isolation, config validation, route replacement,
   guardrail inspection, and cache separation. Existing provider role support
   still governs which upstreams accept a particular message role.

## Remaining priorities

| Gap | Why it needs a separate implementation |
| --- | --- |
| Per-minute token quotas | Define streaming usage normalization, reservations/reconciliation, and shared backend semantics before exposing limits |
| Provider-scoped key permissions | Enforce every actual dispatch, including variants, fallback, guardrail reroutes, and asynchronous shadow traffic |
| Complete Responses/Messages tool parity | Requires protocol fixtures for tool events, multimodal payloads, and streaming translation; current compatibility remains a documented subset |
| Prompt playground and tenant isolation | Labels do not change shared prompt ownership; add a deliberate workspace model and role-scoped UI |
| Adaptive experiments / quality evaluation | Existing weighted routing and policy replay do not replace statistical quality evaluation |
| Multi-writer control plane | PostgreSQL remains experimental; distributed budget and config consistency need dedicated concurrency/migration work |

See [API.md](../API.md), [CONFIGURATION.md](../CONFIGURATION.md), and
[KNOWN_LIMITATIONS.md](../KNOWN_LIMITATIONS.md) for the supported contract.
The older generated [capability register](COMPARISON.md) is a separate,
versioned snapshot and is not silently re-dated by this focused review.
