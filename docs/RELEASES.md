# Releases

This page documents OmniSwitch's versioning policy, how to verify a release, the release notes template maintainers use, and the rollback procedure.

## Versioning policy

OmniSwitch follows [SemVer](https://semver.org/) with beta-specific caveats while the project is pre-1.0:

- **`0.x` minor bumps may break configuration.** Until `v1.0.0`, a `0.x.0` minor release may require changes to your `GatewayConfig`. Patch releases (`0.x.y`) never do.
- **Betas are prereleases.** Beta builds are tagged `vX.Y.0-beta.N` (e.g. `v0.2.0-beta.1`) and published as GitHub prereleases. They are not picked up by `install.sh` unless you set `OMNISWITCH_VERSION` explicitly, and they do not move the `latest` container tag or the Homebrew formula.
- **`latest` and Homebrew track stable only.** The `ghcr.io/omniswitch-dev/omniswitch:latest` image tag and the `omniswitch-dev/tap` Homebrew formula only ever point at the most recent non-prerelease (stable) tag. To try a beta via Docker, pull the exact tag (e.g. `ghcr.io/omniswitch-dev/omniswitch:0.2.0-beta.1`).
- **Config `apiVersion` is additive-only during beta.** Fields may be added to `omniswitch.dev/v1` `GatewayConfig` during the beta, but existing fields will not be removed or renamed, and defaults will not change in a way that silently alters behavior. Any exception is called out explicitly in `CHANGELOG.md` and release notes.

## Verifying a release

Starting with v0.2.0, releases publish keyless cosign signatures for `checksums.txt` (`checksums.txt.sig` and `checksums.txt.pem`), a Syft SPDX JSON SBOM per archive, signed multi-arch image manifests, and GitHub build-provenance attestations for the archives, checksums, and image. Older releases may provide checksums only.

### Verify a download

Install [cosign](https://docs.sigstore.dev/cosign/system_config/installation/) and a recent [GitHub CLI](https://cli.github.com/). `gh` needs GitHub authentication to look up attestations. Download these files into an empty directory; change the tag and platform as needed:

```bash
set -euo pipefail
TAG=v0.2.0
ARCHIVE="omniswitch_${TAG#v}_linux_amd64.tar.gz"
BASE="https://github.com/omniswitch-dev/omniswitch/releases/download/${TAG}"
for FILE in "$ARCHIVE" checksums.txt checksums.txt.sig checksums.txt.pem; do
  curl -fsSLO "${BASE}/${FILE}"
done

cosign verify-blob checksums.txt \
  --signature checksums.txt.sig \
  --certificate checksums.txt.pem \
  --certificate-identity-regexp '^https://github\.com/omniswitch-dev/omniswitch/\.github/workflows/release\.yml@refs/tags/v.*$' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
sha256sum --check --ignore-missing checksums.txt

gh attestation verify "$ARCHIVE" --repo omniswitch-dev/omniswitch \
  --signer-workflow omniswitch-dev/omniswitch/.github/workflows/release.yml \
  --source-ref "refs/tags/${TAG}"
```

Archive names use the **version without the leading `v`**: `omniswitch_0.2.0_linux_amd64.tar.gz`, not `omniswitch_v0.2.0_linux_amd64.tar.gz`. Windows archives end in `.zip`. On macOS, replace the `sha256sum` command with:

```bash
awk -v file="$ARCHIVE" '$2 == file { print }' checksums.txt | shasum -a 256 --check
```

Do not extract or run the binary unless all checks succeed. The cosign check authenticates the checksum file's publisher, SHA-256 binds the archive to that signed file, and the attestation confirms the repository, workflow, and release ref that built it. SHA-256 alone detects corruption but does not authenticate the publisher.

`scripts/verify-install.sh` uses the same signature and SHA-256 checks before extracting a release when cosign is on `PATH`; a failed or missing signature is fatal. Without cosign it retains `install.sh`'s SHA-256-only verification. `OMNISWITCH_BIN` mode tests an already-built local binary and does not verify release signatures.

For the container image (authenticate to GHCR if your client requires it):

```bash
IMAGE="ghcr.io/omniswitch-dev/omniswitch:${TAG#v}"
cosign verify "$IMAGE" \
  --certificate-identity-regexp '^https://github\.com/omniswitch-dev/omniswitch/\.github/workflows/release\.yml@refs/tags/v.*$' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
gh attestation verify "oci://${IMAGE}" --repo omniswitch-dev/omniswitch \
  --bundle-from-oci \
  --signer-workflow omniswitch-dev/omniswitch/.github/workflows/release.yml \
  --source-ref "refs/tags/${TAG}"
```

Pin the verified `sha256:` manifest digest rather than `latest` in production. Each archive's SBOM is named `<archive>.sbom.spdx.json` and is covered by the signed checksums.

## Release notes template

Maintainers use this structure for every tagged release (see past releases for examples):

```markdown
## Highlights
- 2-4 bullets on what's new or fixed and why it matters.

## Install
- Binary: `curl -fsSL https://raw.githubusercontent.com/omniswitch-dev/omniswitch/main/install.sh | sh`
  (for a beta: `OMNISWITCH_VERSION=<tag> curl -fsSL .../install.sh | sh`)
- Docker: `docker run -p 8080:8080 -e OPENAI_API_KEY=... ghcr.io/omniswitch-dev/omniswitch:<tag>`
- Homebrew (stable releases only): `brew install omniswitch-dev/tap/omniswitch`

## Verification
- `omniswitch version` should print `<tag>`.
- `sha256sum -c checksums.txt` (see "Verifying a release" above).

## Known limitations
See [`docs/KNOWN_LIMITATIONS.md`](KNOWN_LIMITATIONS.md).

## Upgrade notes
- Config or behavior changes required to upgrade from the previous release, if any.
```

## Rollback procedure

If a release turns out to be broken:

1. **Mark the GitHub release as a prerelease or draft it out** so `install.sh` (without an explicit `OMNISWITCH_VERSION`) and the `releases/latest` API stop resolving to it.
2. **Do not move `latest`/Homebrew forward for that tag.** Because `latest` and the Homebrew formula only advance on confirmed-good stable releases, a broken tag that is promptly unmarked never reaches users who didn't pin a version.
3. **Re-point Homebrew, if it already advanced,** by re-running the tap update against the last known-good tag (or manually editing the formula in `omniswitch-dev/homebrew-tap` as a stopgap).
4. **Open a `beta-blocker`-labeled issue** describing the break and link it from the release notes of the broken tag.
5. **Ship a fixed patch/beta release** (e.g. `v0.2.0-beta.2`) rather than deleting the broken tag outright — deleting tags breaks anyone who already pinned to it and hides the history of what happened.
6. **Communicate** in GitHub Discussions → Announcements if the break affected anyone who already upgraded.
