# Support

OmniSwitch is community-supported during the public beta. There is no paid support tier yet.

## Where to ask

- **Usage questions, "is this expected?", configuration help:** [GitHub Discussions → Q&A](https://github.com/omniswitch-dev/omniswitch/discussions/categories/q-a). This is the primary support channel.
- **Bugs (something concrete and reproducible is broken):** [open an issue](https://github.com/omniswitch-dev/omniswitch/issues/new/choose) using the Bug report template.
- **Beta feedback (install friction, rough edges, "here's what happened when I tried it"):** use the [Beta feedback issue form](https://github.com/omniswitch-dev/omniswitch/issues/new/choose).
- **Feature ideas:** [GitHub Discussions → Ideas](https://github.com/omniswitch-dev/omniswitch/discussions/categories/ideas).
- **Security vulnerabilities:** see [`SECURITY.md`](SECURITY.md). Do not use Discussions or public issues.

## What to include

To get a useful answer quickly, include:

- Your install method (binary, Docker, Homebrew, Kubernetes) and OS/architecture.
- `omniswitch version` output.
- The relevant part of your `GatewayConfig` (redact API keys and secrets).
- What you expected vs. what happened, including exact error messages or logs.

## Response expectations during beta

- First response to issues and Discussions: target under 24 hours on weekdays.
- Issues labeled `beta-blocker` (broken install path, crash on start, data loss, credential leak): fixed or a workaround posted within 72 hours.
- Everything else: triaged and labeled within 72 hours; fix timing depends on severity and maintainer availability.

These are best-effort targets from a small maintainer team, not a contractual SLA.

## What is not supported

- Deployments on unsupported/EOL operating systems or architectures not listed in the [install matrix](docs/RELEASES.md).
- Forked or patched builds of OmniSwitch.
- Custom code built on internal (non-exported) packages; only documented config and the OpenAI-compatible API surface are considered stable during beta.
- General LLM/provider account issues (rate limits, billing, model availability) that originate with the upstream provider, not OmniSwitch.
