#!/bin/sh
# Runs codex-router with the given args for this plugin's hooks and skill.
#
# Codex runs hooks with the env of whichever shell started the home's daemon,
# so neither PATH nor XDG_STATE_HOME can be trusted to match the home's
# services. codex-router setup records both in <Codex home>/codex-router.env;
# this script reads them from there. The binary is $CODEX_ROUTER_BIN, else
# the recorded one, else codex-router on PATH. The Codex home is
# $CODEX_HOME, else the one this plugin is installed in, else ~/.codex.
set -u
if [ -z "${CODEX_HOME:-}" ]; then
	case ${PLUGIN_ROOT:-} in
	*/plugins/cache/*) CODEX_HOME=${PLUGIN_ROOT%%/plugins/cache/*} ;;
	*) CODEX_HOME=$HOME/.codex ;;
	esac
fi
export CODEX_HOME
bin=${CODEX_ROUTER_BIN:-}
record=$CODEX_HOME/codex-router.env
if [ -r "$record" ]; then
	while IFS='=' read -r key value; do
		case $key in
		CODEX_ROUTER_BIN) [ -n "$bin" ] || bin=$value ;;
		XDG_STATE_HOME) XDG_STATE_HOME=$value && export XDG_STATE_HOME ;;
		esac
	done <"$record"
fi
bin=${bin:-codex-router}
if ! command -v "$bin" >/dev/null 2>&1; then
	if [ "${1:-}" = hook ]; then
		# A hook must never fail a turn: print the hook's empty answer.
		echo '{}'
		exit 0
	fi
	echo "codex-router: $bin not found; install codex-router and run codex-router setup --home $CODEX_HOME" >&2
	exit 127
fi
exec "$bin" "$@"
