#!/usr/bin/env bash
# Stage 1 live test harness (plan Task 12): throwaway repos and worktrees, an
# agterm workspace with five real Codex sessions under two homes, a locked
# panel in session 1's split and an all-sessions panel in a sixth session.
#
#   stage1.sh up                  set everything up; prints the run dir
#   stage1.sh env                 print the run's exports (eval it)
#   stage1.sh capture NAME S [P]  save session S's screen (pane P) as NAME.txt
#   stage1.sh focus LABEL         press ] in the all-sessions panel until
#                                 LABEL is focused
#   stage1.sh down                close sessions, stop leftovers, check configs
#
# The run lives in $RUN (default: the last `up`). Every process gets the
# run's own XDG_STATE_HOME, so its registry and logs never mix with the
# user's. Codex runs only through codex-router (-c overrides, private
# app-server); the real config.toml files are hashed at `up` and checked at
# `down`. Needs agtermctl (run inside agterm), jq, git, fish, codex.
set -euo pipefail

repo_root=$(cd "$(dirname "$0")/../.." && pwd)
bin="$repo_root/.bin/codex-router"
fish=$(command -v fish)
prefix=/private/tmp/codex-router-live.
last_run_file=/private/tmp/codex-router-live.last
workspace_name=router-live
configs=("$HOME/.codex/config.toml" "$HOME/.codex-work/config.toml")

die() {
	echo "stage1: $*" >&2
	exit 1
}

# guard refuses any path outside this script's mktemp prefix (plan safety
# rules), so cleanup can never reach a real home.
guard() {
	case "$1" in
	"$prefix"*) ;;
	*) die "refusing to touch $1: not under $prefix*" ;;
	esac
}

load() {
	RUN=${RUN:-$(cat "$last_run_file" 2>/dev/null || true)}
	[ -n "$RUN" ] && [ -f "$RUN/run.env" ] || die "no run; use: stage1.sh up"
	guard "$RUN"
	# shellcheck disable=SC1091
	. "$RUN/run.env"
}

hashes() { shasum -a 256 "${configs[@]}"; }

new_session() { # name cwd command
	agtermctl session new --workspace "$WS" --cwd "$2" --name "$1" --no-select --wait \
		--command "$3" --json | jq -r .result.id
}

# router_cmd builds the fish login command that starts codex-router, so the
# classifier key from the user's fish config is in env (plan Task 12). The
# runtime dir under $RUN puts the run dir in the command lines of the
# private app-server and the Codex TUI, which down needs after a kill -9.
router_cmd() { # extra-fish-statements
	echo "$fish -l -c 'set -gx XDG_STATE_HOME $RUN/state; set -gx XDG_RUNTIME_DIR $RUN/rt; $1 exec $bin'"
}

wait_for() { # seconds description command...
	local until=$((SECONDS + $1)) what=$2
	shift 2
	until "$@"; do
		((SECONDS < until)) || die "timed out waiting for $what"
		sleep 2
	done
}

screen_has() { agtermctl session text --target "$1" 2>/dev/null | grep -q "$2"; }

listed() { [ "$(XDG_STATE_HOME="$RUN/state" "$bin" sessions --json | jq length)" -ge "$1" ]; }

make_repo() { # dir branch...: a repo on main with a linked worktree per branch
	local dir=$1
	shift
	git init -q -b main "$dir"
	git -C "$dir" -c user.name=live -c user.email=live@example.invalid commit -q --allow-empty -m init
	for b in "$@"; do
		git -C "$dir" worktree add -q -b "$b" "$dir-$b"
	done
}

