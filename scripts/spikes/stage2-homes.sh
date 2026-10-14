#!/usr/bin/env bash
# Stage 2 test homes (plan Task 13): two throwaway Codex homes, each logged
# in once by the user, for the gate spikes (Task 14) and the stage 2 live
# run (Task 23).
#
#   stage2-homes.sh check             both homes exist (0700) and are logged in
#   stage2-homes.sh reset a|b [PORT [TOKEN_FILE]]
#                                     rewrite only that home's config.toml:
#                                     empty, or the router provider at
#                                     127.0.0.1:PORT (with the token header)
#   stage2-homes.sh serve a|b [PORT]  run `codex-router proxy` on PORT (default:
#                                     any free port), point the home at it and
#                                     wait; on exit stop the proxy and the
#                                     home's Codex daemon, check real configs
#   stage2-homes.sh stop a|b          stop the home's Codex daemon
#
# Never copies auth.json: ChatGPT refresh tokens rotate, so a refresh in a
# copy would log the source home out. The real homes are only hashed.
set -euo pipefail

repo_root=$(cd "$(dirname "$0")/../.." && pwd)
bin="$repo_root/.bin/codex-router"
live_root=$HOME/.cache/codex-router-live
configs=("$HOME/.codex/config.toml" "$HOME/.codex-work/config.toml")

die() {
	echo "stage2-homes: $*" >&2
	exit 1
}

# home maps a|b to its test home and refuses any path outside live_root
# (plan safety rules), so a reset or daemon stop can never reach a real home.
home() {
	case "$1" in
	a | b) ;;
	*) die "unknown home $1: use a or b" ;;
	esac
	local dir=$live_root/home-$1
	mkdir -p "$dir"
	chmod 0700 "$dir"
	dir=$(cd "$dir" && pwd -P)
	case "$dir" in
	"$(cd "$live_root" && pwd -P)"/home-?) echo "$dir" ;;
	*) die "refusing $dir: not a test home under $live_root" ;;
	esac
}

hashes() { shasum -a 256 "${configs[@]}"; }

check() {
	local missing=() h dir
	for h in a b; do
		dir=$(home "$h")
		if CODEX_HOME=$dir codex login status >/dev/null 2>&1; then
			echo "home-$h: logged in ($dir)"
		else
			missing+=("$h")
		fi
	done
	[ ${#missing[@]} -eq 0 ] && return 0
	echo "not logged in; run once (home-b ideally with the work account):" >&2
	for h in "${missing[@]}"; do
		echo "  ! CODEX_HOME=~/.cache/codex-router-live/home-$h codex login" >&2
	done
	exit 3
}

reset() { # home [port [token-file]]
	local dir port=${2:-} token_file=${3:-}
	dir=$(home "$1")
	{
		echo "# Written by scripts/spikes/stage2-homes.sh reset; the next reset replaces it."
		if [ -n "$port" ]; then
			echo 'model_provider = "router"'
			echo
			echo '[model_providers.router]'
			echo 'name = "router"'
			echo "base_url = \"http://127.0.0.1:$port/v1\""
			echo 'wire_api = "responses"'
			echo 'requires_openai_auth = true'
			if [ -n "$token_file" ]; then
				echo "http_headers = { \"X-Codex-Router-Token\" = \"$(tr -d '[:space:]' <"$token_file")\" }"
			fi
		fi
	} >"$dir/config.toml.new"
	chmod 0600 "$dir/config.toml.new"
	mv "$dir/config.toml.new" "$dir/config.toml"
	echo "home-$1: config.toml reset${port:+ (router at 127.0.0.1:$port${token_file:+, token header})}"
}

stop() { # home
	local dir
	dir=$(home "$1")
	CODEX_HOME=$dir codex app-server daemon stop || echo "home-$1: no daemon stopped"
	# daemon stop leaves the managed daemon's pid-update-loop running; it runs
	# from the home's own package dir, so the pattern cannot match elsewhere.
	pkill -f "$dir/packages/app-server-daemon/" || true
}

serve() { # home [port]
	[ -x "$bin" ] || die "build first: make build"
	local h=$1 dir run port=""
	check >/dev/null
	dir=$(home "$h")
	run=$(mktemp -d /private/tmp/codex-router-spike.XXXXXX)
	mkdir -m 0700 "$run/state"
	hashes >"$run/hashes.before"
	# CODEX_HOME: the proxy reads the model cache of the test home, not ~/.codex.
	CODEX_HOME=$dir XDG_STATE_HOME=$run/state "$bin" proxy --port "${2:-0}" >"$run/proxy.log" 2>&1 &
	local pid=$!
	# shellcheck disable=SC2064 # expand now: the trap runs after locals are gone
	trap "kill $pid 2>/dev/null || true; stop $h; verify $run/hashes.before" EXIT
	trap 'exit 130' INT TERM
	for _ in $(seq 50); do
		port=$(sed -n 's/^proxy listening on 127\.0\.0\.1:\([0-9]*\);.*/\1/p' "$run/proxy.log")
		[ -n "$port" ] && break
		kill -0 "$pid" 2>/dev/null || die "proxy exited: $(cat "$run/proxy.log")"
		sleep 0.1
	done
	[ -n "$port" ] || die "proxy did not report its port"
	reset "$h" "$port"
	echo "proxy pid $pid on 127.0.0.1:$port, log $run/proxy.log; Ctrl-C stops it and the home's daemon"
	wait "$pid"
}

verify() { # hashes-before-file
	if diff -q "$1" <(hashes) >/dev/null; then
		echo "real config.toml files unchanged"
	else
		echo "stage2-homes: a real config.toml changed during the run" >&2
		exit 1
	fi
}

case "${1:-}" in
check) check ;;
reset) reset "${2:?home}" "${3:-}" "${4:-}" ;;
serve) serve "${2:?home}" "${3:-}" ;;
stop) stop "${2:?home}" ;;
*) die "usage: stage2-homes.sh check|reset a|b [PORT [TOKEN_FILE]]|serve a|b [PORT]|stop a|b" ;;
esac
