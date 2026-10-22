# Evaluation

These results explain the default routes. They are small tests, not a claim about real session savings.

## Scope

The runs tested bounded tasks on 2026-10-16. They did not measure ChatGPT plan limits, total routing overhead or long sessions. Classifier cost is not included in the table.

The tests used `codex exec`, one fixture per task and three runs each. Tests or a fixed answer key graded the results. Plans were scored blind by `gpt-6.1-sol · high` against a 10-point rubric. The table shows score and mean list-price cost per run.

| Task                                 | Luna medium | Luna high   | Luna xhigh  | Sol low    | Sol medium | Sol high   | Sol xhigh  |
| ------------------------------------ | ----------- | ----------- | ----------- | ---------- | ---------- | ---------- | ---------- |
| review: find 5 planted defects       | 87% $0.006  | 100% $0.009 | 100% $0.014 | 100% $0.12 | 100% $0.16 | 100% $0.20 | 100% $0.25 |
| debug: 2 bugs from a symptom + tests | 100% $0.013 | 100% $0.020 | 100% $0.034 | 100% $0.16 | 100% $0.28 | 100% $0.44 | 100% $0.51 |
| ops: fix vet, build and Makefile     | 100% $0.009 | 100% $0.008 | 100% $0.011 | 100% $0.11 | 100% $0.15 | 100% $0.16 | 100% $0.18 |
| code: new effort + hidden tests      | 100% $0.009 | 100% $0.011 | 100% $0.016 | 100% $0.12 | 100% $0.17 | 100% $0.21 | 100% $0.28 |
| explore: 14 facts from the code      | 95% $0.007  | 100% $0.008 | 100% $0.012 | 100% $0.10 | 100% $0.12 | not run          | not run          |
| plan: design doc, rubric score       | 63% $0.010  | 73% $0.015  | 60% $0.031  | 70% $0.19  | 73% $0.27  | 73% $0.43  | 60% $0.47  |

Each task used one fixture and took minutes. These results do not cover long sessions, large context or open design.

## How to read the results

The review score counts planted defects found. Debug, ops and code scores come from checks against fixed fixtures. Explore answers use a fixed answer key. Plan scores come from a model judge, not a correctness test.

Three runs on one fixture cannot establish a general ranking. Prices and available models can change. Use the figures to understand the defaults, then check your own workload.

## Routing modes

