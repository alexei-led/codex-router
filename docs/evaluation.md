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

## Evidence

The [original report](archive/evidence/2026-10-15-openai-defaults/report.md) records the method and limits. Its `r2/` directory holds the runner, fixtures and grading material. The [live routing reports](archive/README.md) cover integration behavior separately.

See [route configuration](configuration.md#routes-and-activities) to change the defaults.
