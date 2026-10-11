#!/usr/bin/env bash
# baseline-noise-gate.sh — CoV acceptance gate for a direct-baseline trial
# block, per benchmark plan section 3.9: p50 CoV <= 10% and p99 CoV <= 20%
# across the block's trials, otherwise the block must be discarded and
# rerun (host noise).
#
# Usage: bench/scripts/baseline-noise-gate.sh p50_1 p50_2 ... -- p99_1 p99_2 ...
# Exits 0 and prints "PASS" if both gates hold, exits 1 and prints "FAIL"
# with the offending CoV otherwise.
set -euo pipefail

p50_vals=()
p99_vals=()
mode="p50"
for arg in "$@"; do
  if [ "$arg" = "--" ]; then mode="p99"; continue; fi
  if [ "$mode" = "p50" ]; then p50_vals+=("$arg"); else p99_vals+=("$arg"); fi
done

cov() {
  python3 - "$@" <<'EOF'
import sys, statistics
vals = [float(v) for v in sys.argv[1:]]
if len(vals) < 2:
    print(0.0)
    raise SystemExit
mean = statistics.mean(vals)
if mean == 0:
    print(0.0)
    raise SystemExit
sd = statistics.stdev(vals)
print(sd / mean * 100.0)
EOF
}

p50_cov="$(cov "${p50_vals[@]}")"
p99_cov="$(cov "${p99_vals[@]}")"

fail=0
if python3 -c "import sys; sys.exit(0 if $p50_cov <= 10.0 else 1)"; then :; else fail=1; fi
if python3 -c "import sys; sys.exit(0 if $p99_cov <= 20.0 else 1)"; then :; else fail=1; fi

echo "p50 CoV: ${p50_cov}% (gate <= 10%)"
echo "p99 CoV: ${p99_cov}% (gate <= 20%)"

if [ "$fail" -eq 0 ]; then
  echo "PASS"
  exit 0
else
  echo "FAIL: block exceeds noise gate, discard and rerun per plan section 3.9"
  exit 1
fi
