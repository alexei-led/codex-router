#!/bin/bash
# Usage: case.sh NAME A|B|keep PROMPT
#   A|B: quit any running Codex, reset the worktree to lab-base, launch a fresh
#        codex-router session with PROMPT, re-lock the panel to it.
#   keep: send PROMPT to the running Codex (same thread).
set -u
L=$(cd "$(dirname "$0")" && pwd); S=$(cat "$L/bench"); name=$1; mode=$2; prompt=$3
WT=~/Workspace/codex-router-wt-routing-lab
txt() { agtermctl session text --target "$S" --pane "$1" --lines "${2:-6}"; }
fg() { agtermctl tree --json | python3 -c '
import json,sys
t=json.load(sys.stdin); t=t.get("result",t); t=t.get("tree",t)
for w in t["workspaces"]:
    for s in w["sessions"]:
        if s["id"].startswith(sys.argv[1]): print(" ".join(s.get(sys.argv[2]) or []))' "$S" "$1"; }
typ() { agtermctl session type --target "$S" --pane "$1" "$2" >/dev/null; sleep 0.7; agtermctl session type --target "$S" --pane "$1" $'\r' >/dev/null; }
idle() { local t; t=$(txt left); grep -q ' Ready ' <<<"$t" && ! grep -q 'Working\|esc to interrupt' <<<"$t"; }
session() { codex-router sessions --json | python3 -c '
import json,sys
for s in json.load(sys.stdin):
    if s["cwd"].endswith("routing-lab"): print(s["endpoint_id"], s["thread_id"])'; }
if [ "$mode" != keep ]; then
  if fg foreground | grep -q codex; then
    typ left '/quit'
    for i in $(seq 1 30); do fg foreground | grep -q codex || break; sleep 1; done
  fi
  git -C $WT stash push -q -u -m "lab-run" >/dev/null 2>&1; git -C $WT checkout -q --detach lab-base
  printf %s "$prompt" > "$L/prompt.txt"
  typ left "$L/launch.sh $mode (cat $L/prompt.txt)"
  for i in $(seq 1 40); do read -r ep th < <(session); [ -n "${ep:-}" ] && break; sleep 1; done
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
for i in $(seq 1 300); do idle && break; sleep 3; done
sleep 2
{
  echo "## $name ($mode) endpoint=$ep thread=$(cat "$L/thread")"; echo "prompt: $prompt"; echo
  echo "### TUI"; txt right 40
  echo "### decisions"
  tail -n +$((start+1)) "$log" | python3 -c '
import sys,json
for l in sys.stdin:
    j=json.loads(l)
    if j["msg"] in ("route decision","route command","route manual"):
        print(j["time"][11:19], j.get("turn","")[-6:], "s%s"%j.get("step",""), j.get("tier",""), j.get("activity","") or "-", j.get("reason",""), j.get("model",""), j.get("effort",""), "rw" if j.get("rewritten") else "")
    elif j["level"] in ("WARN","ERROR"): print(j["time"][11:19], j["msg"], {k:v for k,v in j.items() if k not in ("time","level","msg")})'
  echo "### codex"; txt left 30
} > "$L/results/$name.txt"
cat "$L/results/$name.txt"
