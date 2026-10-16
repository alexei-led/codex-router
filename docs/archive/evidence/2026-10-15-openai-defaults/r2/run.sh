#!/bin/bash
# Usage: run.sh VARIANT_DIR MODEL EFFORT RUNS TASK...
# Each run: reset the worktree to the task base, codex exec, grade, append to results.txt.
set -u
R=$(cd "$(dirname "$0")" && pwd); dir=$1; model=$2; effort=$3; runs=$4; shift 4
REPO=${REPO:?set REPO to a codex-router checkout with the r2-review, r2-debug and r2-ops tags}
. "$R/prompts.sh"
mkdir -p "$R/out"
usage() { python3 -c '
import json,sys
u={}
for l in open(sys.argv[1]):
    try: j=json.loads(l)
    except Exception: continue
    if j.get("type")=="turn.completed": u=j.get("usage",{})
p={"gpt-6-luna":(0.1,0.01,0.5),"gpt-6.1-sol":(2,0.1,10)}[sys.argv[2]]
i,c,o=u.get("input_tokens",0),u.get("cached_input_tokens",0),u.get("output_tokens",0)
print("usd=%.4f in=%d out=%d reason=%d" % (((i-c)*p[0]+c*p[1]+o*p[2])/1e6, i, o, u.get("reasoning_output_tokens",0)))' "$1" "$model"; }
gotest() { (cd $WT && go test -count=1 "$@" >/dev/null 2>&1); }
for task in "$@"; do
  case $task in review) base=r2-review;; debug) base=r2-debug;; ops) base=r2-ops;; *) base=main;; esac
  pv="P_$task"
  for r in $(seq 1 "$runs"); do
    # Fresh export per run: no shared worktree, index or stash between runs.
    WT=$R/wt/$task-$dir-$r; rm -rf "$WT"; mkdir -p "$WT"
    # Review reads `git show HEAD`, so it gets the planted commit on top of its parent.
    gc() { git -C "$WT" -c user.name=lab -c user.email=lab@local commit -qm "$1"; }
    if [ $task = review ]; then
      git -C $REPO archive $base^ | tar -x -C "$WT" && git -C "$WT" init -q && git -C "$WT" add -A && gc base \
        && git -C "$WT" rm -q -r . && git -C $REPO archive $base | tar -x -C "$WT" && git -C "$WT" add -A \
        && gc "$(git -C $REPO log -1 --format=%B $base)"
    else
      git -C $REPO archive $base | tar -x -C "$WT" && git -C "$WT" init -q && git -C "$WT" add -A && gc base
    fi || { echo "$task $dir r=$r SETUP-FAIL" | tee -a $R/results.txt; continue; }
    out=$R/out/$task-$dir-$r; t0=$(date +%s)
    command codex exec --json -C $WT -s workspace-write --skip-git-repo-check -m "$model" -c model_reasoning_effort="$effort" -o "$out.last" "${!pv}" < /dev/null > "$out.jsonl" 2> "$out.err"
    rc=$?; secs=$(( $(date +%s) - t0 ))
    git -C $WT add -A -N; git -C $WT diff HEAD > "$out.patch"
    case $task in
      review)
        cp $WT/review.json "$out.review.json" 2>/dev/null
        grade=$(python3 - "$out.review.json" <<'PY'
import json,sys,re
try: items=json.load(open(sys.argv[1]))
except Exception: print("score=0/5 fp=? parse-fail"); sys.exit()
defects={"USD":"D1","SwitchTax":"D2","Forget":"D3","massAbove":"D4","Decide":"D5"}
found=set(); fp=0
for it in items:
    fn=str(it.get("function","")); txt=fn+" "+str(it.get("problem",""))
    hit=[d for f,d in defects.items() if re.search(r'\b'+f+r'\b',fn)] or [d for f,d in defects.items() if re.search(r'\b'+f+r'\b',txt) and f!="Decide"]
    if hit: found.update(hit)
    else: fp+=1
print("score=%d/5 fp=%d found=%s" % (len(found),fp,",".join(sorted(found))))
PY
);;
      debug)
        gotest ./internal/... && own=own-pass || own=own-FAIL
        git -C $REPO show main:internal/policy/policy_test.go > $WT/internal/policy/policy_test.go
        git -C $REPO show main:internal/route/engine_test.go > $WT/internal/route/engine_test.go
        cp $R/cost_hidden_test.go.txt $WT/internal/cost/cost_hidden_test.go
        gotest -run Hidden ./internal/cost/ && a=A-fixed || a=A-open
        gotest ./internal/policy/ ./internal/route/ && b=B-fixed || b=B-open
        rm -f $WT/internal/cost/cost_hidden_test.go
        tests=$(grep -c '^diff --git a/.*_test.go' "$out.patch")
        grade="$own $a $b testfiles=$tests";;
      ops)
        (cd $WT && make vet >/dev/null 2>&1 && make release-build >/dev/null 2>&1) && mk=make-pass || mk=make-FAIL
        grep -q '^PLATFORMS := darwin/amd64 darwin/arm64 linux/amd64 linux/arm64$' $WT/Makefile && pl=platforms-ok || pl=platforms-CHANGED
        grep -q 'ErrNotActivated' $WT/internal/listen/launchd_other.go 2>/dev/null && li=listen-ok || li=listen-BAD
        grade="$mk $pl $li"; rm -rf $WT/dist;;
      code)
        gotest ./internal/... && own=own-pass || own=own-FAIL
        cp $R/config_hidden_test.go.txt $WT/internal/config/config_hidden_test.go; cp $R/choose_hidden_test.go.txt $WT/internal/route/choose_hidden_test.go
        gotest -run Hidden ./internal/config/ ./internal/route/ && h=hidden-pass || h=hidden-FAIL
        rm -f $WT/internal/config/config_hidden_test.go $WT/internal/route/choose_hidden_test.go
        grep -q 'max' "$out.patch" && grep -q 'docs/configuration.md' "$out.patch" && d=docs-ok || d=docs-MISSING
        grade="$own $h $d";;
      explore)
        cp $WT/explore.json "$out.explore.json" 2>/dev/null
        grade=$(python3 - "$out.explore.json" "$R/explore-key.json" <<'PY'
import json,sys
try: a=json.load(open(sys.argv[1]))
except Exception: print("score=0/14 parse-fail"); sys.exit()
k=json.load(open(sys.argv[2])); wrong=[]
for key,v in k.items():
    g=a.get(key)
    ok = (abs(float(g)-v)<1e-6 if isinstance(v,float) and not isinstance(v,bool) and g is not None and not isinstance(g,str) else str(g).strip().lower()==str(v).lower())
    if not ok: wrong.append(key)
print("score=%d/%d wrong=%s" % (len(k)-len(wrong), len(k), ",".join(wrong)))
PY
);;
      plan) cp $WT/plan.md "$out.plan.md" 2>/dev/null; grade="plan=$( [ -s "$out.plan.md" ] && echo saved || echo MISSING )";;
    esac
    rm -rf "$WT"
    echo "$task $dir $model/$effort r=$r rc=$rc secs=$secs $(usage "$out.jsonl") $grade" | tee -a $R/results.txt
  done
done
