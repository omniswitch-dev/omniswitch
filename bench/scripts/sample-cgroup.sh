#!/usr/bin/env bash
# sample-cgroup.sh — sample a container's cgroup v2 cpu.stat/memory.current
# at a fixed interval to CSV, per benchmark plan section 3.5.
#
# Usage: bench/scripts/sample-cgroup.sh <container-name> <output.csv> [interval_seconds] [duration_seconds]
set -euo pipefail

CONTAINER="${1:?container name required}"
OUT="${2:?output csv path required}"
INTERVAL="${3:-0.1}"
DURATION="${4:-60}"

cid="$(docker inspect -f '{{.Id}}' "$CONTAINER")"
# cgroup v2 path under the default docker cgroup driver; adjust
# CGROUP_BASE if the host uses a non-default cgroup root.
CGROUP_BASE="${CGROUP_BASE:-/sys/fs/cgroup/system.slice/docker-${cid}.scope}"

echo "timestamp_ms,usage_usec,user_usec,system_usec,memory_current_bytes,memory_peak_bytes" > "$OUT"

end=$(( $(date +%s%3N 2>/dev/null || echo $(($(date +%s)*1000)) ) + DURATION * 1000 ))
while true; do
  now_ms=$(date +%s%3N 2>/dev/null || echo $(($(date +%s)*1000)))
  if [ "$now_ms" -ge "$end" ]; then break; fi

  if [ -r "$CGROUP_BASE/cpu.stat" ]; then
    usage_usec=$(awk '/^usage_usec/ {print $2}' "$CGROUP_BASE/cpu.stat")
    user_usec=$(awk '/^user_usec/ {print $2}' "$CGROUP_BASE/cpu.stat")
    system_usec=$(awk '/^system_usec/ {print $2}' "$CGROUP_BASE/cpu.stat")
  else
    usage_usec=""; user_usec=""; system_usec=""
  fi
  mem_current=$(cat "$CGROUP_BASE/memory.current" 2>/dev/null || echo "")
  mem_peak=$(cat "$CGROUP_BASE/memory.peak" 2>/dev/null || echo "")

  echo "$now_ms,$usage_usec,$user_usec,$system_usec,$mem_current,$mem_peak" >> "$OUT"
  sleep "$INTERVAL"
done

echo "wrote $OUT" >&2
