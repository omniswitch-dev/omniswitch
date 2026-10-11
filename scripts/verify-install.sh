#!/usr/bin/env bash
# Verifies that an OmniSwitch binary can be installed (or is already built),
# started with just an API key, and answers health/readiness/models requests.
#
# Two modes:
#   1. Local development: set OMNISWITCH_BIN to a binary you already built
#      (e.g. `go build -o /tmp/omniswitch-dev ./cmd/gateway`) and this script
#      uses it directly, skipping install.sh entirely.
#         OMNISWITCH_BIN=/tmp/omniswitch-dev scripts/verify-install.sh
#   2. CI / release verification: leave OMNISWITCH_BIN unset. The script runs
#      install.sh (honoring OMNISWITCH_VERSION) into a temporary directory and
#      verifies the resulting binary. When cosign is installed, it downloads
#      and authenticates the archive before extracting it instead.
#         OMNISWITCH_VERSION=v0.2.0-beta.1 scripts/verify-install.sh
#
# Env vars:
#   OMNISWITCH_BIN       Path to an already-built binary. If set, install.sh
#                        is not run.
#   OMNISWITCH_VERSION   Version/tag passed through to install.sh when
#                        OMNISWITCH_BIN is not set. Required for prereleases
#                        (install.sh's default "latest" lookup skips them).
#   OMNISWITCH_PORT      Port to listen on for the verification run.
#                        Default: 18080 (deliberately not 8080, to avoid
#                        colliding with anything else running locally).
#   OMNISWITCH_START_TIMEOUT  Seconds to wait for /healthz to come up.
#                        Default: 30.
#
# Exit code is non-zero if any step fails, including a missing `version`
# subcommand or a non-200 from /healthz, /readyz, or /v1/models -- by design,
# running this against an OmniSwitch build that predates those endpoints
# fails loudly rather than silently skipping the check.
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"

PORT="${OMNISWITCH_PORT:-18080}"
START_TIMEOUT="${OMNISWITCH_START_TIMEOUT:-30}"
BASE_URL="http://127.0.0.1:${PORT}"

WORKDIR=""
SERVER_PID=""
SERVER_LOG=""
FAILED=0

cleanup() {
    if [ -n "${SERVER_PID}" ] && kill -0 "${SERVER_PID}" 2>/dev/null; then
        echo "==> Stopping server (pid ${SERVER_PID})"
        kill "${SERVER_PID}" 2>/dev/null || true
        for _ in $(seq 1 10); do
            kill -0 "${SERVER_PID}" 2>/dev/null || break
            sleep 0.5
        done
        kill -9 "${SERVER_PID}" 2>/dev/null || true
    fi
    if [ -n "${WORKDIR}" ] && [ -d "${WORKDIR}" ]; then
        rm -rf "${WORKDIR}"
    fi
}
trap cleanup EXIT

fail() {
    echo "FAIL: $1" >&2
    FAILED=1
}

WORKDIR="$(mktemp -d)"
SERVER_LOG="${WORKDIR}/server.log"

