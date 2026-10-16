#!/bin/bash
# Usage: judge.sh  -> scores every out/plan-*.plan.md blind against rubric.txt
set -u
R=$(cd "$(dirname "$0")" && pwd); mkdir -p $R/judge
for f in $R/out/plan-*.plan.md; do
  id=$(basename "$f" .plan.md); [ -s "$R/judge/$id.json" ] && continue
  prompt="You are grading an implementation plan against a rubric. Score each of the 10 rubric items 1 only if the plan explicitly addresses it, else 0. Do not reward length. Reply with JSON only: {\"scores\":[10 integers],\"total\":N}.

RUBRIC:
$(cat $R/rubric.txt)

PLAN:
$(cat "$f")"
  command codex exec -C /tmp --skip-git-repo-check -s read-only -m gpt-6.1-sol -c model_reasoning_effort=high -o "$R/judge/$id.raw" "$prompt" < /dev/null >/dev/null 2>&1
  python3 - "$R/judge/$id.raw" "$R/judge/$id.json" <<'PY'
import json,re,sys
t=open(sys.argv[1]).read(); m=re.search(r'\{.*\}',t,re.S)
j=json.loads(m.group(0)); json.dump(j,open(sys.argv[2],'w')); print(sys.argv[2].split('/')[-1], j['total'])
PY
done
