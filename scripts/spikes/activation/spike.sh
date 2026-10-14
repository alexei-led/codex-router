#!/usr/bin/env bash
# macOS socket activation spike (plan Task 13, gate G3 and G4): a launchd
# agent whose port launchd holds, served by the minimal edge in this dir.
#
#   spike.sh up PORT [UPSTREAM_URL] [TOKEN_FILE]
#                       build the edge, load the agent on 127.0.0.1:PORT
#   spike.sh probe [MS] time one /spike request (the edge waits MS first)
#   spike.sh stall      kill -9 the edge during a slow request, then time the
#                       next request: it waits in the backlog until launchd
#                       starts the edge again
#   spike.sh kill       kill -9 the edge (launchd keeps the port)
#   spike.sh down       unload the agent; the run dir stays for its log
#
# The agent is loaded from the run dir with launchctl bootstrap, so nothing
# is installed into ~/Library/LaunchAgents. Needs launchctl, curl, go.
set -euo pipefail

here=$(cd "$(dirname "$0")" && pwd)
run_dir=$HOME/.cache/codex-router-live/activation
label=com.github.alexei-led.codex-router.spike.edge
domain=gui/$(id -u)

die() {
	echo "spike: $*" >&2
	exit 1
}

load() {
	[ -f "$run_dir/spike.env" ] || die "not up; use: spike.sh up PORT"
	# shellcheck disable=SC1091
	. "$run_dir/spike.env"
}

probe() { # delay-ms
	curl -sS --max-time 120 -w ' %{time_total}s\n' "http://127.0.0.1:${PORT:?}/spike?delay=${1:-0}"
}

up() { # port [upstream] [token-file]
	[ "$(uname)" = Darwin ] || die "macOS only; on Linux use the systemd units in $here"
	local port=$1 upstream=${2:-} token_file=${3:-}
	mkdir -p "$run_dir"
	chmod 0700 "$run_dir"
	(cd "$here" && CGO_ENABLED=0 go build -o "$run_dir/edge" .)
	sed -e "s#@LABEL@#$label#" -e "s#@BIN@#$run_dir/edge#" -e "s#@PORT@#$port#" \
		-e "s#@UPSTREAM@#$upstream#" -e "s#@TOKEN_FILE@#$token_file#" \
		-e "s#@LOG@#$run_dir/edge.log#" "$here/edge.plist" >"$run_dir/edge.plist"
	echo "PORT=$port" >"$run_dir/spike.env"
	launchctl bootstrap "$domain" "$run_dir/edge.plist"
	echo "loaded $label on 127.0.0.1:$port; log $run_dir/edge.log"
}

stall() {
	load
	probe 3000 >"$run_dir/slow.out" 2>&1 &
	local slow=$!
	sleep 1
	launchctl kill SIGKILL "$domain/$label"
	echo "killed the edge during a slow request"
	echo -n "next request: "
	probe 0
	wait "$slow" || true
	echo "slow request: $(cat "$run_dir/slow.out")"
}

case "${1:-}" in
up) up "${2:?port}" "${3:-}" "${4:-}" ;;
probe)
	load
	probe "${2:-0}"
	;;
stall) stall ;;
kill) launchctl kill SIGKILL "$domain/$label" ;;
down) launchctl bootout "$domain/$label" ;;
*) die "usage: spike.sh up PORT [UPSTREAM_URL] [TOKEN_FILE]|probe [MS]|stall|kill|down" ;;
esac