install_signed_release() {
    local repo=omniswitch-dev/omniswitch
    local version="${OMNISWITCH_VERSION:-}" os arch archive binary base directory expected actual
    if [ -z "$version" ]; then
        local metadata
        metadata=$(curl -fsSL --retry 3 "https://api.github.com/repos/${repo}/releases/latest") || return 1
        version=$(printf '%s\n' "$metadata" | sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p')
    fi
    if [ -z "$version" ]; then
        echo "FAIL: could not resolve the release tag" >&2
        return 1
    fi
    os=$(uname -s | tr '[:upper:]' '[:lower:]')
    case "$os" in
        linux|darwin) ;;
        mingw*|msys*|cygwin*) os=windows ;;
        *) echo "FAIL: unsupported OS: ${os}" >&2; return 1 ;;
    esac
    arch=$(uname -m)
    case "$arch" in
        x86_64|amd64) arch=amd64 ;;
        aarch64|arm64) arch=arm64 ;;
        *) echo "FAIL: unsupported architecture: ${arch}" >&2; return 1 ;;
    esac
    binary=omniswitch
    archive="omniswitch_${version#v}_${os}_${arch}.tar.gz"
    if [ "$os" = windows ]; then
        binary=omniswitch.exe
        archive="omniswitch_${version#v}_${os}_${arch}.zip"
    fi
    base="https://github.com/${repo}/releases/download/${version}"
    directory="${WORKDIR}/release"
    mkdir -p "$directory" || return 1
    local file
    for file in checksums.txt checksums.txt.sig checksums.txt.pem; do
        curl -fsSL --retry 3 -o "${directory}/${file}" "${base}/${file}" || return 1
    done
    echo "==> Authenticating checksums.txt with cosign"
    cosign verify-blob "${directory}/checksums.txt" \
        --signature "${directory}/checksums.txt.sig" --certificate "${directory}/checksums.txt.pem" \
        --certificate-identity-regexp '^https://github\.com/omniswitch-dev/omniswitch/\.github/workflows/release\.yml@refs/tags/v.*$' \
        --certificate-oidc-issuer https://token.actions.githubusercontent.com || return 1
    curl -fsSL --retry 3 -o "${directory}/${archive}" "${base}/${archive}" || return 1
    expected=$(awk -v file="$archive" '$2 == file || $2 == "*"file { print $1 }' "${directory}/checksums.txt")
    if command -v sha256sum >/dev/null 2>&1; then
        actual=$(sha256sum "${directory}/${archive}" | awk '{ print $1 }') || return 1
    elif command -v shasum >/dev/null 2>&1; then
        actual=$(shasum -a 256 "${directory}/${archive}" | awk '{ print $1 }') || return 1
    else
        echo "FAIL: sha256sum or shasum is required" >&2
        return 1
    fi
    if [ -z "$expected" ] || [ "$expected" != "$actual" ]; then
        echo "FAIL: ${archive} does not match the signed checksums.txt" >&2
        return 1
    fi
    # Extract only the verified gateway binary, never an unauthenticated archive.
    if [ "$os" = windows ]; then
        unzip -q "${directory}/${archive}" "$binary" -d "$directory" || return 1
    else
        tar -xzf "${directory}/${archive}" -C "$directory" "$binary" || return 1
    fi
    install -m 0755 "${directory}/${binary}" "${INSTALL_DIR}/${binary}"
}

# --- Resolve the binary -----------------------------------------------------

if [ -n "${OMNISWITCH_BIN:-}" ]; then
    BIN="${OMNISWITCH_BIN}"
    if [ ! -x "${BIN}" ]; then
        echo "FAIL: OMNISWITCH_BIN=${BIN} is not an executable file" >&2
        exit 1
    fi
    echo "==> Using pre-built binary: ${BIN}"
else
    INSTALL_DIR="${WORKDIR}/bin"
    mkdir -p "${INSTALL_DIR}"
    if command -v cosign >/dev/null 2>&1; then
        echo "==> Installing a signature-verified release (OMNISWITCH_VERSION=${OMNISWITCH_VERSION:-<latest>})"
        if ! install_signed_release; then
            echo "FAIL: signature-verified installation did not complete successfully" >&2
            exit 1
        fi
    else
        echo "==> cosign not found; install.sh verifies SHA-256 only"
        if ! OMNISWITCH_INSTALL="${INSTALL_DIR}" sh "${REPO_ROOT}/install.sh"; then
            echo "FAIL: install.sh did not complete successfully" >&2
            exit 1
        fi
    fi
    BIN="${INSTALL_DIR}/omniswitch"
    if [ ! -x "${BIN}" ]; then
        # install.sh on Windows-style hosts may append .exe; check that too.
        if [ -x "${INSTALL_DIR}/omniswitch.exe" ]; then
            BIN="${INSTALL_DIR}/omniswitch.exe"
        else
            echo "FAIL: install.sh completed but ${INSTALL_DIR}/omniswitch was not produced" >&2
            exit 1
        fi
    fi
fi

