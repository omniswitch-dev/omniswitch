# Roadmap

OmniSwitch's goal is to become a local-first, open-source control plane for AI gateways and agent systems.

## Near Term

- Shared storage or external control-plane support for horizontally scaled key, log, budget, and config state.
- SAML/mTLS identity options on top of the current API key and OIDC/JWKS model.
- OpenAPI-to-MCP conversion and richer MCP OAuth client flows.
- Prompt rollback and playground workflows on top of the current prompt version API.
- Guardrail retry, reroute, and fallback actions after policy violations.
- Dashboard trace waterfall, cost charts, and eval report views.

## Medium Term

- Evaluation datasets and model-quality simulations on top of the current policy replay endpoint.
- Visual policy and routing editor.
- First-class provider adapters for non-OpenAI-compatible APIs, including AWS Bedrock and Cohere.
- Image, audio, batch, and file endpoints.
- Fuller A2A task lifecycle, streaming, push notifications, and outbound agent calls.

## Long Term

- Agent identity registry.
- Multi-agent execution graph observability.
- Compliance evidence reports.
- Policy marketplace and signed policy bundles.

Roadmap items are not commitments. They are intended to help contributors find useful places to push.
