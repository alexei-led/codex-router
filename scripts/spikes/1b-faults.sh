#!/usr/bin/env bash
# Spike 1b: proxy fault injection against `codex exec` on a fixed port.
set -euo pipefail

# shellcheck source=lib.sh
. "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

handle_help 1b-faults.sh "Runs codex exec through shape-proxy.py four times with injected faults:
  1 proxy never bound   2 502 before upstream, proxy restarted after 10 s
  3 connection closed after the first output item   4 kill -9 mid-turn, restart after 5 s
Env: CASES='1 2 3 4' picks a subset; PORT=47911 sets the proxy port; LIMIT=150 caps each run in seconds." "$@"

PORT=${PORT:-47911}
LIMIT=${LIMIT:-150}
NO_PROXY_LIMIT=${NO_PROXY_LIMIT:-90}
CASES=${CASES:-1 2 3 4}

codex_preflight

RT=$(make_rt cr-spike-1b)
REPO="$RT/repo"
make_repo "$REPO"
trap 'trap - EXIT INT TERM; kill_children' EXIT INT TERM

python3 -I -c "
import socket, sys
s = socket.socket()
try:
    s.bind(('127.0.0.1', $PORT))
except OSError:
    sys.exit(1)
" || die "port $PORT is busy; set PORT=<free port>"

echo "== 1b: evidence dir $RT (port $PORT)"

run_codex_bg() {
	# run_codex_bg NAME LIMIT: codex in the background; result in $RT/NAME.rc as "rc seconds".
	local name=$1 limit=$2
	codex_exec_cmd "$PORT" "$THREE_TOOL_PROMPT"
	(
		RB_CWD=$REPO run_bounded "$limit" "$RT/$name.codex.out" "$RT/$name.codex.err" "${CODEX_CMD[@]}"
		echo "$RB_RC $RB_SECS" >"$RT/$name.rc"
	) &
	CODEX_BG=$!
	track_pid "$CODEX_BG"
}

wait_codex() {
	wait "$CODEX_BG" 2>/dev/null || true
}

report() {
	# report NAME: one-screen result for a case.
	local name=$1 rc secs
	read -r rc secs <"$RT/$name.rc" || true
	echo "-- $name: exit=${rc:-?} wall=${secs:-?}s timed_out=$([ "${rc:-}" = 124 ] && echo yes || echo no)" \
		"stderr_bytes=$(wc -c <"$RT/$name.codex.err" | tr -d ' ')"
	if [ -s "$RT/$name.jsonl" ]; then
		python3 -I "$SHAPE_PROXY" --summarize "$RT/$name.jsonl" | sed 's/^/   /'
	else
		echo "   (no requests reached the proxy)"
	fi
}

case_1() {
	echo "== case 1: proxy never bound (limit ${NO_PROXY_LIMIT}s)"
	codex_exec_cmd "$PORT" "$THREE_TOOL_PROMPT"
	RB_CWD=$REPO run_bounded "$NO_PROXY_LIMIT" "$RT/1b-1.codex.out" "$RT/1b-1.codex.err" "${CODEX_CMD[@]}"
	echo "$RB_RC $RB_SECS" >"$RT/1b-1.rc"
	report 1b-1
}

case_2() {
	echo "== case 2: 502 before upstream, proxy restarted in none mode after 10 s"
	RECORD=$RT/1b-2.jsonl start_proxy 1b-2-fault "$PORT" --fault before-upstream
	run_codex_bg 1b-2 "$LIMIT"
	sleep 10
	stop_proxy
	RECORD=$RT/1b-2.jsonl start_proxy 1b-2-restart "$PORT"
	wait_codex
	stop_proxy
	report 1b-2
}

case_3() {
	echo "== case 3: close after first output item (first /responses only)"
	start_proxy 1b-3 "$PORT" --fault after-first-item --fault-count 1
	run_codex_bg 1b-3 "$LIMIT"
	wait_codex
	stop_proxy
	report 1b-3
}

case_4() {
	echo "== case 4: kill -9 the proxy after the first recorded request, restart after 5 s"
	RECORD=$RT/1b-4.jsonl start_proxy 1b-4 "$PORT"
	local first_pid=$PROXY_PID
	run_codex_bg 1b-4 "$LIMIT"
	for _ in $(seq 1 120); do
		[ -s "$RT/1b-4.jsonl" ] && break
		sleep 0.5
	done
	kill -9 "$first_pid" 2>/dev/null || true
	wait "$first_pid" 2>/dev/null || true
	echo "   proxy killed at t=${SECONDS}s"
	sleep 5
	RECORD=$RT/1b-4.jsonl start_proxy 1b-4-restart "$PORT"
	wait_codex
	stop_proxy
	report 1b-4
}

for c in $CASES; do
	case "$c" in
	1) case_1 ;;
	2) case_2 ;;
	3) case_3 ;;
	4) case_4 ;;
	*) echo "unknown case $c" >&2 ;;
	esac
done

echo
echo "evidence: $RT  (1b-<case>.jsonl, 1b-<case>*.proxy.log, 1b-<case>.codex.err, 1b-<case>.rc)"
