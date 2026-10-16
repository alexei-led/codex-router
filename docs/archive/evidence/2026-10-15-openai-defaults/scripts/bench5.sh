#!/bin/bash
# Usage: bench4.sh VARIANT MODEL EFFORT RUNS -> graded by existing policy/route tests
set -u
L=$(cd "$(dirname "$0")" && pwd); var=$1; model=$2; effort=$3; runs=$4
WT=~/Workspace/codex-router-wt-bench-$var
prompt='Bug report from a user: when a session comes back after roughly four and a half to five minutes of idle time, the router still prices a model switch as if the current model prompt cache were warm, so it charges switch tax and keeps a more expensive model it should have left. With the default config the cache should stop counting as warm earlier than that. Find the root cause, fix it, add a regression test, and run go test ./internal/... and report the result.'
for r in $(seq 1 "$runs"); do
  git -C $WT stash push -q -u -m lab-run >/dev/null 2>&1; git -C $WT checkout -q --detach b5-base
  out=$L/bench-out/B5v2-$var-$r; t0=$(date +%s)
  command codex exec --json -C $WT -s workspace-write --skip-git-repo-check -m "$model" -c model_reasoning_effort="$effort" -o "$out.last" "$prompt" > "$out.jsonl" 2> "$out.err"
  rc=$?; t1=$(date +%s)
  git -C $WT add -A -N; git -C $WT diff b5-base > "$out.patch"
  [ -z "$(git -C $WT diff b5-base --name-only -- '*_test.go')" ] && tf=NO-TEST || tf=has-test
  pkgs="./internal/cost/ ./internal/policy/ ./internal/route/"
  (cd $WT && go test -count=1 $pkgs >/dev/null 2>&1) && own=own-pass || own=own-FAIL
  cp $WT/internal/cost/cost.go "$out.cost.go"
  git -C $WT show b5-base:internal/cost/cost.go > $WT/internal/cost/cost.go
  (cd $WT && go test -count=1 $pkgs >/dev/null 2>&1) && catches=MISSES-BUG || catches=catches-bug
  cp "$out.cost.go" $WT/internal/cost/cost.go
  cp $WT/internal/cost/cost_test.go "$out.cost_test.go"
  git -C $WT show lab-base:internal/cost/cost_test.go > $WT/internal/cost/cost_test.go
  (cd $WT && go test -count=1 -run TestSwitchTaxByCandidateWarmth -v ./internal/cost/ 2>&1 | grep -q -- '--- PASS: TestSwitchTaxByCandidateWarmth/at_TTL_minus_margin') && hidden=hidden-pass || hidden=hidden-FAIL
  cp "$out.cost_test.go" $WT/internal/cost/cost_test.go
  [ -z "$(git -C $WT diff lab-base -- internal/cost/cost.go)" ] && exact=exact || exact=differs
  files=$(git -C $WT diff b5-base --name-only | tr '\n' ',')
  usage=$(python3 -c '
import json,sys
u={}
for l in open(sys.argv[1]):
    try: j=json.loads(l)
    except: continue
    if j.get("type")=="turn.completed": u=j.get("usage",{})
print(u.get("input_tokens",0), u.get("cached_input_tokens",0), u.get("output_tokens",0), u.get("reasoning_output_tokens",0))' "$out.jsonl")
  echo "B5v2 $var $model/$effort run=$r rc=$rc $own $hidden regression=$tf/$catches fix=$exact files=$files secs=$((t1-t0)) in/cached/out/reason=$usage" | tee -a $L/bench-out/summary.txt
done
