# OpenAI default routes, 2026-10-15/16

Codex CLI 0.162.1, `codex-router` v0.6.1, classifier Jev. Goal: pick the model and effort for each
tier and activity, then check live that routing moves up and down as designed and prices cache
loss. Plan and results were reviewed with a Codex peer. Prices are OpenAI list prices from the
model cards (Sol 6.1 verified 2026-09-30, Luna 2026-09-23); plan billing is usage limits, so the
dollar figures are estimates.

## Result: the new defaults

| Tier   | Route                  | Activity overrides           |
| ------ | ---------------------- | ---------------------------- |
| micro  | `gpt-6-luna · medium`  | —                            |
| low    | `gpt-6-luna · high`    | —                            |
| medium | `gpt-6.1-sol · medium` | debug → `gpt-6.1-sol · high` |
| high   | `gpt-6.1-sol · high`   | —                            |

Default prices: `gpt-6.1-sol` 2 / 0.10 / 10, `gpt-6-luna` 0.10 / 0.01 / 0.50 (USD per million
tokens: input / cached input / output). Policy defaults are unchanged. Round 1 (2026-10-15) set
micro `luna · low`, low `luna · medium` and high `sol · xhigh`; round 2 (2026-10-16, below) moved
each one step.

Why:

- `gpt-6-sol` is dropped: same list price as `gpt-6.1-sol`, a dearer cached input (0.20), and the
  model card marks it as the predecessor. Speed and quality against 6.1 were not measured.
- Codex caches per model, not per effort. With two models, medium ↔ high and every effort change
  on one model cost no cache; only a Luna ↔ Sol move does.
- Luna at high effort matched every Sol setting on all six round-2 tasks at 5–10% of the list
  cost. Luna at medium missed a review defect, an explore fact and planned worst; low therefore
  runs Luna·high for every activity, and micro Luna·medium (public DeepSWE: Luna·low 2.4%).
- `xhigh` lost on both models: plan scores 6.0 against 7.3 at high, 14–70% dearer and 1.3–2.5×
  slower elsewhere, with no pass gained. High runs Sol·high.
- Medium stays on Sol: long multi-turn work, open-ended design and large-context tasks were not
  measured, public Terminal-Bench, SRE Bench and Bug Hunt show a wide Sol lead, and escalation
  cannot catch a plausible wrong plan or a missed review finding.

## Round 2: six activities, seven settings

`r2/run.sh`, 2026-10-16. Each run gets a fresh `git archive` export of its fixture (patches in
`r2/fixtures/`), `codex exec --json -s workspace-write`, stdin closed, 3 runs per cell, list-price
USD from the run's usage. Review gets the planted commit on top of its parent, because the prompt
reviews `git show HEAD`. Plans are scored blind by `gpt-6.1-sol · high` against `r2/rubric.txt`
(`r2/judge.sh`, scores in `r2/judge/`). Raw lines: `r2/results.txt`; table: `r2/summary.md`.

| Task                                                    | Luna medium | Luna high   | Luna xhigh  | Sol low    | Sol medium | Sol high   | Sol xhigh  |
| ------------------------------------------------------- | ----------- | ----------- | ----------- | ---------- | ---------- | ---------- | ---------- |
| review: 5 planted defects, 0 false positives in any run | 87% $0.006  | 100% $0.009 | 100% $0.014 | 100% $0.12 | 100% $0.16 | 100% $0.20 | 100% $0.25 |
| debug: 2 bugs from a symptom, hidden + restored tests   | 100% $0.013 | 100% $0.020 | 100% $0.034 | 100% $0.16 | 100% $0.28 | 100% $0.44 | 100% $0.51 |
| ops: vet error, platform build, Makefile typo           | 100% $0.009 | 100% $0.008 | 100% $0.011 | 100% $0.11 | 100% $0.15 | 100% $0.16 | 100% $0.18 |
| code: `max` effort, hidden tests, docs                  | 100% $0.009 | 100% $0.011 | 100% $0.016 | 100% $0.12 | 100% $0.17 | 100% $0.21 | 100% $0.28 |
| explore: 14 facts against a key                         | 95% $0.007  | 100% $0.008 | 100% $0.012 | 100% $0.10 | 100% $0.12 | —          | —          |
| plan: USD switch tax, 10-item rubric                    | 63% $0.010  | 73% $0.015  | 60% $0.031  | 70% $0.19  | 73% $0.27  | 73% $0.43  | 60% $0.47  |

