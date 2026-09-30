#!/bin/sh
# Run as root to include per-process socket ownership from ss.
# Usage: ./scripts/observe-memory.sh [systemd-unit] [interval-seconds]
set -eu
unit=${1:-V2bX}
interval=${2:-60}
case "$interval" in ''|*[!0-9]*|0) echo 'interval must be a positive integer' >&2; exit 2;; esac
printf 'time,pid,rss_kb,anon_kb,swap_kb,os_threads,fd,tcp\n'
while :; do
    pid=$(systemctl show "$unit" --property MainPID --value)
    if [ "${pid:-0}" -gt 0 ] && [ -r "/proc/$pid/status" ]; then
        metrics=$(awk '
            /^VmRSS:/ {rss=$2} /^RssAnon:/ {anon=$2}
            /^VmSwap:/ {swap=$2} /^Threads:/ {threads=$2}
            END {printf "%d,%d,%d,%d", rss,anon,swap,threads}
        ' "/proc/$pid/status" 2>/dev/null) || metrics='NA,NA,NA,NA'
        fd=$(find "/proc/$pid/fd" -mindepth 1 -maxdepth 1 2>/dev/null | wc -l | tr -d ' ')
        tcp=$(ss -Hntp 2>/dev/null | awk -v pattern="pid=$pid," 'index($0,pattern) {n++} END {print n+0}')
        printf '%s,%s,%s,%s,%s\n' "$(date -u +%FT%TZ)" "$pid" "$metrics" "$fd" "$tcp"
    else
        printf '%s,%s,NA,NA,NA,NA,NA,NA\n' "$(date -u +%FT%TZ)" "${pid:-0}"
    fi
    sleep "$interval"
done
