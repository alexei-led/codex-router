#!/usr/bin/env bash
# shellcheck disable=SC2034 # variables here are consumed by the scripts that source this file
# Shared helpers for the Stage 1 spike scripts. Source it; do not run it.
# Rules: header VALUES are never printed; codex runs are wall-clock bounded;
# all evidence lives under one mktemp dir that the script prints at the end.

SPIKE_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SHAPE_PROXY="$SPIKE_DIR/shape-proxy.py"
APPSERVER_CLIENT="$SPIKE_DIR/appserver-client.py"
SPIKE_MODEL="${SPIKE_MODEL:-gpt-6-luna}"
SPIKE_EFFORT="${SPIKE_EFFORT:-low}"
SOCK_PATH_MAX=90

CHILD_PIDS=()

die() {
	echo "error: $*" >&2
	exit 1
}

need() {
	command -v "$1" >/dev/null 2>&1 || die "$1 is required but not on PATH"
}

print_order() {
	cat <<'TXT'
Run order (each from the repo root; the agent sandbox cannot run codex):
  ! bash scripts/spikes/selftest.sh       offline check of the tooling, no codex
  ! bash scripts/spikes/1a-two-clients.sh app-server + --remote TUI + footer mid-turn + parity
  ! bash scripts/spikes/1b-faults.sh      proxy fault injection (4 cases, ~10 min)
  ! bash scripts/spikes/1c-shapes.sh      request shapes and post-compaction switch (5 cases)
Evidence goes to a mktemp directory printed at the end of each script.
TXT
}

# handle_help NAME DESCRIPTION "$@": print usage and exit on -h/--help.
handle_help() {
	local name=$1 desc=$2
	shift 2
	case "${1:-}" in
	-h | --help)
		echo "usage: bash scripts/spikes/$name [-h]"
		echo
		echo "$desc"
		echo
		print_order
		exit 0
		;;
	esac
}

# make_rt PREFIX: private evidence dir; unix socket paths must stay short on macOS.
make_rt() {
	local dir
	dir=$(mktemp -d "${TMPDIR:-/tmp}/$1.XXXXXX")
	if [ "${#dir}" -gt "$SOCK_PATH_MAX" ]; then
		rmdir "$dir"
		dir=$(mktemp -d "/tmp/$1.XXXXXX")
	fi
	chmod 700 "$dir"
	printf '%s\n' "$dir"
}

# make_repo DIR: throwaway git repo for codex to work in.
make_repo() {
	mkdir -p "$1"
	git -C "$1" init -q
	printf 'spike repo\n' >"$1/README.md"
	git -C "$1" add README.md
	git -C "$1" -c user.name=spike -c user.email=spike@example.invalid commit -q -m init
}

# make_big_files DIR COUNT KB: text files that grow the context when read.
make_big_files() {
	local dir=$1 count=$2 kb=$3 i
	mkdir -p "$dir"
	for i in $(seq 1 "$count"); do
		awk -v n="$((kb * 1024 / 64))" -v f="$i" \
			'BEGIN { for (k = 1; k <= n; k++) printf "file %d line %05d the quick brown fox jumps over it\n", f, k }' \
			>"$dir/part$i.txt"
	done
}

track_pid() {
	CHILD_PIDS+=("$1")
}

kill_children() {
	local pid
	for pid in "${CHILD_PIDS[@]:-}"; do
		if [ -n "$pid" ]; then kill "$pid" 2>/dev/null || true; fi
	done
	for pid in "${CHILD_PIDS[@]:-}"; do
		if [ -n "$pid" ]; then wait "$pid" 2>/dev/null || true; fi
	done
	CHILD_PIDS=()
}

