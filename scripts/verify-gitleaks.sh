#!/usr/bin/env bash
# Generate detector fixtures outside the repository; no committed API keys.
set -euo pipefail

DIRECTORY=$(mktemp -d)
trap 'rm -rf "$DIRECTORY"' EXIT

printf 'api_key=sk-osw-%064d\napi_key=sk-sentinel-%064d\n' 1 2 > "$DIRECTORY/keys.txt"
status=0
gitleaks stdin --config .gitleaks.toml --enable-rule omniswitch-api-key \
  --redact --no-banner --exit-code 42 --report-format json \
  --report-path "$DIRECTORY/report.json" < "$DIRECTORY/keys.txt" || status=$?
if [ "$status" -ne 42 ]; then
  echo "Expected both API key formats to be detected (exit 42), got ${status}" >&2
  exit 1
fi
python3 - "$DIRECTORY/report.json" <<'PY'
import json
import sys

findings = json.load(open(sys.argv[1]))
assert len(findings) == 2 and all(f["RuleID"] == "omniswitch-api-key" for f in findings)
PY

printf 'api_key=sk-osw-too-short\napi_key=sk-sentinel-not-a-key\n' | \
  gitleaks stdin --config .gitleaks.toml --enable-rule omniswitch-api-key --redact --no-banner

echo "OK current and legacy API key detection; placeholders do not trigger"

# Keep the default generic-key rule active alongside the historical SigV4
# fixture exception. Generate the credential-shaped value only in scratch.
python3 - "$DIRECTORY/generic.txt" <<'PY'
import hashlib
import sys
from pathlib import Path

value = hashlib.sha256(b"omniswitch generic API key detector fixture").hexdigest()
Path(sys.argv[1]).write_text("api_key=" + value + "\n")
PY
status=0
gitleaks stdin --config .gitleaks.toml --enable-rule generic-api-key \
  --redact --no-banner --exit-code 42 --report-format json \
  --report-path "$DIRECTORY/generic-report.json" < "$DIRECTORY/generic.txt" || status=$?
if [ "$status" -ne 42 ]; then
  echo "Expected the generated generic API key to be detected (exit 42), got ${status}" >&2
  exit 1
fi
python3 - "$DIRECTORY/generic-report.json" <<'PY'
import json
import sys

findings = json.load(open(sys.argv[1]))
assert len(findings) == 1 and findings[0]["RuleID"] == "generic-api-key"
PY

echo "OK generic API key detection remains enabled"
