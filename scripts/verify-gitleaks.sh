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
