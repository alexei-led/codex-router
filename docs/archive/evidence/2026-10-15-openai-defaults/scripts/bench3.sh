#!/bin/bash
# Usage: bench3.sh VARIANT MODEL EFFORT RUNS  -> graded by the hidden test
set -u
L=$(cd "$(dirname "$0")" && pwd); var=$1; model=$2; effort=$3; runs=$4
WT=~/Workspace/codex-router-wt-bench-$var
for r in $(seq 1 "$runs"); do
  git -C $WT stash push -q -u -m lab-run >/dev/null 2>&1; git -C $WT checkout -q --detach lab-base
  out=$L/bench-out/B3-$var-$r; t0=$(date +%s)
  command codex exec --json -C $WT -s workspace-write --skip-git-repo-check -m "$model" -c model_reasoning_effort="$effort" -o "$out.last" "$(cat $L/b3-prompt.txt)" > "$out.jsonl" 2> "$out.err"
  rc=$?; t1=$(date +%s)
  cp $L/size_hidden_test.go $WT/internal/labfix/
  fails=$(cd $WT && go test -tags grade -run TestParseSizeHidden ./internal/labfix/ 2>&1 | grep -c 'ParseSize(')
  build=$(cd $WT && go vet -tags grade ./internal/labfix/ >/dev/null 2>&1 && echo ok || echo BUILD-FAIL)
  usage=$(python3 -c '
import json,sys
u={}
for l in open(sys.argv[1]):
    try: j=json.loads(l)
    except: continue
    if j.get("type")=="turn.completed": u=j.get("usage",{})
print(u.get("input_tokens",0), u.get("cached_input_tokens",0), u.get("output_tokens",0), u.get("reasoning_output_tokens",0))' "$out.jsonl")
  echo "B3 $var $model/$effort run=$r rc=$rc build=$build hidden_fails=$fails/30 secs=$((t1-t0)) in/cached/out/reason=$usage" | tee -a $L/bench-out/summary.txt
done