# --- omniswitch version -----------------------------------------------------
#
# Run with a watchdog timeout: a binary that predates the `version`
# subcommand may not recognize it as a subcommand at all and instead ignore
# the argument and start the server, which would otherwise hang this check
# forever waiting for the process to exit.

VERSION_TIMEOUT="${OMNISWITCH_VERSION_TIMEOUT:-10}"
VERSION_LOG="${WORKDIR}/version.log"

echo "==> Running: ${BIN} version (timeout ${VERSION_TIMEOUT}s)"
"${BIN}" version >"${VERSION_LOG}" 2>&1 &
VERSION_PID=$!
i=0
while kill -0 "${VERSION_PID}" 2>/dev/null; do
    if [ "${i}" -ge "${VERSION_TIMEOUT}" ]; then
        kill -9 "${VERSION_PID}" 2>/dev/null
        wait "${VERSION_PID}" 2>/dev/null
        fail "'${BIN} version' did not exit within ${VERSION_TIMEOUT}s. This binary likely predates the version subcommand and is treating 'version' as an ignored argument while starting the server instead."
        VERSION_PID=""
        break
    fi
    sleep 1
    i=$((i + 1))
done

if [ -n "${VERSION_PID}" ]; then
    wait "${VERSION_PID}"
    VERSION_RC=$?
    cat "${VERSION_LOG}"
    if [ "${VERSION_RC}" -ne 0 ]; then
        fail "'${BIN} version' exited ${VERSION_RC} (expected 0)."
    fi
else
    cat "${VERSION_LOG}"
fi

# --- Start the server --------------------------------------------------------

echo "==> Starting: OPENAI_API_KEY=dummy ${BIN} serve (listening on :${PORT})"
(
    cd "${WORKDIR}"
    OPENAI_API_KEY="dummy" \
    OMNISWITCH_LISTEN=":${PORT}" \
    OMNISWITCH_DATA="${WORKDIR}/data" \
    OMNISWITCH_AUTH="false" \
    OMNISWITCH_MCP_ENABLED="false" \
    OMNISWITCH_CONFIG="" \
    exec "${BIN}" serve
) >"${SERVER_LOG}" 2>&1 &
SERVER_PID=$!

echo "==> Waiting up to ${START_TIMEOUT}s for ${BASE_URL}/healthz"
UP=0
DEADLINE=$((SECONDS + START_TIMEOUT))
while [ "${SECONDS}" -lt "${DEADLINE}" ]; do
    if ! kill -0 "${SERVER_PID}" 2>/dev/null; then
        fail "server process exited before becoming healthy; see log below"
        break
    fi
    if curl -fsS -o /dev/null "${BASE_URL}/healthz" 2>/dev/null; then
        UP=1
        break
    fi
    sleep 1
done

if [ "${UP}" -ne 1 ] && [ "${FAILED}" -eq 0 ]; then
    fail "${BASE_URL}/healthz did not return 200 within ${START_TIMEOUT}s"
fi

# --- Endpoint checks ---------------------------------------------------------

check_endpoint() {
    local path="$1"
    local url="${BASE_URL}${path}"
    local code
    code=$(curl -s -o "${WORKDIR}/last_response.txt" -w '%{http_code}' "${url}" 2>/dev/null) || code="000"
    if [ "${code}" = "200" ]; then
        echo "OK    ${path} -> 200"
    else
        fail "GET ${path} returned ${code} (expected 200)"
        echo "----- response body (${path}) -----"
        cat "${WORKDIR}/last_response.txt" 2>/dev/null || true
        echo "------------------------------------"
    fi
}

if [ "${UP}" -eq 1 ]; then
    check_endpoint "/healthz"
    check_endpoint "/readyz"
    check_endpoint "/v1/models"
fi

echo "----- server log -----"
cat "${SERVER_LOG}" 2>/dev/null || true
echo "-----------------------"

if [ "${FAILED}" -ne 0 ]; then
    echo "RESULT: FAIL" >&2
    exit 1
fi

echo "RESULT: PASS"
exit 0
