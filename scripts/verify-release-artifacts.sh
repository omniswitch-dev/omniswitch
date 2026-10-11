#!/usr/bin/env bash
# Check the release archive contract consumed by install.sh and the SBOMs.
set -euo pipefail

DIRECTORY="${1:-dist}"
python3 - "$DIRECTORY" <<'PY'
import json
import pathlib
import re
import sys

root = pathlib.Path(sys.argv[1])
archives = sorted([*root.glob("*.tar.gz"), *root.glob("*.zip")])
expected = {("linux", "amd64"), ("linux", "arm64"), ("darwin", "amd64"), ("darwin", "arm64"), ("windows", "amd64")}
targets = set()
for archive in archives:
    match = re.fullmatch(r"omniswitch_(?!v)[^_]+_(linux|darwin|windows)_(amd64|arm64)\.(tar\.gz|zip)", archive.name)
    if not match:
        raise SystemExit(f"Unexpected archive name: {archive.name}")
    targets.add((match[1], match[2]))
    if (match[1] == "windows") != (match[3] == "zip"):
        raise SystemExit(f"Unexpected archive format: {archive.name}")
    sbom = root / (archive.name + ".sbom.spdx.json")
    document = json.loads(sbom.read_text())
    if not document.get("spdxVersion", "").startswith("SPDX-") or not document.get("packages"):
        raise SystemExit(f"Invalid or empty SPDX SBOM: {sbom.name}")
    print(f"OK {archive.name} + {sbom.name}")
if targets != expected or len(archives) != len(expected):
    raise SystemExit(f"Expected five release targets, got: {sorted(targets)}")
if len(list(root.glob("*.sbom.spdx.json"))) != len(archives):
    raise SystemExit("Expected exactly one SPDX SBOM per archive")
PY
(cd "$DIRECTORY" && sha256sum --check checksums.txt)
