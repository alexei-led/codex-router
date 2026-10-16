#!/bin/bash
# Usage: bench.sh TASK VARIANT MODEL EFFORT RUNS
# Runs codex exec RUNS times in ~/Workspace/codex-router-wt-bench-VARIANT, reset to lab-base each run.
set -u
L=$(cd "$(dirname "$0")" && pwd); task=$1; var=$2; model=$3; effort=$4; runs=$5
WT=~/Workspace/codex-router-wt-bench-$var
case $task in
  B1) prompt='Make all tests in ./internal/labfix/ pass by fixing the code (not the tests), and add a test case for x == hi to TestClamp. Then run: go test -tags lab ./internal/labfix/ ; echo EXIT=$? and report the EXIT line.'
      check() { (cd $WT && go test -tags lab ./internal/labfix/ >/dev/null 2>&1 && grep -q '10, 0, 10\|hi' internal/labfix/labfix_test.go); } ;;
  B2) prompt='Run make lint and then go test ./internal/... and report every failure as file:line with a one-line cause. Do not fix anything. End with a line TOTAL=<number of failing tests>.'
      check() { grep -q 'TOTAL=2' "$out.last"; } ;;
esac
for r in $(seq 1 "$runs"); do
  git -C $WT stash push -q -u -m "lab-run" >/dev/null 2>&1; git -C $WT checkout -q --detach lab-base
  out=$L/bench-out/$task-$var-$r
  t0=$(date +%s)
  command codex exec --json -C $WT -s workspace-write --skip-git-repo-check -m "$model" -c model_reasoning_effort="$effort" -o "$out.last" "$prompt" > "$out.jsonl" 2> "$out.err"
  rc=$?; t1=$(date +%s)
  if check; then ok=pass; else ok=FAIL; fi
  usage=$(python3 -c '
import json,sys
u={}
for l in open(sys.argv[1]):
    try: j=json.loads(l)
    except: continue
    if j.get("type")=="turn.completed": u=j.get("usage",{})
print(u.get("input_tokens",0), u.get("cached_input_tokens",0), u.get("output_tokens",0), u.get("reasoning_output_tokens",0))' "$out.jsonl")
  echo "$task $var $model/$effort run=$r rc=$rc $ok secs=$((t1-t0)) in/cached/out/reason=$usage" | tee -a $L/bench-out/summary.txt
done
