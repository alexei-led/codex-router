"""Aggregate r2/results.txt (+ judge/*.json for plans) into a task x variant table."""
import json, re, sys, pathlib
from collections import defaultdict

R = pathlib.Path(__file__).parent
ORDER = ["lunamed", "lunahi", "luna", "sol", "solmed", "solhigh", "solxhigh"]
LABEL = {"lunamed": "L·med", "lunahi": "L·high", "luna": "L·xhigh", "sol": "S·low",
         "solmed": "S·med", "solhigh": "S·high", "solxhigh": "S·xhigh"}
rows = defaultdict(list)
for line in (R / "results.txt").read_text().splitlines():
    p = line.split()
    if len(p) < 3 or "SETUP-FAIL" in line:
        continue
    task, var = p[0], p[1]
    kv = dict(x.split("=", 1) for x in p if "=" in x)
    r = kv["r"]
    if task == "review":
        k = int(kv["score"].split("/")[0]); fp = kv.get("fp", "?")
        q = k / 5; tag = f"{k}/5" + (f" fp{fp}" if fp not in ("0",) else "")
    elif task == "explore":
        a, b = kv["score"].split("/"); q = int(a) / int(b); tag = f"{a}/{b}"
    elif task == "plan":
        j = R / "judge" / f"plan-{var}-{r}.json"
        if j.exists():
            t = json.loads(j.read_text())["total"]; q = t / 10; tag = f"{t}/10"
        else:
            q = None; tag = "?"
    else:
        ok = not re.search(r"FAIL|open|CHANGED|BAD|MISSING", line); q = 1.0 if ok else 0.0
        tag = "ok" if ok else "|".join(x for x in p if re.search(r"FAIL|open|CHANGED|BAD|MISSING", x))
    rows[(task, var)].append((q, float(kv["usd"]), int(kv["secs"]), tag))

tasks = ["review", "debug", "ops", "code", "explore", "plan"]
print("| Task | " + " | ".join(LABEL[v] for v in ORDER) + " |")
print("|---" * (len(ORDER) + 1) + "|")
for t in tasks:
    cells = []
    for v in ORDER:
        rs = rows.get((t, v))
        if not rs:
            cells.append("—"); continue
        qs = [x[0] for x in rs if x[0] is not None]
        q = f"{100*sum(qs)/len(qs):.0f}%" if qs else "?"
        usd = sum(x[1] for x in rs) / len(rs); secs = sorted(x[2] for x in rs)[len(rs) // 2]
        cells.append(f"{q} ${usd:.3f} {secs}s ({','.join(x[3] for x in rs)})" if "-v" in sys.argv else f"{q} ${usd:.3f} {secs}s n{len(rs)}")
    print(f"| {t} | " + " | ".join(cells) + " |")
