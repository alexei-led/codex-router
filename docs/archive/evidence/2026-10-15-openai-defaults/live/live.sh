#!/bin/bash
# Usage: live.sh NAME new|keep PROMPT
#   new:  quit any running Codex, re-export the fixture, launch `codex-router -- --yolo PROMPT`
#         with the installed binary and the user's Codex config, re-lock the panel to the thread.
#   keep: type PROMPT into the running Codex (same thread).
# Writes results/NAME.txt: panel text, route decisions from the router log, Codex screen.
set -u
L=$(cd "$(dirname "$0")" && pwd); S=$(cat "$L/session"); name=$1; mode=$2; prompt=$3
WT=~/Workspace/codex-router-live; REPO=~/Workspace/codex-router; BASE=lab-base
mkdir -p "$L/results"
txt() { agtermctl session text --target "$S" --pane "$1" --lines "${2:-6}"; }
fg() { agtermctl tree --json | python3 -c '
import json,sys
t=json.load(sys.stdin); t=t.get("result",t); t=t.get("tree",t)
for w in t["workspaces"]:
    for s in w["sessions"]:
        if s["id"].startswith(sys.argv[1]): print(" ".join(s.get(sys.argv[2]) or []))' "$S" "$1"; }
typ() { agtermctl session type --target "$S" --pane "$1" "$2" >/dev/null; sleep 0.7; agtermctl session type --target "$S" --pane "$1" $'\r' >/dev/null; }
# A turn is over when Codex's "Worked for 1m 8s • 19:05" line follows the last "• " item.
# The status timer alone is not enough: a Stop hook can keep the turn going with no timer shown.
idle() { agtermctl session text --target "$S" --pane left --lines 60 | python3 -c '
import sys
ls=sys.stdin.read().splitlines()
item=max([i for i,l in enumerate(ls) if l.startswith("• ")], default=-1)
done=max([i for i,l in enumerate(ls) if "Worked for" in l], default=-1)
sys.exit(0 if done>item and any("Ask Codex" in l for l in ls[done:]) else 1)'; }
settled() { for k in 1 2 3; do idle || return 1; sleep 2; done; }
session() { codex-router sessions --json | python3 -c '
import json,sys
for s in json.load(sys.stdin):
    if s["cwd"].endswith("codex-router-live"): print(s["endpoint_id"], s["thread_id"])'; }
export_fixture() {
  rm -rf "$WT"; mkdir -p "$WT"
  gc() { git -C "$WT" -c user.name=lab -c user.email=lab@local commit -qm "$1"; }
  git -C $REPO archive $BASE^ | tar -x -C "$WT" && git -C "$WT" init -q && git -C "$WT" add -A && gc "$(git -C $REPO log -1 --format=%s $BASE^)" \
    && git -C "$WT" rm -q -r . && git -C $REPO archive $BASE | tar -x -C "$WT" && git -C "$WT" add -A && gc "$(git -C $REPO log -1 --format=%s $BASE)"
}
if [ "$mode" = new ]; then
  if fg foreground | grep -q codex; then
    for i in $(seq 1 300); do settled && break; sleep 3; done
    typ left '/quit'
    for i in $(seq 1 30); do fg foreground | grep -q codex || break; sleep 1; done
  fi
  export_fixture || { echo "fixture export failed"; exit 1; }
  printf %s "$prompt" > "$L/prompt.txt"
  typ left "cd $WT && codex-router -- --yolo \"\$(cat $L/prompt.txt)\""
  for i in $(seq 1 60); do read -r ep th < <(session); [ -n "${ep:-}" ] && [ -n "${th:-}" ] && break; sleep 1; done
  echo "$ep" > "$L/endpoint"; echo "$th" > "$L/thread"
  fg splitForeground | grep -q 'codex-router tui' && { agtermctl session type --target "$S" --pane right q >/dev/null; sleep 1; }
  typ right "codex-router tui --no-mouse --session $th"
  start=0
else
  ep=$(cat "$L/endpoint")
  start=$(wc -l < ~/.local/state/codex-router/logs/$ep.log)
  typ left "$prompt"
fi
log=~/.local/state/codex-router/logs/$ep.log
sleep 8
for i in $(seq 1 300); do settled && break; sleep 3; done
sleep 3
{
  echo "## $name ($mode) endpoint=$ep thread=$(cat "$L/thread")"; echo "prompt: $prompt"; echo
  echo "### panel"; txt right 45
  echo "### decisions"
  tail -n +$((start+1)) "$log" | python3 -c '
import sys,json
for l in sys.stdin:
    try: j=json.loads(l)
    except Exception: continue
    if j["msg"] in ("route decision","route command","route manual"):
        print(j["time"][11:19], j.get("turn","")[-6:], "s%s"%j.get("step",""), j.get("tier",""), j.get("activity","") or "-", j.get("reason",""), j.get("model",""), j.get("effort",""), "rw" if j.get("rewritten") else "")
    elif j["level"] in ("WARN","ERROR"): print(j["time"][11:19], j["msg"], {k:v for k,v in j.items() if k not in ("time","level","msg")})'
  echo "### codex"; txt left 30
} > "$L/results/$name.txt"
cat "$L/results/$name.txt"
