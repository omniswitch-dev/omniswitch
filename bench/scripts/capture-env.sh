#!/usr/bin/env bash
# capture-env.sh — write host/toolchain/environment facts to manifest.json
# per benchmark plan section 3.6. Run once per campaign (or per run if the
# host state might have changed) before any measured trial.
#
# Usage: bench/scripts/capture-env.sh > bench/results/<run-id>/manifest.json
set -euo pipefail

json_str() { printf '%s' "$1" | python3 -c 'import json,sys; print(json.dumps(sys.stdin.read().strip()))'; }

GIT_SHA="$(git rev-parse HEAD 2>/dev/null || echo unknown)"
GIT_DIRTY="$( [ -n "$(git status --porcelain 2>/dev/null)" ] && echo true || echo false )"
DOCKER_VERSION="$(docker version --format '{{.Server.Version}}' 2>/dev/null || echo unknown)"
KERNEL="$(uname -r 2>/dev/null || echo unknown)"
UNAME_A="$(uname -a 2>/dev/null || echo unknown)"
GO_VERSION="$(go version 2>/dev/null || echo unknown)"
CPU_MODEL="$(lscpu 2>/dev/null | awk -F: '/Model name/ {gsub(/^[ \t]+/, "", $2); print $2; exit}')"
CPU_MODEL="${CPU_MODEL:-unknown}"
GOVERNOR="$(cat /sys/devices/system/cpu/cpu0/cpufreq/scaling_governor 2>/dev/null || echo unknown)"

cat <<EOF
{
  "captured_at": "$(date -u +%Y-%m-%dT%H:%M:%SZ)",
  "git_sha": "$GIT_SHA",
  "git_dirty": $GIT_DIRTY,
  "docker_version": $(json_str "$DOCKER_VERSION"),
  "kernel": $(json_str "$KERNEL"),
  "uname_a": $(json_str "$UNAME_A"),
  "go_version": $(json_str "$GO_VERSION"),
  "cpu_model": $(json_str "$CPU_MODEL"),
  "cpu_governor": $(json_str "$GOVERNOR"),
  "note": "Per plan section 3.6, this should also capture docker inspect per container, sysctl subset (net.core.somaxconn, net.ipv4.ip_local_port_range, tcp_tw_reuse), turbo/SMT state, and the plan file hash. Those are appended by run-cell.sh per-run, not here, since they depend on the containers already being started."
}
EOF