up() {
	[ -x "$bin" ] || die "build first: make build"
	RUN=$(mktemp -d "${prefix}XXXXXX")
	mkdir -m 0700 "$RUN/state" "$RUN/rt"
	echo "$RUN" >"$last_run_file"
	make_repo "$RUN/alpha" feat-a feat-b
	make_repo "$RUN/beta" feat-c feat-d
	WS=$(agtermctl workspace new "$workspace_name" --json | jq -r .result.id)
	{
		echo "RUN=$RUN"
		echo "WS=$WS"
		hashes | sed 's/^/# before: /'
	} >"$RUN/run.env"

	S1=$(new_session s1 "$RUN/alpha-feat-a" "$(router_cmd '')")
	S2=$(new_session s2 "$RUN/alpha-feat-b" "$(router_cmd '')")
	S3=$(new_session s3 "$RUN/beta" "$(router_cmd '')")
	S4=$(new_session s4 "$RUN/beta-feat-c" "$(router_cmd "set -gx CODEX_HOME $HOME/.codex-work;")")
	S5=$(new_session s5 "$RUN/beta-feat-d" "$(router_cmd "set -gx CODEX_HOME $HOME/.codex-work;")")
	printf 'S%d=%s\n' 1 "$S1" 2 "$S2" 3 "$S3" 4 "$S4" 5 "$S5" >>"$RUN/run.env"

	# A session joins the list at its first prompt.
	for s in "$S1" "$S2" "$S3" "$S4" "$S5"; do
		wait_for 60 "codex prompt in $s" screen_has "$s" "Ask Codex"
		agtermctl session type --target "$s" "Reply with the single word: ok"$'\r' >/dev/null
	done
	wait_for 240 "5 listed sessions" listed 5

	label=$(XDG_STATE_HOME="$RUN/state" "$bin" sessions --json |
		jq -r --arg cwd "$RUN/alpha-feat-a" '.[] | select(.cwd == $cwd) | .label')
	[ -n "$label" ] || die "session 1 is not listed"
	agtermctl session split visibility on --axis vertical --target "$S1" >/dev/null
	sleep 2
	agtermctl session type --target "$S1" --pane split \
		"env XDG_STATE_HOME=$RUN/state $bin tui --session '$label'"$'\r' >/dev/null
	S6=$(new_session s6-panel "$RUN" "$fish -l -c 'set -gx XDG_STATE_HOME $RUN/state; exec $bin tui'")
	echo "S6=$S6" >>"$RUN/run.env"
	echo "$RUN"
}

capture() { # name session [pane]
	load
	local out=${OUT:?set OUT to the evidence dir} args=(--target "$2")
	[ -n "${3:-}" ] && args+=(--pane "$3")
	agtermctl session text "${args[@]}" | sed -e "s#$RUN#\$RUN#g" -e "s#$HOME#~#g" >"$out/$1.txt"
	echo "$out/$1.txt"
}

focus() { # label
	load
	local first
	for _ in 1 2 3 4 5 6 7 8 9 10; do
		# The top bar starts with the focused label, maybe with an attention mark.
		first=$(agtermctl session text --target "$S6" | grep -m1 '◂' |
			sed -e 's/^◂ ● //' -e 's/  ●.*//' -e 's/ *▸.*//' -e 's/ [◐‖↑?]$//')
		[ "$first" = "$1" ] && return 0
		agtermctl session type --target "$S6" ']' >/dev/null
		sleep 1
	done
	die "could not focus $1"
}

down() {
	load
	for s in ${S6:-} ${S5:-} ${S4:-} ${S3:-} ${S2:-} ${S1:-}; do
		agtermctl session close --target "$s" >/dev/null 2>&1 || true
	done
	agtermctl workspace delete --target "$WS" >/dev/null 2>&1 || true
	sleep 3
	# A router killed with -9 orphans its Codex TUI and app-server; their
	# command lines name the run's runtime or state dir. The patterns start
	# at the program, so they never match the caller's own pipeline (a sed
	# or this script naming $RUN).
	pkill -f "^/usr/bin/login .*$RUN/state" 2>/dev/null || true
	pkill -f "^([^ ]*/)?(codex-router|codex)( .*)? [^ ]*$RUN/(rt|state)" 2>/dev/null || true
	if ! diff <(grep '^# before: ' "$RUN/run.env" | sed 's/^# before: //') <(hashes); then
		die "a real config.toml changed during the run"
	fi
	echo "config.toml unchanged; run dir $RUN kept for logs (remove it when done)"
}

case "${1:-}" in
up) up ;;
env)
	load
	echo "export RUN=$RUN XDG_STATE_HOME=$RUN/state WS=$WS S1=$S1 S2=$S2 S3=$S3 S4=$S4 S5=$S5 S6=${S6:-}"
	;;
capture) capture "${2:?name}" "${3:?session}" "${4:-}" ;;
focus) focus "${2:?label}" ;;
down) down ;;
*) die "usage: stage1.sh up|env|capture NAME SESSION [PANE]|focus LABEL|down" ;;
esac
