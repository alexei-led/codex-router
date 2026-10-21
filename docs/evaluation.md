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
| defaults                              | quality  | $51.68      | 100%         | 2.73        | 41       | 0                      |
| a profile with a third, mid-priced model | cost  | $23.09      | 0%           | 0.66        | 92       | 2.51M                  |
| same profile                          | balanced | $34.34      | 18%          | 1.14        | 73       | 1.28M                  |
| same profile                          | quality  | $45.69      | 68%          | 2.73        | 58       | 0.84M                  |
| no prices                             | cost     | none        | 12%          | 0.19        | 59       | 1.52M                  |
| no prices                             | balanced | none        | 16%          | 1.17        | 35       | 0.45M                  |
| no prices                             | quality  | none        | 69%          | 2.73        | 42       | 0.68M                  |

Mean effort runs from 0 (`low`) to 3 (`xhigh`).

What the replay shows, and what it does not:

- With the default routes, `cost` cut the list-price cost by 71% and `quality` raised it by 26%.
- `cost` switched models more often and wrote 3.4 times as many uncached tokens on switches. The cost column does not charge for that cache loss. Priced at Sol's full input rate, the extra 0.74M tokens add about $1.47, and `cost` is still 66% below `balanced`.
- `quality` raised effort, not price per token. Its higher cost comes from the stronger policy keeping Sol, not from `xhigh`. More reasoning tokens at `xhigh` are not modeled.
- Without `prices`, `cost` cannot use its model lever and no dollar figure exists: it changes effort and the switching policy only.
- The answers are random, not a real workload. The replay checks direction and size, not real savings.

`TestModeDecisionMatrix` pins the route and reason of a fresh session's first turn for every tier, activity and mode (`internal/route/testdata/mode-matrix.golden`). `TestBalancedDecisionsAreUnchanged` pins about 1,400 balanced decisions to goldens recorded on the v0.8.0 engine.

## Evidence

The [original report](archive/evidence/2026-10-15-openai-defaults/report.md) records the method and limits. Its `r2/` directory holds the runner, fixtures and grading material. The [live routing reports](archive/README.md) cover integration behavior separately.

See [route configuration](configuration.md#routes-and-activities) to change the defaults.