# run_bounded LIMIT_SECS OUT ERR CMD...: wall-clock limit without `timeout`.
# The child gets its own process group; on the limit it is TERMed, then KILLed.
# Sets RB_RC (124 on the limit) and RB_SECS. Honors RB_CWD.
run_bounded() {
	local limit=$1 out=$2 err=$3 start=$SECONDS
	shift 3
	RB_RC=0
	perl -e '
		my $limit = shift; chdir $ENV{RB_CWD} if $ENV{RB_CWD};
		my $pid = fork; die "fork: $!" unless defined $pid;
		if (!$pid) { setpgrp(0, 0); exec @ARGV or exit 127 }
		my $to = 0;
		my $stop = sub { kill "TERM", -$pid; sleep 2; kill "KILL", -$pid };
		$SIG{ALRM} = sub { $to = 1; $stop->() };
		$SIG{TERM} = $SIG{INT} = sub { $stop->(); exit 143 };
		alarm $limit;
		my $r;
		do { $r = waitpid($pid, 0) } while ($r == -1 && $!{EINTR});
		my $st = $?;
		kill "TERM", -$pid;
		exit 124 if $to;
		exit(($st & 127) ? 128 + ($st & 127) : $st >> 8);
	' "$limit" "$@" >"$out" 2>"$err" </dev/null || RB_RC=$?
	RB_SECS=$((SECONDS - start))
}

# provider_args PORT: sets PROVIDER_ARGS (only -c overrides; nothing under ~/.codex is edited).
provider_args() {
	PROVIDER_ARGS=(
		-c model_provider=router
		-c "model_providers.router={name=\"router\", base_url=\"http://127.0.0.1:$1/v1\", wire_api=\"responses\", requires_openai_auth=true}"
	)
}

# codex_exec_cmd PORT PROMPT [EXTRA_ARGS...]: sets CODEX_CMD for run_bounded.
# MODEL and EFFORT override the cheap defaults.
codex_exec_cmd() {
	local port=$1 prompt=$2
	shift 2
	provider_args "$port"
	CODEX_CMD=(codex --no-daemon exec --ephemeral -s read-only
		-m "${MODEL:-$SPIKE_MODEL}" -c "model_reasoning_effort=${EFFORT:-$SPIKE_EFFORT}"
		"${PROVIDER_ARGS[@]}" "$@" "$prompt")
}

THREE_TOOL_PROMPT='Run these three shell commands, one per separate tool call, in order: echo one; echo two; echo three. Do not combine them. Then reply with the word done.'

# start_proxy NAME PORT ARGS...: starts shape-proxy.py in the background (port 0 = ephemeral).
# Sets PROXY_PID and PROXY_PORT; logs to $RT/NAME.proxy.log, records to $RECORD or $RT/NAME.jsonl.
start_proxy() {
	local name=$1 port=$2 portfile i
	shift 2
	portfile="$RT/$name.port"
	rm -f "$portfile"
	python3 -I "$SHAPE_PROXY" --port "$port" --port-file "$portfile" --case "$name" \
		--log "$RT/$name.proxy.log" --record "${RECORD:-$RT/$name.jsonl}" ${SPIKE_UPSTREAM:+--upstream "$SPIKE_UPSTREAM"} "$@" >/dev/null 2>"$RT/$name.proxy.err" &
	PROXY_PID=$!
	track_pid "$PROXY_PID"
	for i in $(seq 1 50); do
		[ -s "$portfile" ] && break
		kill -0 "$PROXY_PID" 2>/dev/null || break
		sleep 0.1
	done
	PROXY_PORT=$(cat "$portfile" 2>/dev/null || true)
	[ -n "$PROXY_PORT" ] || die "proxy $name did not start; see $RT/$name.proxy.err"
}

stop_proxy() {
	kill "$PROXY_PID" 2>/dev/null || true
	wait "$PROXY_PID" 2>/dev/null || true
}

# tty_read VAR PROMPT: read one line from the terminal; empty when there is none.
tty_read() {
	local __var=$1 __ans=""
	printf '%s' "$2" >&2
	if [ -r /dev/tty ] && : 2>/dev/null </dev/tty; then
		read -r __ans </dev/tty || __ans=""
	fi
	printf -v "$__var" '%s' "$__ans"
}

codex_preflight() {
	need codex
	need python3
	need git
	need perl
	echo "codex: $(codex --version 2>&1 | head -1)"
}
