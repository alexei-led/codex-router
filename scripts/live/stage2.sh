#!/usr/bin/env bash
# Stage 2 live test harness (plan Task 23): the two logged-in test homes from
# Task 13 run through codex-router setup (edge + brain under launchd) with the
# Codex plugin installed, and an agterm workspace with five plain `codex`
# sessions (three in home A, two in home B), a locked panel in session 1's
# split and an all-sessions panel in a sixth session.
#
#   stage2.sh prep                reset the test homes' config.toml, install the
#                                 plugin, make the run dir; prints it
#   stage2.sh trust a|b           open codex in the home, trust its plugin hooks
#                                 (/hooks, t), quit, stop the home's daemon
#   stage2.sh up                  setup both homes, start the sessions and panels
#   stage2.sh env                 print the run's exports (eval it)
#   stage2.sh capture NAME S [P]  save session S's screen (pane P) as NAME.txt
#   stage2.sh pid a|b edge|brain  pid of a home's running edge or brain
#   stage2.sh undo a|b            setup --undo and compare config.toml with the
#                                 setup backup
#   stage2.sh down                close sessions, undo what is left, stop the
#                                 test daemons, check the real configs
#
# The run lives in $RUN (default: the last `prep`). Every process gets the
# run's own XDG_STATE_HOME; setup records it in the units. The router config
# is $RUN/router.json; its classifier reads the key from the envFile given as
# $ENV_FILE at prep (the path only: this script never reads that file).
# Setup never runs on a real home: home paths are checked against the test
# home root. Needs agtermctl (run inside agterm), jq, git, fish, codex.
set -euo pipefail

repo_root=$(cd "$(dirname "$0")/../.." && pwd)
bin="$repo_root/.bin/codex-router"
homes_script="$repo_root/scripts/spikes/stage2-homes.sh"
fish=$(command -v fish)
prefix=/private/tmp/codex-router-live2.
last_run_file=/private/tmp/codex-router-live2.last
live_root=$HOME/.cache/codex-router-live
workspace_name=router-live-2
configs=("$HOME/.codex/config.toml" "$HOME/.codex-work/config.toml")
agents=$HOME/Library/LaunchAgents
label_prefix=com.github.alexei-led.codex-router.

die() {
	echo "stage2: $*" >&2
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

# home_dir maps a|b to its test home and refuses anything else.
home_dir() {
	case "$1" in
	a | b) ;;
	*) die "unknown home $1: use a or b" ;;
	esac
	local dir
	dir=$(cd "$live_root/home-$1" && pwd -P) || die "no test home $1"
	case "$dir" in
	"$(cd "$live_root" && pwd -P)"/home-[ab]) echo "$dir" ;;
	*) die "refusing $dir: not a test home under $live_root" ;;
	esac
}

load() {
	RUN=${RUN:-$(cat "$last_run_file" 2>/dev/null || true)}
	[ -n "$RUN" ] && [ -f "$RUN/run.env" ] || die "no run; use: stage2.sh prep"
	guard "$RUN"
	# shellcheck disable=SC1091
	. "$RUN/run.env"
}

hashes() { shasum -a 256 "${configs[@]}"; }

router() { XDG_STATE_HOME="$RUN/state" "$bin" --config "$RUN/router.json" "$@"; }

home_id() { router service status --home "$(home_dir "$1")" | sed -n 's/.*(id \([0-9a-f]*\)).*/\1/p'; }

new_session() { # name cwd command
	agtermctl session new --workspace "$WS" --cwd "$2" --name "$1" --no-select --wait \
		--command "$3" --json | jq -r .result.id
}

