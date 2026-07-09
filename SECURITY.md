# Security Policy

Sentinel is security-sensitive software because it can proxy model traffic, provider credentials, and agent tool calls.

## Supported Versions

Security fixes are provided for the latest version on the default branch until versioned releases are established.

## Reporting a Vulnerability

Please do not open a public issue for suspected vulnerabilities.

Use GitHub Security Advisories for private disclosure, or contact the maintainers through the repository owner account if advisories are not yet enabled.

Please include:

- A description of the issue.
- Reproduction steps or proof of concept.
- Impact and affected versions or commits.
- Any suggested remediation.

## Secret Handling

Do not commit provider API keys, Sentinel API keys, `.env` files, SQLite data files, decision traces containing secrets, or production policy files with confidential details.

Provider account config should reference environment variables with `api_key_env` rather than embedding secrets directly.
