#!/usr/bin/env bash
# Spike 1c: request shapes (baseline, compaction local/remote, tool error, subagent) and post-compaction switch.
set -euo pipefail

# shellcheck source=lib.sh
. "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

handle_help 1c-shapes.sh "Runs five codex exec cases through shape-proxy.py --record and prints a one-screen
summary of request shapes (header names, turn-metadata keys, turn_id, item types, body keys,
compaction hints, status, usage). Header values are never recorded.
  1 baseline   2 local compaction + switch to gpt-6-luna/low after the item count drops
  3 remote compaction (capabilities.remote_compaction=v2)   4 failing tool command   5 subagent
Env: CASES='1 2 3 4 5' picks a subset; LIMIT=240 caps each run in seconds." "$@"

CASES=${CASES:-1 2 3 4 5}
LIMIT=${LIMIT:-240}
CONTEXT_WINDOW=60000
COMPACT_LIMIT=45000
BIG_FILES=8
BIG_FILE_KB=32

LONG_PROMPT='Use the shell tool to read each file data/part1.txt through data/part8.txt in full with cat, one file per tool call, in order. When all are read, reply with the total number of lines across the files.'
FAIL_PROMPT='Run the shell command: false; exit 1. Then run: echo after. Then reply done and say what the first command returned.'
SUBAGENT_PROMPT='Use the spawn_agent tool to start a subagent that runs "echo hi" and reports its output. If you have no spawn_agent tool, reply exactly: no spawn_agent tool.'

codex_preflight

RT=$(make_rt cr-spike-1c)
REPO="$RT/repo"
RECORD="$RT/shapes.jsonl"
make_repo "$REPO"
make_big_files "$REPO/data" "$BIG_FILES" "$BIG_FILE_KB"
trap 'trap - EXIT INT TERM; kill_children' EXIT INT TERM
echo "== 1c: evidence dir $RT"

run_case() {
	# run_case NAME PROMPT PROXY_ARGS_STRING -- CODEX_EXTRA_ARGS...
	local name=$1 prompt=$2 proxy_args=$3
	shift 3
	local -a pargs=()
	# shellcheck disable=SC2206 # proxy_args is a plain space-separated flag list
	[ -n "$proxy_args" ] && pargs=($proxy_args)
	echo "-- case $name"
	start_proxy "$name" 0 ${pargs[@]+"${pargs[@]}"}
	codex_exec_cmd "$PROXY_PORT" "$prompt" "$@"
	RB_CWD=$REPO run_bounded "$LIMIT" "$RT/$name.codex.out" "$RT/$name.codex.err" "${CODEX_CMD[@]}"
	stop_proxy
	echo "   exit=$RB_RC wall=${RB_SECS}s (124 = hit the ${LIMIT}s limit)"
}

COMPACT_ARGS=(-c "model_context_window=$CONTEXT_WINDOW" -c "model_auto_compact_token_limit=$COMPACT_LIMIT")

for c in $CASES; do
	case "$c" in
	1) run_case 1-baseline "$THREE_TOOL_PROMPT" "" ;;
	2)
		MODEL=gpt-6-sol EFFORT=medium run_case 2-local-compaction-switch "$LONG_PROMPT" \
			"--rewrite model=gpt-6-luna,effort=low --rewrite-after-drop" "${COMPACT_ARGS[@]}"
		;;
	3)
		MODEL=gpt-6-sol EFFORT=medium run_case 3-remote-compaction "$LONG_PROMPT" "" \
			"${COMPACT_ARGS[@]}" -c 'model_providers.router.capabilities.remote_compaction="v2"'
		;;
	4) run_case 4-tool-error "$FAIL_PROMPT" "" ;;
	5) run_case 5-subagent "$SUBAGENT_PROMPT" "" ;;
	*) echo "unknown case $c" >&2 ;;
	esac
done

echo
if [ -s "$RECORD" ]; then
	python3 -I "$SHAPE_PROXY" --summarize "$RECORD"
	echo
	echo "== post-compaction switch verdict (case 2)"
	python3 -I - "$RECORD" <<'PY'
import json, sys

rows = [json.loads(line) for line in open(sys.argv[1]) if line.strip()]
case = [r for r in rows if r["case"] == "2-local-compaction-switch" and r["path"].endswith("/responses")]
if not case:
    print("  case 2 did not run")
    sys.exit(0)
counts = [r["item_count"] for r in case]
dropped = any(b < a for a, b in zip(counts, counts[1:]))
switched = [r for r in case if r["rewritten"]]
print("  item counts per request: %s" % counts)
print("  item count dropped (compaction seen): %s" % ("yes" if dropped else "NO - compaction did not trigger"))
if switched:
    print("  first switched request status: %s; all switched statuses: %s"
          % (switched[0]["status"], [r["status"] for r in switched]))
else:
    print("  no request was rewritten")
PY
else
	echo "no requests recorded"
fi
echo
echo "evidence: $RT  (shapes.jsonl, <case>.proxy.log, <case>.codex.err)"