# codex_cmd runs plain codex in a test home under a fish login shell, with
# the run's state dir; the home's daemon inherits that env.
codex_cmd() { # home-dir
	echo "$fish -l -c 'set -gx XDG_STATE_HOME $RUN/state; set -gx CODEX_HOME $1; exec codex'"
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

# reset_home writes a test home's config.toml: only trust entries for the
# run's repos and worktrees (trust is per project root, not inherited from
# the run dir), so Codex asks nothing that would write config.toml mid-run.
reset_home() { # a|b
	local dir d
	dir=$(home_dir "$1")
	"$homes_script" stop "$1" >/dev/null 2>&1 || true
	{
		echo "# Written by scripts/live/stage2.sh prep; the next prep replaces it."
		for d in alpha alpha-feat-a alpha-feat-b beta beta-feat-c beta-feat-d; do
			echo
			echo "[projects.\"$RUN/$d\"]"
			echo 'trust_level = "trusted"'
		done
	} >"$dir/config.toml.new"
	chmod 0600 "$dir/config.toml.new"
	mv "$dir/config.toml.new" "$dir/config.toml"
}

install_plugin() { # a|b
	local dir
	dir=$(home_dir "$1")
	CODEX_HOME=$dir codex plugin marketplace add "$repo_root" >/dev/null
	CODEX_HOME=$dir codex plugin add codex-router@codex-router >/dev/null
}

prep() {
	[ -x "$bin" ] || die "build first: make build"
	[ -n "${ENV_FILE:-}" ] || die "set ENV_FILE to the classifier key file (path only)"
	"$homes_script" check
	RUN=$(mktemp -d "${prefix}XXXXXX")
	RUN=$(cd "$RUN" && pwd -P)
	mkdir -m 0700 "$RUN/state"
	echo "$RUN" >"$last_run_file"
	jq -n --arg f "$ENV_FILE" '{classifiers: {jev: {
		label: "Jev", api: "system-one", endpoint: "https://api.typesafe.ai/v1/systemone",
		model: "jev-1.13.0", keyEnv: "TYPESAFE_API_KEY", envFile: $f, timeoutMs: 1500}}}' >"$RUN/router.json"
	make_repo "$RUN/alpha" feat-a feat-b
	make_repo "$RUN/beta" feat-c feat-d
	WS=$(agtermctl workspace new "$workspace_name" --json | jq -r .result.id)
	{
		echo "RUN=$RUN"
		echo "WS=$WS"
		echo "HOME_A=$(home_dir a)"
		echo "HOME_B=$(home_dir b)"
		hashes | sed 's/^/# before: /'
	} >"$RUN/run.env"
	for h in a b; do
		reset_home "$h"
		install_plugin "$h"
	done
	echo "$RUN"
}

trust() { # a|b
	load
	local dir s
	dir=$(home_dir "$1")
	s=$(new_session "trust-$1" "$RUN/alpha" "$(codex_cmd "$dir")")
	# Codex asks about new plugin hooks at start: 2 = trust all and continue.
	wait_for 90 "hooks review" screen_has "$s" "Hooks need review"
	OUT=${OUT:-$RUN} capture "trust-$1" "$s" >/dev/null
	agtermctl session type --target "$s" "2" >/dev/null
	sleep 1
	agtermctl session type --target "$s" $'\r' >/dev/null
	wait_for 60 "codex prompt" screen_has "$s" "Ask Codex"
	agtermctl session close --target "$s" >/dev/null
	"$homes_script" stop "$1" >/dev/null 2>&1 || true
	grep -c 'trusted_hash' "$dir/config.toml" | sed "s/^/home-$1 trusted hooks: /"
}

