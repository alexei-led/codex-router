#!/bin/sh
# Gate spike G5 (plan Task 14): a plugin SessionStart hook that records what
# a stage 2 hook can rely on. It writes one report per run under
# $PLUGIN_DATA/gate/ and prints nothing, so Codex adds no context.
#
# - env: CODEX_HOME, PLUGIN_ROOT, PLUGIN_DATA, XDG_STATE_HOME (values of
#   these names only; never the whole env, which may hold keys)
# - stdin: the hook input's keys and its cwd, session_id, model, source
# - a detached child (stdio redirected) whose pid the gate checks later
# - the session registry: `codex-router sessions --json` and `codex-router
#   hook`, only when XDG_STATE_HOME is a gate run's temp dir, so the spike
#   never reads or prunes the user's real registry
set -u
out=${PLUGIN_DATA:?}/gate
mkdir -p "$out"
run=$out/$(date +%Y%m%dT%H%M%S)-$$
input=$(cat)
router=${CODEX_ROUTER_BIN:-$(command -v codex-router || true)}
{
	echo "pid=$$ pgid=$(ps -o pgid= -p $$ | tr -d ' ')"
	for v in CODEX_HOME PLUGIN_ROOT PLUGIN_DATA XDG_STATE_HOME CODEX_ROUTER_CONTROL SHELL; do
		eval "echo \"$v=\${$v-<unset>}\""
	done
	# PLUGIN_ROOT is <home>/plugins/cache/<marketplace>/<plugin>/<version>.
	echo "derived_home=${PLUGIN_ROOT%%/plugins/cache/*}"
	echo "input_keys=$(printf '%s' "$input" | jq -c 'keys' 2>&1)"
	echo "input=$(printf '%s' "$input" | jq -c '{hook_event_name, source, cwd, session_id, model}' 2>&1)"
	echo "router=${router:-<not found>}"
} >"$run.txt"

nohup sh -c 'sleep 600' </dev/null >/dev/null 2>&1 &
echo "child_pid=$!" >>"$run.txt"

case "${XDG_STATE_HOME-}" in
/private/tmp/codex-router-gate.*)
	if [ -n "$router" ]; then
		"$router" sessions --json >"$run.sessions.json" 2>&1
		echo "sessions_exit=$?" >>"$run.txt"
		start=$(date +%s)
		printf '%s' "$input" | "$router" hook >"$run.hook.json" 2>&1
		echo "hook_exit=$? hook_s=$(($(date +%s) - start))" >>"$run.txt"
	fi
	;;
*) echo "registry=skipped (not a gate run's XDG_STATE_HOME)" >>"$run.txt" ;;
esac
