#!/bin/bash
# Usage: bench4.sh VARIANT MODEL EFFORT RUNS -> graded by existing policy/route tests
set -u
L=$(cd "$(dirname "$0")" && pwd); var=$1; model=$2; effort=$3; runs=$4
WT=~/Workspace/codex-router-wt-bench-$var
prompt='Tests in ./internal/policy and ./internal/route fail after a recent change. Find and fix the root causes in non-test code only; do not edit any _test.go file. Then run go test ./internal/policy/ ./internal/route/ and report the result.'
for r in $(seq 1 "$runs"); do
  git -C $WT stash push -q -u -m lab-run >/dev/null 2>&1; git -C $WT checkout -q --detach b4-base
  out=$L/bench-out/B4-$var-$r; t0=$(date +%s)
  command codex exec --json -C $WT -s workspace-write --skip-git-repo-check -m "$model" -c model_reasoning_effort="$effort" -o "$out.last" "$prompt" > "$out.jsonl" 2> "$out.err"
  rc=$?; t1=$(date +%s)
  (cd $WT && go test ./internal/policy/ ./internal/route/ >/dev/null 2>&1) && tests=pass || tests=FAIL
  [ -z "$(git -C $WT diff b4-base --name-only -- '*_test.go')" ] && tf=untouched || tf=EDITED
  [ -z "$(git -C $WT diff lab-base -- internal/policy/policy.go)" ] && exact=exact || exact=differs
  files=$(git -C $WT diff b4-base --name-only | tr '\n' ',')
  usage=$(python3 -c '
import json,sys
u={}
for l in open(sys.argv[1]):
    try: j=json.loads(l)
    except: continue
    if j.get("type")=="turn.completed": u=j.get("usage",{})
print(u.get("input_tokens",0), u.get("cached_input_tokens",0), u.get("output_tokens",0), u.get("reasoning_output_tokens",0))' "$out.jsonl")
  echo "B4 $var $model/$effort run=$r rc=$rc tests=$tests testfiles=$tf fix=$exact files=$files secs=$((t1-t0)) in/cached/out/reason=$usage" | tee -a $L/bench-out/summary.txt
done