up() {
	load
	for h in a b; do
		router setup --home "$(home_dir "$h")" >"$RUN/setup-$h.txt"
		echo "ID_${h^^}=$(home_id "$h")" >>"$RUN/run.env"
	done
	# shellcheck disable=SC1091
	. "$RUN/run.env"
	S1=$(new_session a1 "$RUN/alpha-feat-a" "$(codex_cmd "$HOME_A")")
	S2=$(new_session a2 "$RUN/alpha-feat-b" "$(codex_cmd "$HOME_A")")
	S3=$(new_session a3 "$RUN/alpha" "$(codex_cmd "$HOME_A")")
	S4=$(new_session b1 "$RUN/beta-feat-c" "$(codex_cmd "$HOME_B")")
	S5=$(new_session b2 "$RUN/beta-feat-d" "$(codex_cmd "$HOME_B")")
	printf 'S%d=%s\n' 1 "$S1" 2 "$S2" 3 "$S3" 4 "$S4" 5 "$S5" >>"$RUN/run.env"

	# A session joins the list at its first prompt.
	for s in "$S1" "$S2" "$S3" "$S4" "$S5"; do
		wait_for 90 "codex prompt in $s" screen_has "$s" "Ask Codex"
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

units() { # home-id: how many launchd agent files the home has
	local n=0 f
	for f in "$agents/$label_prefix$1".*.plist; do
		[ -e "$f" ] && n=$((n + 1))
	done
	echo "$n"
}

pid() { # a|b edge|brain
	load
	local id
	id=$(home_id "$1")
	launchctl print "gui/$(id -u)/$label_prefix$id.$2" 2>/dev/null | sed -n 's/^\tpid = //p'
}

undo() { # a|b
	load
	local dir backup
	dir=$(home_dir "$1")
	[ -f "$agents/$label_prefix$(home_id "$1").edge.plist" ] || {
		echo "home-$1: not set up"
		return 0
	}
	# The oldest backup is setup's; undo writes another one.
	backup=$(find "$dir" -maxdepth 1 -name 'config.toml.codex-router-*.bak' | sort | head -1)
	{
		router setup --undo --home "$dir"
		if cmp -s "$backup" "$dir/config.toml"; then
			echo "home-$1: config.toml byte-identical to the setup backup $(basename "$backup")"
		else
			echo "home-$1: config.toml differs from the setup backup $(basename "$backup"):"
			diff "$backup" "$dir/config.toml" || true
		fi
		echo "home-$1 units left: $(units "$(home_id "$1")")"
	} 2>&1 | tee "$RUN/undo-$1.txt"
}

down() {
	load
	for s in ${S6:-} ${S5:-} ${S4:-} ${S3:-} ${S2:-} ${S1:-}; do
		agtermctl session close --target "$s" >/dev/null 2>&1 || true
	done
	agtermctl workspace delete --target "$WS" >/dev/null 2>&1 || true
	sleep 3
	for h in a b; do
		undo "$h"
		"$homes_script" stop "$h" >/dev/null 2>&1 || true
		CODEX_HOME=$(home_dir "$h") codex plugin remove codex-router@codex-router >/dev/null 2>&1 || true
		CODEX_HOME=$(home_dir "$h") codex plugin marketplace remove codex-router >/dev/null 2>&1 || true
		"$homes_script" reset "$h" >/dev/null
		find "$(home_dir "$h")" -maxdepth 1 -name 'config.toml.codex-router-*.bak' -delete
	done
	# Session wrappers left by a closed session; the pattern is anchored so
	# it never matches the caller's own pipeline (a sed naming $RUN).
	pkill -f "^/usr/bin/login .*$RUN/state" 2>/dev/null || true
	local real
	for real in "$HOME/.codex" "$HOME/.codex-work"; do
		local id
		id=$(CODEX_HOME=$real "$bin" service status --home "$real" | sed -n 's/.*(id \([0-9a-f]*\)).*/\1/p')
		if [ "$(units "$id")" -ne 0 ]; then
			die "a codex-router unit exists for the real home $real"
		fi
	done
	echo "no codex-router units for the real homes"
	if ! diff <(grep '^# before: ' "$RUN/run.env" | sed 's/^# before: //') <(hashes); then
		die "a real config.toml changed during the run"
	fi
	echo "config.toml unchanged; run dir $RUN kept for logs (remove it when done)"
}

case "${1:-}" in
prep) prep ;;
trust) trust "${2:?home}" ;;
up) up ;;
env)
	load
	echo "export RUN=$RUN XDG_STATE_HOME=$RUN/state WS=$WS HOME_A=$HOME_A HOME_B=$HOME_B ID_A=${ID_A:-} ID_B=${ID_B:-} S1=${S1:-} S2=${S2:-} S3=${S3:-} S4=${S4:-} S5=${S5:-} S6=${S6:-}"
	;;
capture) capture "${2:?name}" "${3:?session}" "${4:-}" ;;
pid) pid "${2:?home}" "${3:?edge|brain}" ;;
undo) undo "${2:?home}" ;;
down) down ;;
*) die "usage: stage2.sh prep|trust a|b|up|env|capture NAME SESSION [PANE]|pid a|b edge|brain|undo a|b|down" ;;
esac
