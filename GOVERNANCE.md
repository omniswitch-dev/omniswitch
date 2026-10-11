# Governance

## Model: BDFL-with-maintainers (beta phase)

For the public beta, OmniSwitch is governed as a Benevolent-Dictator-For-Life-with-maintainers project: the project founder (see [`MAINTAINERS.md`](MAINTAINERS.md)) has final say on direction and disputes, but day-to-day decisions are delegated to maintainers, and most decisions are made by lazy consensus. This is a deliberately lightweight model for a small, fast-moving beta; it will be revisited (likely toward a maintainer-committee model) once the maintainer group grows past the founder.

## How decisions are made

- **Pull requests:** decided by lazy consensus. If a maintainer with relevant area ownership (see [`CODEOWNERS`](CODEOWNERS)) hasn't objected within a reasonable review window and CI is green, the PR can be merged. Any maintainer may request more time for review or escalate to the founder for a tie-break.
- **Roadmap and scope:** maintainers collectively decide what goes on [`ROADMAP.md`](ROADMAP.md) and its priority. Community input happens via GitHub Discussions → Ideas and the pinned roadmap-voting thread; maintainers weigh it but are not bound by vote counts.
- **Breaking changes** (config schema, CLI flags, wire-format changes): require explicit sign-off from at least one maintainer who owns the affected area, and must be called out in `CHANGELOG.md` and, if user-facing, `docs/KNOWN_LIMITATIONS.md` or release notes.
- **Disputes** that can't be resolved by discussion escalate to the founder, whose decision is final.

## Becoming a maintainer

There's no fixed contribution count, but the general path is:

1. Make several substantive, merged contributions (code, docs, triage, or review) over time — sustained engagement matters more than volume.
2. Show good judgment reviewing others' PRs and issues: catching real bugs, giving actionable feedback, respecting scope.
3. Be nominated by an existing maintainer (or self-nominate in a Discussions thread) and get lazy-consensus approval from current maintainers.
4. New maintainers are added to [`MAINTAINERS.md`](MAINTAINERS.md) and `CODEOWNERS` for the area(s) they own, and get merge rights scoped to those areas.

Maintainers can step back or be marked inactive at any time; this is a low-pressure, volunteer-friendly project.

## Code of Conduct enforcement

The [Code of Conduct](CODE_OF_CONDUCT.md) applies to all project spaces (issues, PRs, Discussions, and any official community channels). Reports should go to the maintainer(s) listed in `MAINTAINERS.md` via a private channel (GitHub private vulnerability reporting is for security only — use direct contact or a private message for conduct reports). Maintainers review and act on reports promptly and will keep the reporter's identity confidential except where legally required otherwise.
