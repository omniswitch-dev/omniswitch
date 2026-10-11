#!/bin/sh
# OmniSwitch installer: https://github.com/omniswitch-dev/omniswitch
#
#   curl -fsSL https://raw.githubusercontent.com/omniswitch-dev/omniswitch/main/install.sh | sh
#
set -e

REPO="omniswitch-dev/omniswitch"
INSTALL_DIR="${OMNISWITCH_INSTALL:-/usr/local/bin}"

get_arch() {
    arch=$(uname -m)
    case "$arch" in
        x86_64|amd64) echo "amd64" ;;
        aarch64|arm64) echo "arm64" ;;
        *) echo "unsupported architecture: $arch" >&2; exit 1 ;;
    esac
}

get_os() {
    os=$(uname -s | tr '[:upper:]' '[:lower:]')
    case "$os" in
        linux) echo "linux" ;;
        darwin) echo "darwin" ;;
        mingw*|msys*|cygwin*) echo "windows" ;;
        *) echo "unsupported OS: $os" >&2; exit 1 ;;
    esac
}

OS=$(get_os)
ARCH=$(get_arch)
EXT=""
if [ "$OS" = "windows" ]; then EXT=".zip"; else EXT=".tar.gz"; fi

if [ -z "${OMNISWITCH_VERSION}" ]; then
    VERSION=$(curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest" | grep '"tag_name"' | sed -E 's/.*"([^"]+)".*/\1/')
else
    VERSION="${OMNISWITCH_VERSION}"
fi

if [ -z "$VERSION" ]; then
    echo "error: could not determine the latest release; set OMNISWITCH_VERSION=v0.1.0 and retry" >&2
    exit 1
fi

# Release archives are named without the leading "v" of the tag
# (goreleaser {{ .Version }}), for example omniswitch_0.2.0_linux_amd64.tar.gz.
ARCHIVE="omniswitch_${VERSION#v}_${OS}_${ARCH}${EXT}"
BASE_URL="https://github.com/${REPO}/releases/download/${VERSION}"
URL="${BASE_URL}/${ARCHIVE}"
TMPDIR=$(mktemp -d)
trap 'rm -rf "$TMPDIR"' EXIT

echo "Downloading ${URL}..."
curl -fSL --retry 3 -o "${TMPDIR}/${ARCHIVE}" "$URL"

echo "Verifying SHA-256 checksum..."
curl -fsSL --retry 3 -o "${TMPDIR}/checksums.txt" "${BASE_URL}/checksums.txt"
EXPECTED=$(awk -v f="$ARCHIVE" '$2 == f || $2 == "*"f { print $1 }' "${TMPDIR}/checksums.txt")
if [ -z "$EXPECTED" ]; then
    echo "error: ${ARCHIVE} is not listed in checksums.txt" >&2
    exit 1
fi
if command -v sha256sum >/dev/null 2>&1; then
    ACTUAL=$(sha256sum "${TMPDIR}/${ARCHIVE}" | awk '{ print $1 }')
elif command -v shasum >/dev/null 2>&1; then
    ACTUAL=$(shasum -a 256 "${TMPDIR}/${ARCHIVE}" | awk '{ print $1 }')
else
    echo "error: sha256sum or shasum is required to verify the download" >&2
    exit 1
fi
if [ "$EXPECTED" != "$ACTUAL" ]; then
    echo "error: checksum mismatch for ${ARCHIVE}" >&2
    echo "  expected ${EXPECTED}" >&2
    echo "  actual   ${ACTUAL}" >&2
    exit 1
fi
echo "Checksum OK"

echo "Extracting..."
case "$EXT" in
    .tar.gz) tar -xzf "${TMPDIR}/${ARCHIVE}" -C "$TMPDIR" ;;
    .zip) unzip -q "${TMPDIR}/${ARCHIVE}" -d "$TMPDIR" ;;
esac

BINARY="omniswitch"
[ "$OS" = "windows" ] && BINARY="omniswitch.exe"

mkdir -p "$INSTALL_DIR" 2>/dev/null || SUDO_NEEDED=1
if [ "$SUDO_NEEDED" = "1" ]; then
    sudo install -m 0755 "${TMPDIR}/${BINARY}" "${INSTALL_DIR}/${BINARY}"
else
    install -m 0755 "${TMPDIR}/${BINARY}" "${INSTALL_DIR}/${BINARY}"
fi

echo "Installed ${INSTALL_DIR}/${BINARY}"
case ":${PATH}:" in
    *":${INSTALL_DIR}:"*) ;;
    *) echo "Note: ${INSTALL_DIR} is not on your PATH." ;;
esac
echo
echo "Quickstart: OPENAI_API_KEY=sk-... omniswitch serve"