`TestModesMakeAMeasurableDifference` (`internal/route/matrix_test.go`) replays the same seeded sessions under each [mode](configuration.md#mode-and-modes): two threads, 90 turns, 706 routed steps, with random tier and activity answers, continuations, tool failures, edits, passing checks and cache gaps. Token counts come from the script and do not depend on the route, so the cost column is the list price of the models each mode chose, not a measured bill.

| Config                                | Mode     | Cost (list) | Steps on Sol | Mean effort | Switches | Uncached switch tokens |
| ------------------------------------- | -------- | ----------- | ------------ | ----------- | -------- | ---------------------- |
| defaults                              | cost     | $11.94      | 17%          | 1.29        | 58       | 1.05M                  |
| defaults                              | balanced | $41.02      | 78%          | 1.38        | 35       | 0.31M                  |
| defaults                              | quality  | $51.68      | 100%         | 2.68        | 40       | 0                      |
| a profile with a third, mid-priced model | cost  | $20.21      | 0%           | 0.86        | 125      | 3.12M                  |
| same profile                          | balanced | $34.34      | 18%          | 1.14        | 73       | 1.28M                  |
| same profile                          | quality  | $46.33      | 72%          | 2.63        | 63       | 0.93M                  |
| no prices                             | cost     | none        | 12%          | 0.19        | 59       | 1.52M                  |
| no prices                             | balanced | none        | 16%          | 1.17        | 35       | 0.45M                  |
| no prices                             | quality  | none        | 69%          | 2.68        | 41       | 0.68M                  |

Mean effort runs from 0 (`low`) to 3 (`xhigh`).

What the replay shows, and what it does not:

- With the default routes, `cost` cut the list-price cost by 71% and `quality` raised it by 26%.
- `cost` switched models more often and wrote 3.4 times as many uncached tokens on switches. The cost column does not charge for that cache loss. Priced at Sol's full input rate, the extra 0.74M tokens add about $1.47, and `cost` is still 66% below `balanced`.
- In the profile, `cost` switched 1.7 times as often as `balanced` and wrote 1.8M more uncached tokens: it often moves first to the mid-priced model, as `balanced` does, then on to Luna. Priced at Sol's input rate, that cache loss adds at most $3.70, and `cost` stays about 30% below `balanced`.
- `quality` raised effort, not price per token. Its higher cost comes from the stronger policy keeping Sol, not from `xhigh`. More reasoning tokens at `xhigh` are not modeled.
- Without `prices`, `cost` cannot use its model lever and no dollar figure exists: it changes effort and the switching policy only.
- The answers are random, not a real workload. The replay checks direction and size, not real savings.

`TestBalancedDecisionsAreUnchanged` pins balanced decisions to goldens recorded on the v0.8.0 engine: about 1,400 from the plain replay, and about 1,900 from a second replay that adds background reviews, pins, context compaction and threads seeded by a resume. Both replays round `a*b+c` sums before adding (`float64(a*b) + c`), so the goldens are the same on arm64 and amd64.

### Decision matrix

`TestModeDecisionMatrix` (`internal/route/matrix_test.go`) decides a new session's first turn for every classifier tier, activity and mode. It uses the default config and a profile with a third, mid-priced model (`gpt-6-sol`) and activity routes. Every answer is confident, so each cell shows the route a mode aims at, not a pending move. The test checks that:

- `micro` routes the same in every mode.
- `balanced` routes to the pair the config names for the tier and activity.
- `cost` never spends more than `balanced`, and `quality` never spends less. Spend compares input price first, then effort.
- `cost` never routes below the `low` tier and never below `low` effort.
- At least a set number of cells differ from `balanced`: a mode that stops working fails the test.

`TestAPinBypassesEveryMode` checks that a pin routes to the pinned tier's pair in every mode.

The test writes the table below. It fails when the table is out of date. Reasons: `mode` is a mode's move, `same-tier` keeps the route, `uncertain` keeps the incumbent, `activity-up` is an activity route.

<details>
<summary>Decision matrix: 80 rows</summary>

<!-- mode-matrix:start -->
| Config | Tier | Activity | cost | balanced | quality |
| --- | --- | --- | --- | --- | --- |
| default | micro | code | 6-luna/medium (downgrade) | 6-luna/medium (downgrade) | 6-luna/medium (downgrade) |
| default | micro | debug | 6-luna/medium (downgrade) | 6-luna/medium (downgrade) | 6-luna/medium (downgrade) |
| default | micro | explore | 6-luna/medium (downgrade) | 6-luna/medium (downgrade) | 6-luna/medium (downgrade) |
| default | micro | plan | 6-luna/medium (downgrade) | 6-luna/medium (downgrade) | 6-luna/medium (downgrade) |
| default | micro | review | 6-luna/medium (downgrade) | 6-luna/medium (downgrade) | 6-luna/medium (downgrade) |
| default | micro | ops | 6-luna/medium (downgrade) | 6-luna/medium (downgrade) | 6-luna/medium (downgrade) |
| default | micro | docs | 6-luna/medium (downgrade) | 6-luna/medium (downgrade) | 6-luna/medium (downgrade) |
| default | micro | uncertain | 6-luna/medium (downgrade) | 6-luna/medium (downgrade) | 6-luna/medium (downgrade) |
| default | low | code | 6-luna/medium (downgrade) | 6-luna/high (downgrade) | 6-luna/xhigh (downgrade) |
| default | low | debug | 6-luna/medium (downgrade) | 6-luna/high (downgrade) | 6-luna/xhigh (downgrade) |
| default | low | explore | 6-luna/medium (downgrade) | 6-luna/high (downgrade) | 6-luna/xhigh (downgrade) |
| default | low | plan | 6-luna/medium (downgrade) | 6-luna/high (downgrade) | 6-luna/xhigh (downgrade) |
| default | low | review | 6-luna/medium (downgrade) | 6-luna/high (downgrade) | 6-luna/xhigh (downgrade) |
| default | low | ops | 6-luna/medium (downgrade) | 6-luna/high (downgrade) | 6-luna/xhigh (downgrade) |
| default | low | docs | 6-luna/medium (downgrade) | 6-luna/high (downgrade) | 6-luna/xhigh (downgrade) |
| default | low | uncertain | 6-luna/medium (downgrade) | 6-luna/high (downgrade) | 6-luna/xhigh (downgrade) |
| default | medium | code | 6-luna/high (mode) | 6.1-sol/medium (same-tier) | 6.1-sol/high (mode) |
| default | medium | debug | 6-luna/high (mode) | 6.1-sol/high (activity-up) | 6.1-sol/xhigh (mode) |
| default | medium | explore | 6-luna/high (mode) | 6.1-sol/medium (same-tier) | 6.1-sol/high (mode) |
| default | medium | plan | 6-luna/high (mode) | 6.1-sol/medium (same-tier) | 6.1-sol/high (mode) |
| default | medium | review | 6-luna/high (mode) | 6.1-sol/medium (same-tier) | 6.1-sol/high (mode) |
| default | medium | ops | 6-luna/high (mode) | 6.1-sol/medium (same-tier) | 6.1-sol/high (mode) |
| default | medium | docs | 6-luna/high (mode) | 6.1-sol/medium (same-tier) | 6.1-sol/high (mode) |
| default | medium | uncertain | 6-luna/high (mode) | 6.1-sol/medium (same-tier) | 6.1-sol/high (mode) |
| default | high | code | 6.1-sol/medium (upgrade) | 6.1-sol/high (upgrade) | 6.1-sol/xhigh (upgrade) |
| default | high | debug | 6.1-sol/medium (upgrade) | 6.1-sol/high (upgrade) | 6.1-sol/xhigh (upgrade) |
| default | high | explore | 6.1-sol/medium (upgrade) | 6.1-sol/high (upgrade) | 6.1-sol/xhigh (upgrade) |
| default | high | plan | 6.1-sol/medium (upgrade) | 6.1-sol/high (upgrade) | 6.1-sol/xhigh (upgrade) |
| default | high | review | 6.1-sol/medium (upgrade) | 6.1-sol/high (upgrade) | 6.1-sol/xhigh (upgrade) |
| default | high | ops | 6.1-sol/medium (upgrade) | 6.1-sol/high (upgrade) | 6.1-sol/xhigh (upgrade) |
| default | high | docs | 6.1-sol/medium (upgrade) | 6.1-sol/high (upgrade) | 6.1-sol/xhigh (upgrade) |
| default | high | uncertain | 6.1-sol/medium (upgrade) | 6.1-sol/high (upgrade) | 6.1-sol/xhigh (upgrade) |
| default | uncertain | code | 6.1-sol/medium (uncertain) | 6.1-sol/medium (uncertain) | 6.1-sol/medium (uncertain) |
| default | uncertain | debug | 6.1-sol/medium (uncertain) | 6.1-sol/high (activity-up) | 6.1-sol/high (activity-up) |
| default | uncertain | explore | 6.1-sol/medium (uncertain) | 6.1-sol/medium (uncertain) | 6.1-sol/medium (uncertain) |
| default | uncertain | plan | 6.1-sol/medium (uncertain) | 6.1-sol/medium (uncertain) | 6.1-sol/medium (uncertain) |
| default | uncertain | review | 6.1-sol/medium (uncertain) | 6.1-sol/medium (uncertain) | 6.1-sol/medium (uncertain) |
| default | uncertain | ops | 6.1-sol/medium (uncertain) | 6.1-sol/medium (uncertain) | 6.1-sol/medium (uncertain) |
| default | uncertain | docs | 6.1-sol/medium (uncertain) | 6.1-sol/medium (uncertain) | 6.1-sol/medium (uncertain) |
| default | uncertain | uncertain | 6.1-sol/medium (uncertain) | 6.1-sol/medium (uncertain) | 6.1-sol/medium (uncertain) |
| profile | micro | code | 6-luna/medium (downgrade) | 6-luna/medium (downgrade) | 6-luna/medium (downgrade) |
| profile | micro | debug | 6-luna/medium (downgrade) | 6-luna/medium (downgrade) | 6-luna/medium (downgrade) |
| profile | micro | explore | 6-luna/medium (downgrade) | 6-luna/medium (downgrade) | 6-luna/medium (downgrade) |
| profile | micro | plan | 6-luna/medium (downgrade) | 6-luna/medium (downgrade) | 6-luna/medium (downgrade) |
| profile | micro | review | 6-luna/medium (downgrade) | 6-luna/medium (downgrade) | 6-luna/medium (downgrade) |
| profile | micro | ops | 6-luna/medium (downgrade) | 6-luna/medium (downgrade) | 6-luna/medium (downgrade) |
| profile | micro | docs | 6-luna/medium (downgrade) | 6-luna/medium (downgrade) | 6-luna/medium (downgrade) |
| profile | micro | uncertain | 6-luna/medium (downgrade) | 6-luna/medium (downgrade) | 6-luna/medium (downgrade) |
| profile | low | code | 6-sol/low (downgrade) | 6-sol/low (downgrade) | 6-sol/medium (downgrade) |
| profile | low | debug | 6-luna/medium (downgrade) | 6-luna/high (downgrade) | 6-luna/xhigh (downgrade) |
| profile | low | explore | 6-luna/medium (downgrade) | 6-luna/high (downgrade) | 6-luna/xhigh (downgrade) |
| profile | low | plan | 6-luna/medium (downgrade) | 6-luna/high (downgrade) | 6-luna/xhigh (downgrade) |
| profile | low | review | 6-luna/medium (downgrade) | 6-luna/high (downgrade) | 6-luna/xhigh (downgrade) |
| profile | low | ops | 6-luna/medium (downgrade) | 6-luna/high (downgrade) | 6-luna/xhigh (downgrade) |
| profile | low | docs | 6-luna/medium (downgrade) | 6-luna/high (downgrade) | 6-luna/xhigh (downgrade) |
| profile | low | uncertain | 6-luna/medium (downgrade) | 6-luna/high (downgrade) | 6-luna/xhigh (downgrade) |
| profile | medium | code | 6-sol/low (mode) | 6-sol/medium (same-tier) | 6-sol/high (mode) |
| profile | medium | debug | 6-luna/high (mode) | 6-sol/high (activity-up) | 6-sol/xhigh (mode) |
| profile | medium | explore | 6-luna/high (mode) | 6-sol/medium (same-tier) | 6-sol/high (mode) |
| profile | medium | plan | 6-luna/high (mode) | 6-sol/medium (same-tier) | 6-sol/high (mode) |
| profile | medium | review | 6-luna/high (mode) | 6.1-sol/medium (activity-up) | 6.1-sol/high (mode) |
| profile | medium | ops | 6-luna/high (mode) | 6-sol/medium (same-tier) | 6-sol/high (mode) |
| profile | medium | docs | 6-luna/high (mode) | 6-sol/medium (same-tier) | 6-sol/high (mode) |
| profile | medium | uncertain | 6-luna/high (mode) | 6-sol/medium (same-tier) | 6-sol/high (mode) |
| profile | high | code | 6-sol/medium (upgrade) | 6.1-sol/high (upgrade) | 6.1-sol/xhigh (upgrade) |
| profile | high | debug | 6-sol/high (upgrade) | 6.1-sol/high (upgrade) | 6.1-sol/xhigh (upgrade) |
| profile | high | explore | 6-sol/medium (upgrade) | 6.1-sol/high (upgrade) | 6.1-sol/xhigh (upgrade) |
| profile | high | plan | 6-sol/medium (upgrade) | 6.1-sol/xhigh (upgrade) | 6.1-sol/xhigh (upgrade) |
| profile | high | review | 6.1-sol/medium (upgrade) | 6.1-sol/high (upgrade) | 6.1-sol/xhigh (upgrade) |
| profile | high | ops | 6-sol/medium (upgrade) | 6.1-sol/high (upgrade) | 6.1-sol/xhigh (upgrade) |
| profile | high | docs | 6-sol/medium (upgrade) | 6-sol/high (upgrade) | 6-sol/xhigh (upgrade) |
| profile | high | uncertain | 6-sol/medium (upgrade) | 6.1-sol/high (upgrade) | 6.1-sol/xhigh (upgrade) |
| profile | uncertain | code | 6-sol/medium (uncertain) | 6-sol/medium (uncertain) | 6-sol/medium (uncertain) |
| profile | uncertain | debug | 6-sol/medium (uncertain) | 6-sol/high (activity-up) | 6-sol/high (activity-up) |
| profile | uncertain | explore | 6-sol/medium (uncertain) | 6-sol/medium (uncertain) | 6-sol/medium (uncertain) |
| profile | uncertain | plan | 6-sol/medium (uncertain) | 6-sol/medium (uncertain) | 6-sol/medium (uncertain) |
| profile | uncertain | review | 6-sol/medium (uncertain) | 6.1-sol/medium (activity-up) | 6.1-sol/medium (activity-up) |
| profile | uncertain | ops | 6-sol/medium (uncertain) | 6-sol/medium (uncertain) | 6-sol/medium (uncertain) |
| profile | uncertain | docs | 6-sol/medium (uncertain) | 6-sol/medium (uncertain) | 6-sol/medium (uncertain) |
| profile | uncertain | uncertain | 6-sol/medium (uncertain) | 6-sol/medium (uncertain) | 6-sol/medium (uncertain) |
<!-- mode-matrix:end -->

</details>

### Live check

The [routing modes live check](archive/evidence/2026-10-21-routing-modes/report.md) has two runs. First, 20 fixed prompts went through Jev and were routed under each mode: no routing bug, and cost ≤ balanced ≤ quality on every prompt. Jev classified 10 of them other than expected, mostly one tier off. Second, the same four-turn task ran once per mode. All three runs passed their tests. Router spend was $0.009 for cost, $0.108 for balanced and $0.117 for quality. The live classifier run is `go test -tags live ./internal/route -run TestLiveClassifierRoutes` with `CODEX_ROUTER_CONFIG` set.

## Evidence

The [original report](archive/evidence/2026-10-15-openai-defaults/report.md) records the method and limits. Its `r2/` directory holds the runner, fixtures and grading material. The [live routing reports](archive/README.md) cover integration behavior separately.

See [route configuration](configuration.md#routes-and-activities) to change the defaults.
