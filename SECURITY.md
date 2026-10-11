# Security Policy

OmniSwitch is security-sensitive software: it proxies model traffic, holds provider credentials, and can gate agent tool calls (MCP) with real side effects. Please help us keep it safe to run.

## Supported Versions

OmniSwitch is in public beta. Support during the beta follows this table and will be tightened once `v1.0.0` ships.

| Version | Supported |
| --- | --- |
| `0.2.x` (beta) | Yes, full support |
| `0.1.0` | Critical fixes only |
| `main` (unreleased) | Best effort |
| Anything older | No |

## Reporting a Vulnerability

**Please do not open a public issue for suspected vulnerabilities.**

Preferred: use [GitHub private vulnerability reporting](https://github.com/omniswitch-dev/omniswitch/security/advisories/new) on this repository (Security tab → "Report a vulnerability"). This opens a private advisory only the maintainers and you can see.

If private reporting is unavailable to you, email **security@omniswitch.dev** instead. Please use a descriptive subject line and, if possible, PGP-encrypt anything containing exploit details or credentials.

Please include:

- A description of the issue and its impact.
- Reproduction steps or a proof of concept.
- Affected version(s) or commit SHA.
- Any suggested remediation.

### What to expect

| Milestone | Target |
| --- | --- |
| Acknowledgment | Within 3 business days |
| Triage and severity assessment | Within 7 days |
| Fix target for High/Critical severity | Within 30 days |
| Fix target for Medium/Low severity | Best effort, scheduled into a normal release |

We practice coordinated disclosure: we will work with you on a disclosure timeline, credit you in the advisory and release notes (unless you prefer to stay anonymous), and avoid disclosing details publicly until a fix is available.

## Hardening Guidance

Before installing a release, [verify the download's signature and build provenance](docs/RELEASES.md#verify-a-download). Checksums alone do not authenticate the publisher.

Before running OmniSwitch against production traffic, read the [Production Notes](docs/DEPLOYMENT.md#production-notes) in `docs/DEPLOYMENT.md` for authentication, network exposure, and secret-handling guidance.

## Out of Scope

The following are known, documented configuration choices, not vulnerabilities. Please don't file security reports for these unless you found a way to trigger them without the operator opting in:

- Running with `auth: false` in `GatewayConfig` (documented as development-only; production deployments must enable API key auth).
- Setting `cache_scope: global` (intentionally shares cache entries across tenants/API keys — only appropriate for single-tenant deployments).
- Setting `log_payloads: true` in observability config (intentionally logs request/response bodies, which may contain sensitive data; this is an explicit opt-in for debugging).
- Vulnerabilities that require an attacker to already have direct file-system or process access to the host running OmniSwitch.
- Denial-of-service reports that rely on sending unlimited unauthenticated traffic to a deployment the operator chose to expose without rate limiting.

## Secret Handling

Do not commit provider API keys, OmniSwitch API keys, `.env` files, SQLite data files, decision traces containing secrets, or production policy files with confidential details.

Provider account config should reference environment variables via `api_key_env` rather than embedding secrets directly.
