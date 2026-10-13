#!/usr/bin/env bash
# Spike 1a: two clients on one unix app-server, TUI via --remote, footer mid-turn, parity.
set -euo pipefail

# shellcheck source=lib.sh
. "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

handle_help 1a-two-clients.sh "Starts a private codex app-server on a unix socket, attaches the TUI with
'codex --remote', sends thread/settings/update mid-turn from a second client, and
asks you to confirm feature parity. Interactive: reads answers from /dev/tty." "$@"

CHECKLIST=(
	"footer repaints mid-turn (model-with-reasoning and context-window-size)"
	"/model works"
	"/status works"
	"approvals prompt appears and works"
	"shell tool runs"
	"image paste works"
	"Esc interrupts a running turn"
	"/compact works"
)

codex_preflight
need uv

RT=$(make_rt cr-spike-1a)
REPO="$RT/repo"
SOCK="$RT/app.sock"
CLIENT_LOG="$RT/1a.log"
SPLIT_OPEN=0
HAVE_AGTERM=0
make_repo "$REPO"
: >"$CLIENT_LOG"

cleanup() {
	trap - EXIT INT TERM
	kill_children
	if [ "$SPLIT_OPEN" = 1 ]; then
		agtermctl session split close --target "$AGTERM_SESSION_ID" >/dev/null 2>&1 || true
	fi
}
trap cleanup EXIT INT TERM

log_line() {
	echo "t=$SECONDS $*" >>"$RT/1a-script.log"
}

wait_for_log() {
	# wait_for_log PATTERN SECS: poll the client log; returns 1 on timeout.
	for _ in $(seq 1 $(($2 * 2))); do
		grep -q -- "$1" "$CLIENT_LOG" 2>/dev/null && return 0
		sleep 0.5
	done
	return 1
}

capture() {
	# capture NAME: save the TUI split screen when agterm drives it.
	[ "$HAVE_AGTERM" = 1 ] || return 0
	agtermctl session text --pane split --target "$AGTERM_SESSION_ID" >"$RT/$1.txt" 2>"$RT/$1.err" || true
}

ask_yn() {
	# ask_yn LABEL: record y/n/s from the user.
	local ans
	tty_read ans "  $1 [y/n/s=skip]: "
	ans=${ans:-s}
	echo "$ans  $1" >>"$RT/1a-checklist.txt"
}

echo "== 1a: evidence dir $RT"
echo "starting: codex app-server --listen unix://$SOCK"
codex app-server --listen "unix://$SOCK" >"$RT/appserver.log" 2>&1 &
track_pid $!
for _ in $(seq 1 60); do
	[ -S "$SOCK" ] && break
	sleep 0.25
done
[ -S "$SOCK" ] || die "app-server did not create $SOCK; see $RT/appserver.log"
log_line "app-server up"

REMOTE_CMD="cd '$REPO' && codex --remote unix://$SOCK"
TASK_PROMPT='Run this shell task as four separate tool calls, one step each, in order: sleep 5 && echo step1; sleep 5 && echo step2; sleep 5 && echo step3; sleep 5 && echo step4. Then reply done.'

if command -v agtermctl >/dev/null 2>&1 && [ -n "${AGTERM_SESSION_ID:-}" ]; then
	if agtermctl session split visibility on --axis vertical --target "$AGTERM_SESSION_ID" >"$RT/split-open.txt" 2>&1; then
		SPLIT_OPEN=1
		HAVE_AGTERM=1
	fi
fi

uv run --with websockets "$APPSERVER_CLIENT" --sock "$SOCK" --log "$CLIENT_LOG" \
	--update-model gpt-6-luna --update-effort low --update-delay 3 >"$RT/client.out" 2>&1 &
track_pid $!

if [ "$HAVE_AGTERM" = 1 ]; then
	echo "agterm split opened; attaching the TUI there"
	agtermctl session type --pane split --target "$AGTERM_SESSION_ID" "$REMOTE_CMD"$'\n' >/dev/null
	tty_read _ "Answer any trust prompt in the split, wait for the TUI input box, then press Enter here: "
	agtermctl session type --pane split --target "$AGTERM_SESSION_ID" "$TASK_PROMPT"$'\n' >/dev/null
else
	cat <<EOF

agterm not available (agtermctl missing or AGTERM_SESSION_ID unset): manual mode.
In ANOTHER terminal run:
  $REMOTE_CMD
Answer any trust prompt, then paste this prompt into the TUI and submit it:
  $TASK_PROMPT
Watch the footer while the turn runs: it should change to gpt-6-luna low about 3 s after the turn starts.
EOF
fi

echo "waiting for turn/started (up to 180 s)..."
if wait_for_log "event=notify method=turn/started" 180; then
	sleep 1
	capture 1a-before
	log_line "captured before"
else
	echo "turn/started not seen; see $CLIENT_LOG"
fi
if wait_for_log "event=update_sent" 60; then
	sleep 3
	capture 1a-after
	log_line "captured after"
	wait_for_log "event=notify method=thread/settings/updated" 10 || true
else
	echo "update not sent; see $CLIENT_LOG"
fi
wait_for_log "event=notify method=turn/completed" 90 || true

echo
echo "In the TUI run: /model gpt-6-sol   (pick it, confirm), then press Enter here."
tty_read _ "Press Enter after /model finished (Enter alone also skips): "
sleep 2

echo
echo "Parity checklist: answer in the TUI, then y/n/s here."
: >"$RT/1a-checklist.txt"
for item in "${CHECKLIST[@]}"; do
	ask_yn "$item"
done
ask_yn "footer repainted at the NEXT turn even if not mid-turn"

echo
echo "== 1a summary"
echo "transport:   $(grep -o 'event=transport[_a-z]* kind=[a-z]*' "$CLIENT_LOG" | tr '\n' ';')"
echo "notifications (method, in order):"
grep -oE 'event=notify method=[^ ]*( model=[^ ]* effort=[^ ]*)?' "$CLIENT_LOG" | sed 's/event=notify method=//' | uniq -c | sed 's/^/  /' || true
echo "update:      $(grep 'event=update_sent' "$CLIENT_LOG" | head -1)"
echo "settings/updated lines:"
grep 'method=thread/settings/updated' "$CLIENT_LOG" | sed 's/^/  /' || true
echo "checklist:"
sed 's/^/  /' "$RT/1a-checklist.txt"
if [ "$HAVE_AGTERM" = 1 ]; then
	echo "screen captures: $RT/1a-before.txt  $RT/1a-after.txt"
fi
echo
echo "evidence: $RT"