Median seconds per run (review / debug / plan): Luna high 55 / 97 / 103, Sol medium 76 / 215 /
209, Sol high 117 / 342 / 342, Sol xhigh 153 / 468 / 447.

Run hygiene: two earlier attempts are not counted. The first shared worktrees and a stash-based
reset that raced across parallel runners, so later tasks ran on the wrong fixture; the second
exported one commit, so review saw the whole repository as `HEAD`.

Limits: every task is bounded (one fixture, minutes of work) and most cells pass, so only review,
explore and plan separate the settings. Three plan runs per cell do not separate 6/10 from 7/10
with confidence. No item of the rubric's 4 (plan billing) or 8 (cache-write and long-context
pricing) was hit by any model.

## Round 1 benchmarks

`codex exec --json`, one fixture per task, reset before each run, 3 runs per setting
(`scripts/bench*.sh`, results in `summary.txt`). Pass needs the task's own check, not the model's
claim.

| Task                                                          | Luna·medium    | Luna·high      | Sol·low        | Sol·medium     | Sol·high       |
| ------------------------------------------------------------- | -------------- | -------------- | -------------- | -------------- | -------------- |
| B3 `ParseSize` from a spec, graded by 30 hidden cases         | 3/3 $0.005 44s | 3/3 $0.007 63s | 3/3 $0.094 60s | 3/3 $0.106 75s | —              |
| B4 two planted bugs in `internal/policy`, graded by its tests | 3/3 $0.006 36s | 3/3 $0.005 26s | —              | 3/3 $0.117 52s | 3/3 $0.120 50s |
| B5 bug report only (cache warmth), fix + regression test      | 3/3 $0.006 49s | —              | —              | 3/3 $0.110 72s | —              |

- B3 was regraded from the saved patches (`b3-regrade.txt`): `go test` exit code 0 and
  `TestParseSizeHidden` ran and passed, for all 12 runs.
- B5 counts the rerun with the full grader (`B5v2` lines): the model's tests pass on its fix,
  fail on the planted bug, and the original test passes. The first B5 round overwrote the
  model's test file before grading, so it is not counted.
- B1 and B2 (trivial fixes and a lint report) passed 12/12 but separate nothing; B2 ran before
  the fixture tests were moved behind the `lab` build tag.

## Live routing checks

An agterm split: `codex-router … -- --yolo` on the left, `codex-router tui` on the right, in a
throwaway worktree with a `labfix` fixture. Each case file in `cases/` holds the panel, the
`route decision` lines and the Codex screen. C and P cases ran on `scripts/config.v2.json`
(the table above plus Sol·low for low code, debug, plan and review); F cases ran on the new
built-in defaults.

| Check                                     | Result                                                                                                                                                | Evidence               |
| ----------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------- | ---------------------- |
| Fresh thread moves down on one answer     | Pass. `downgrade` to Luna, bar 90% (no cache yet).                                                                                                    | C1, C2, C7, F1         |
| Activity override on a new task           | Pass. low + code → `gpt-6.1-sol · low` under v2.                                                                                                      | C3                     |
| Stay when support is short                | Pass. `downgrade-pending` below 90%.                                                                                                                  | C4, C5                 |
| Jump two tiers                            | Pass. micro → high at 98–100%, micro → medium after idle.                                                                                             | C6, P2-1, P3d          |
| Escalation and hold                       | Pass. Same failure twice with an edit: +1 tier, then `hold` for 2 turns while the classifier said micro. From low it lands on `gpt-6.1-sol · medium`. | P1, F2                 |
| Two-vote downgrade after a measured reply | Pass. First answer `downgrade-pending`, second `downgrade`.                                                                                           | P1-5/6, P2-2/3, F2-5/6 |
| Cache tax raises the bar                  | Pass. Sol warm, Luna cold: bar 93–94%, switch ≈ 83k uncached tokens.                                                                                  | P2-2, P2-3             |
| No tax when both models are idle          | Pass. After 300 s idle, upgrade bar at the 75% base.                                                                                                  | P3d                    |
| No tax on an effort-only change           | Pass. Sol xhigh → medium, bar 90%.                                                                                                                    | P4-after               |
| Pin                                       | Pass. `pinned` for one turn, no classifier call.                                                                                                      | P4-pinned              |

