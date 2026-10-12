#!/usr/bin/env bash
# Stop the smelt daemon so the next smelt command starts it from the freshly
# built binary. A running daemon keeps the code it started with.
#
# It is left alone while any factory run is active (in any project): stopping
# it would cancel that run. Pass --force to stop it anyway.
set -euo pipefail

force=false
[ "${1:-}" = "--force" ] && force=true

dir="${SMELT_DAEMON_DIR:-${HOME}/.smelt/daemon}"
sock="${dir}/daemon.sock"
pidfile="${dir}/daemon.pid"

if ! pgrep -f 'smelt _daemon' >/dev/null 2>&1; then
	exit 0 # not running: nothing to restart
fi

if [ "${force}" = false ] && [ -S "${sock}" ] && command -v curl >/dev/null && command -v python3 >/dev/null; then
	active=$(curl -s --max-time 3 --unix-socket "${sock}" http://smeltd/factory 2>/dev/null |
		python3 -c 'import json,sys
try: print(sum(1 for s in json.load(sys.stdin).get("sessions") or [] if s.get("state") == "running"))
except Exception: print(0)')
	if [ "${active}" -gt 0 ]; then
		echo "smelt daemon left running: ${active} factory run(s) active. It still runs the old binary;" >&2
		echo "restart it when they finish with: task daemon:restart" >&2
		exit 0
	fi
fi

if [ -f "${pidfile}" ] && kill "$(cat "${pidfile}")" 2>/dev/null; then
	:
else
	pkill -f 'smelt _daemon' || true
fi
echo "smelt daemon stopped; the next smelt command starts it from the new build." >&2
