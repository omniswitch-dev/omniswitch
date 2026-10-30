# Publishing the one-command install

The `omniswitch` Python distribution now packages the gateway, rather than only
an SDK wrapper. The five platform wheels contain the stripped Go binary and
standard launcher. They are tagged `py3-none-<platform>` and require Python 3.9+:

| Target | Wheel platform | Minimum |
| --- | --- | --- |
| Linux x86-64 | `manylinux_2_17_x86_64` | 64-bit Linux; static Go build |
| Linux ARM64 | `manylinux_2_17_aarch64` | ARM64 Linux; static Go build |
| macOS Intel | `macosx_12_0_x86_64` | macOS 12 |
| macOS Apple Silicon | `macosx_12_0_arm64` | macOS 12 |
| Windows x86-64 | `win_amd64` | Windows 10+ |

Go is built with `CGO_ENABLED=0`. The OS minimums follow the pinned Go toolchain;
update the wheel platform tags when changing the toolchain's requirements.
No universal binary wheel or source distribution is published as a fallback for
unsupported platforms. Source installs remain useful for SDK development; they
do not contain the gateway executable.

## One-time owner setup

A public package is **not live merely because these files exist**. An owner
must configure a pending/normal PyPI Trusted Publisher for:

| PyPI publisher field | Value |
| --- | --- |
| Project name | `omniswitch` |
| GitHub owner | `omniswitch-dev` |
| Repository | `omniswitch` |
| Workflow filename | `python-package.yml` |
| Environment | `pypi` |

Use the authenticated PyPI account that should own the project. Verify project
name availability; this implementation does not reserve the name or create a
PyPI account. Configure the matching `pypi` GitHub environment under the
repository's normal release policy. Do not add a PyPI token to source files or
ask users to paste one into the gateway.

Official instructions:
[create a pending publisher](https://docs.pypi.org/trusted-publishers/creating-a-project-through-oidc/),
[configure an existing project](https://docs.pypi.org/trusted-publishers/adding-a-publisher/),
and [publish through GitHub Actions](https://docs.pypi.org/trusted-publishers/using-a-publisher/).

## Build, test, publish

1. Merge the reviewed implementation after CI succeeds. The Python package
   workflow runs native wheel install tests for all five platforms. Pull-request,
   main-branch and manual runs only build/test artifacts; they do not publish.
2. Publish a GitHub Release with a new valid version tag. The wheel builder
   normalizes it using `packaging.version.Version`, for example
   `v0.2.0-beta.1` becomes `0.2.0b1`.
3. `.github/workflows/python-package.yml` builds from that release source,
   verifies metadata and fresh installation on native runners, then publishes
   only when all matrix jobs pass. The publish job uses the `pypi` environment
   and scoped `id-token: write`; PyPI exchanges its GitHub identity for a
   short-lived publishing credential. Attestations are enabled.
4. Verify the public project belongs to this repository and that a fresh
   `pip install omniswitch` (or an explicit prerelease version) runs the guided
   flow. Update the publication-status notices in README/onboarding/package
   docs only after that succeeds.

The local development version `0.2.0.dev0` is not published by PR or manual
workflow runs. PyPI disallows reusing a version's existing wheel filenames, so
publish a new version for fixes. The GoReleaser binary/image release workflow
remains separate and unchanged by this packaging pipeline.

## Reproduce locally

```bash
python -m pip install build 'setuptools>=77' wheel packaging twine
python scripts/build-python-wheels.py --target all --version 0.2.0.dev0
python -m twine check dist/python/*.whl
```

On a native platform, run `scripts/verify-python-wheel.py` against its wheel.
It creates a fresh virtual environment, installs with `--no-index` and no Go executable on its runtime PATH,
checks launcher/module entrypoints without provider SDKs, completes the guided
setup, sends simulated requests, verifies app-key restrictions, runs doctor,
and restarts the saved profile. It does not contact PyPI or real providers.