## Live session on the new defaults (2026-10-16)

The installed build of this branch, the user's own Codex config (start model `gpt-6-luna · high`)
and `--yolo`, Codex on the left of an agterm split and `codex-router tui` on the right. Each `new`
case starts a fresh thread in a fresh export of `lab-base`. Driver: `live/live.sh`; captures in
`live/cases/`.

| Case | Prompt                             | Decision                                                            |
| ---- | ---------------------------------- | ------------------------------------------------------------------- |
| L1   | Makefile lint target, one sentence | `downgrade` to micro `luna · medium`, Jev 93% vs 90% (fresh thread) |
| L2   | list `internal/` packages          | stays low `luna · high`: cheaper 80% < 90%                          |
| L3   | follow-up: biggest package         | stays low: cheaper 56%                                              |
| L4   | add `version --json` with a test   | stays low `luna · high`: stronger 51% < 79%; done, tests pass       |
| L5   | follow-up: fix `-tags lab` failure | `upgrade` to medium `sol · medium`, 95% ≥ 79%; fixed, exit_code=0   |
| L6   | USD tax design doc                 | `jump` from low to high `sol · high`, Jev 98%                      |
| L8b  | follow-up: line count              | `downgrade-pending`: cheaper 87% < 94% (Sol warm, Luna cold)        |
| L9   | review `git show HEAD`             | `upgrade` to medium `sol · medium`, 81% ≥ 75% (fresh thread)        |
| L10  | `make lint`, one line              | stays low: cheaper 75% < 90%                                        |
| L11  | "debug" the `-tags lab` failure    | `upgrade` to medium, activity `code`: the debug route did not fire  |
| L12a | "thanks, that works"               | `uncertain`, keeps medium                                           |
| L12b | follow-up: which file              | `downgrade-pending`: 90% < 92%                                      |
| L12c | follow-up: lines changed           | `downgrade-held`: same task, no passed check since the last edit    |
| L12d | unrelated question                 | `downgrade-pending`: 83% < 92%                                      |
| L12e | another unrelated question         | `downgrade` to low `luna · high`: second vote, 93% ≥ 92%            |

Findings:

- **A Stop hook that continues a turn makes input typed afterwards a steer.** The user's dev-flow
  Stop hook ran a prose check on L6's design doc, failed it and kept the turn going with no status
  timer. L7 and L8 were typed then and joined L6's turn (same turn id), each with a
  `stream-aborted stage=client` at that moment: Codex cancels the open stream when input arrives
  mid-turn. This is the likely source of the earlier `stream-aborted` warnings too (not verified
  for round 1). A finished turn shows `Worked for …`; the driver now waits for it.
- **The footer's window figure lags one turn.** After a cross-model switch Codex keeps the window
  of the model the turn started on: Sol at 258K on L11's first turn, Luna at 475K on L12e. The
  router's context guard uses the routed model's window. The user guide now says so.
- **The debug route rarely fires.** Jev labelled L5 and L11 `code`, as it labelled round 1's "run
  the failing test" `ops`.
- **The panel's baseline is the model Codex sent.** With `gpt-6-luna · high` in Codex's config,
  medium turns show a cost above baseline (`+1592%`), not a saving.

## Open items

- **Tax counts tokens, not dollars.** A move to Luna that writes 83k uncached tokens (~$0.008)
  raises the bar as much as a move to Sol (~$0.17). The token measure suits plan usage limits;
  USD answers a different question. Compare stay-versus-switch cost and plan-limit behaviour
  before changing the policy. Raising `downgradePivotTokens` would relax every model alike.
- **`stream-aborted stage=client`** on long `gpt-6.1-sol · xhigh` steps (C6, P2-1). The turns
  completed; the cause is unknown.
- **Classifier reads "run this failing test" as ops**, not debug (P1-1, F2-1), so the debug
  route at medium rarely fires from such prompts.
- **Not measured:** low-tier plan and review quality on Luna, long multi-turn sessions, design
  quality, large context.
