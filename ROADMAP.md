# Roadmap

Sentinel's goal is to become a local-first, open-source control plane for AI gateways and agent systems.

## Near Term

- Provider credential vault with encrypted-at-rest storage.
- Workspace enforcement middleware and SSO/SAML integration on top of the current organization/workspace/user data model.
- Richer MCP server registry with credential injection and per-tool access policies.
- OpenTelemetry trace export.
- Prompt rollback and playground workflows on top of the current prompt version API.
- Guardrail registry and configurable guardrail bundles.

## Medium Term

- Evaluation datasets and model-quality simulations on top of the current policy replay endpoint.
- Visual policy and routing editor.
- Provider SDK packages for Python and Node.js.
- More provider adapters, including Azure OpenAI, Mistral, Cohere, Together, AWS Bedrock, and local OpenAI-compatible providers.
- Embeddings, rerank, image, and audio endpoints.

## Long Term

- Agent identity registry.
- Multi-agent execution graph observability.
- Compliance evidence reports.
- Policy marketplace and signed policy bundles.

Roadmap items are not commitments. They are intended to help contributors find useful places to push.
