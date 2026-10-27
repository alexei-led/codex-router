# Evaluation

These results inform the default routes. They are small tests, not a claim about real session savings.

## Terra defaults

The Luna/Terra/Sol preference matrix is a **derived routing policy**, not a provider recommendation or permission boundary. Standard input/cached-input/output prices per million tokens are Luna $0.10/$0.01/$0.50, Terra $1/$0.05/$5, and Sol $2/$0.10/$10. [OpenAI model specifications](https://developers.openai.com/api/docs/models/gpt-6.1-terra) and the [independent efficiency comparison](https://artificialanalysis.ai/articles/gpt-6-1-terra-pushes-the-cost-efficiency-frontier) motivate testing Terra between Luna and Sol; they do not establish your Codex entitlement or window.

Maintainer fixtures on 2026-10-27 used 36 isolated Codex runs: two repetitions, three tasks and six model/effort settings. One capacity failure was retried with explicit authorization and the original failure remained recorded. Terra xhigh code averaged $0.1339 versus Sol medium $0.2177; Terra medium review $0.0649 versus $0.1648; docs $0.0225 versus $0.0493. Luna high passed bounded code/review more cheaply; one Luna docs run made an arithmetic error and one Terra low run left a stale test. Those runs recorded selected CLI settings, not server-confirmed backend model/effort; treat the comparisons as provisional routing evidence. These list-price equivalents and small fixtures are not production failure rates, broad model equivalence or guaranteed session savings. The earlier benchmark below did not include Terra.

The defaults use Terra medium for generic balanced medium, cost high and quality low; code/debug raise the first two to xhigh. Quality medium uses Sol medium or high by activity. The [full matrix](configuration.md#mode-and-modes) and independent 96-cell acceptance fixture in `internal/route/testdata/default-preferences.json` pin exact model and effort. Runtime catalog effort, group and context checks can choose an eligible fallback instead. Flat estimates omit long-context, Fast and cache-write billing details and are not [Codex subscription invoices](https://developers.openai.com/codex/pricing).

## Scope

The runs tested bounded tasks on 2026-10-16. They did not measure ChatGPT plan limits, total routing overhead or long sessions. Classifier cost is not included in the table.

The tests used `codex exec`, one fixture per task and three runs each. Tests or a fixed answer key graded the results. Plans were scored blind by `gpt-6.1-sol · high` against a 10-point rubric. The table shows score and mean list-price cost per run.

| Task                                 | Luna medium | Luna high   | Luna xhigh  | Sol low    | Sol medium | Sol high   | Sol xhigh  |
| ------------------------------------ | ----------- | ----------- | ----------- | ---------- | ---------- | ---------- | ---------- |
| review: find 5 planted defects       | 87% $0.006  | 100% $0.009 | 100% $0.014 | 100% $0.12 | 100% $0.16 | 100% $0.20 | 100% $0.25 |
| debug: 2 bugs from a symptom + tests | 100% $0.013 | 100% $0.020 | 100% $0.034 | 100% $0.16 | 100% $0.28 | 100% $0.44 | 100% $0.51 |
| ops: fix vet, build and Makefile     | 100% $0.009 | 100% $0.008 | 100% $0.011 | 100% $0.11 | 100% $0.15 | 100% $0.16 | 100% $0.18 |
| code: new effort + hidden tests      | 100% $0.009 | 100% $0.011 | 100% $0.016 | 100% $0.12 | 100% $0.17 | 100% $0.21 | 100% $0.28 |
| explore: 14 facts from the code      | 95% $0.007  | 100% $0.008 | 100% $0.012 | 100% $0.10 | 100% $0.12 | not run    | not run    |
| plan: design doc, rubric score       | 63% $0.010  | 73% $0.015  | 60% $0.031  | 70% $0.19  | 73% $0.27  | 73% $0.43  | 60% $0.47  |

Each task used one fixture and took minutes. These results do not cover long sessions, large context or open design.

## How to read the results

The review score counts planted defects found. Debug, ops and code scores come from checks against fixed fixtures. Explore answers use a fixed answer key. Plan scores come from a model judge, not a correctness test.

Three runs on one fixture cannot establish a general ranking. Prices and available models can change. Use the figures to understand the defaults, then check your own workload.

## Routing modes

`TestModesMakeAMeasurableDifference` (`internal/route/matrix_test.go`) replays the same seeded sessions under each [mode](configuration.md#mode-and-modes): two threads, 90 turns, 706 routed steps, with random tier and activity answers, continuations, tool failures, edits, passing checks and cache gaps. Token counts come from the script and do not depend on the route, so the cost column is the list price of the models each mode chose, not a measured bill.

| Config                                   | Mode     | Cost (list) | Steps on Sol | Mean effort | Switches | Uncached switch tokens |
| ---------------------------------------- | -------- | ----------- | ------------ | ----------- | -------- | ---------------------- |
| defaults                                 | cost     | $6.75       | 0%           | 1.43        | 67       | 0.98M                  |
| defaults                                 | balanced | $24.86      | 16%          | 1.91        | 126      | 0.45M                  |
| defaults                                 | quality  | $50.20      | 94%          | 2.66        | 57       | 0                      |
| a profile with a third, mid-priced model | cost     | $20.44      | 0%           | 0.83        | 124      | 3.11M                  |
| same profile                             | balanced | $34.34      | 18%          | 1.14        | 73       | 1.28M                  |
| same profile                             | quality  | $46.33      | 72%          | 2.63        | 63       | 0.93M                  |
| no prices                                | cost     | none        | 12%          | 0.19        | 59       | 1.52M                  |
| no prices                                | balanced | none        | 16%          | 1.17        | 35       | 0.45M                  |
| no prices                                | quality  | none        | 69%          | 2.68        | 41       | 0.68M                  |

Mean effort runs from 0 (`low`) to 3 (`xhigh`).

What the replay shows, and what it does not:

- Default `cost` uses cheaper physical targets and `quality` uses more Sol. Both model prices and switching policy affect the totals.
- Switch-token counts quantify cache exposure separately. The cost column does not add a second charge for those tokens.
- The replay does not model route-dependent answer quality or extra reasoning tokens at higher effort. Mean effort is an ordering indicator, not a billing multiplier.
- Without `prices`, `cost` cannot use its model lever and no dollar figure exists: it changes effort and the switching policy only.
- The answers are random, not a real workload. The replay checks direction and size, not real savings.

`TestBalancedDecisionsAreUnchanged` freezes the legacy/custom ladder and pins its balanced decisions to goldens recorded on the v0.8.0 engine: about 1,400 from the plain replay, and about 1,900 from a second replay that adds background reviews, pins, context compaction and threads seeded by a resume. Both replays round `a*b+c` sums before adding (`float64(a*b) + c`), so the goldens are the same on arm64 and amd64.

### Decision matrix

`TestModeDecisionMatrix` (`internal/route/matrix_test.go`) decides a new session's first turn for every classifier tier, activity and mode. It uses the default config and a profile with a third, mid-priced model (`gpt-6-sol`) and activity routes. Every answer is confident, so each cell shows the route a mode aims at, not a pending move. The test checks that:

- `micro` routes the same in every mode.
- `balanced` routes to its resolved preferred pair for the tier and activity; custom cells keep their configured pair.
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
| default | low | code | 6-luna/medium (downgrade) | 6-luna/high (downgrade) | 6.1-terra/medium (downgrade) |
| default | low | debug | 6-luna/medium (downgrade) | 6-luna/high (downgrade) | 6.1-terra/medium (downgrade) |
| default | low | explore | 6-luna/medium (downgrade) | 6-luna/high (downgrade) | 6.1-terra/medium (downgrade) |
| default | low | plan | 6-luna/medium (downgrade) | 6-luna/high (downgrade) | 6.1-terra/medium (downgrade) |
| default | low | review | 6-luna/medium (downgrade) | 6-luna/high (downgrade) | 6.1-terra/medium (downgrade) |
| default | low | ops | 6-luna/medium (downgrade) | 6-luna/high (downgrade) | 6.1-terra/medium (downgrade) |
| default | low | docs | 6-luna/medium (downgrade) | 6-luna/high (downgrade) | 6.1-terra/medium (downgrade) |
| default | low | uncertain | 6-luna/medium (downgrade) | 6-luna/high (downgrade) | 6.1-terra/medium (downgrade) |
| default | medium | code | 6-luna/high (mode) | 6.1-terra/xhigh (activity-up) | 6.1-sol/high (mode) |
| default | medium | debug | 6-luna/high (mode) | 6.1-terra/xhigh (activity-up) | 6.1-sol/high (mode) |
| default | medium | explore | 6-luna/high (mode) | 6.1-terra/medium (same-tier) | 6.1-sol/medium (mode) |
| default | medium | plan | 6-luna/high (mode) | 6.1-terra/medium (same-tier) | 6.1-sol/high (mode) |
| default | medium | review | 6-luna/high (mode) | 6.1-terra/medium (same-tier) | 6.1-sol/high (mode) |
| default | medium | ops | 6-luna/high (mode) | 6.1-terra/medium (same-tier) | 6.1-sol/medium (mode) |
| default | medium | docs | 6-luna/high (mode) | 6.1-terra/medium (same-tier) | 6.1-sol/medium (mode) |
| default | medium | uncertain | 6-luna/high (mode) | 6.1-terra/medium (same-tier) | 6.1-sol/medium (mode) |
| default | high | code | 6.1-terra/xhigh (upgrade) | 6.1-sol/high (upgrade) | 6.1-sol/xhigh (upgrade) |
| default | high | debug | 6.1-terra/xhigh (upgrade) | 6.1-sol/xhigh (upgrade) | 6.1-sol/xhigh (upgrade) |
| default | high | explore | 6.1-terra/medium (upgrade) | 6.1-sol/high (upgrade) | 6.1-sol/xhigh (upgrade) |
| default | high | plan | 6.1-terra/medium (upgrade) | 6.1-sol/xhigh (upgrade) | 6.1-sol/xhigh (upgrade) |
| default | high | review | 6.1-terra/medium (upgrade) | 6.1-sol/xhigh (upgrade) | 6.1-sol/xhigh (upgrade) |
| default | high | ops | 6.1-terra/medium (upgrade) | 6.1-sol/high (upgrade) | 6.1-sol/xhigh (upgrade) |
| default | high | docs | 6.1-terra/medium (upgrade) | 6.1-sol/high (upgrade) | 6.1-sol/xhigh (upgrade) |
| default | high | uncertain | 6.1-terra/medium (upgrade) | 6.1-sol/high (upgrade) | 6.1-sol/xhigh (upgrade) |
| default | uncertain | code | 6.1-terra/medium (uncertain) | 6.1-terra/xhigh (activity-up) | 6.1-terra/xhigh (activity-up) |
| default | uncertain | debug | 6.1-terra/medium (uncertain) | 6.1-terra/xhigh (activity-up) | 6.1-terra/xhigh (activity-up) |
| default | uncertain | explore | 6.1-terra/medium (uncertain) | 6.1-terra/medium (uncertain) | 6.1-terra/medium (uncertain) |
| default | uncertain | plan | 6.1-terra/medium (uncertain) | 6.1-terra/medium (uncertain) | 6.1-terra/medium (uncertain) |
| default | uncertain | review | 6.1-terra/medium (uncertain) | 6.1-terra/medium (uncertain) | 6.1-terra/medium (uncertain) |
| default | uncertain | ops | 6.1-terra/medium (uncertain) | 6.1-terra/medium (uncertain) | 6.1-terra/medium (uncertain) |
| default | uncertain | docs | 6.1-terra/medium (uncertain) | 6.1-terra/medium (uncertain) | 6.1-terra/medium (uncertain) |
| default | uncertain | uncertain | 6.1-terra/medium (uncertain) | 6.1-terra/medium (uncertain) | 6.1-terra/medium (uncertain) |
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
