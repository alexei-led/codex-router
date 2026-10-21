# Routing modes: live check, 2026-10-21

The worktree build of `feat/routing-modes` ran in launcher mode in agterm, with its own config and state directories, against the logged-in test home `home-a`. The classifier was Jev (`jev-1.13.0`). The router config pointed `envFile` at the user's key file; the test never read it. The real `~/.codex/config.toml` and `~/.codex-work/config.toml` hashes did not change.

## Behavior

| Check | Result | Evidence |
| --- | --- | --- |
| The panel and `ctl status` show the mode and its source | `balanced (default)`, `cost (session)`, `cost (config)`, `quality (project)` | `02-panel-cost-move.txt`, `09-L2-panel-project.txt` |
| A mode switch mid-session waits for the gates | After `ctl mode cost`, a continuation of the same task held `gpt-6-luna · high` (`downgrade-held`: no passed check). The next task moved to `gpt-6-luna · medium` with reason `mode`, in one switch. | `03-L1-decisions.txt` |
| A pin bypasses the mode | In cost mode, `ctl pin --tier high` routed `gpt-6.1-sol · high`, the high tier's own pair, not cost's `gpt-6.1-sol · medium` | `04-panel-pinned.txt` |
| Resume keeps the override | After `/quit` and `codex-router -- resume <thread>`, the thread showed `cost (session)` | `05-resume-status.txt` |
| A new session does not inherit it | A new thread in the same repo started at `balanced (default)` | `07-new-session-status.txt` |
| `default` clears the override | `ctl mode default` returned the thread to its config or project mode and deleted its store file | `06-ctl-mode-default.txt`, `09-L2-default.txt` |
| Project file beats the config | `r2/.codex/router.json` with `quality` beat a profile config with `cost`; a session override beat both | `09-L2-panel-project.txt` |
| An invalid project file is ignored | `{"mode":"quality","routes":{}}` was ignored with a panel finding, and the config's `cost` applied | `10-invalid-project-panel.txt` |
| Log records carry the mode | Each `route decision` has `mode`, `modeSource`, `classified`, `model` and `effort` | `03-L1-decisions.txt` |

### `CODEX_THREAD_ID` and the sandbox (D8)

- Codex sets `CODEX_THREAD_ID` in its shell commands, and the value matched the session id.
- Inside Codex's default sandbox, `ctl` cannot connect to the router's socket (`connect: operation not permitted`).
- `!codex-router ctl mode balanced` in the Codex prompt runs outside the sandbox, and it worked.
- Bug found and fixed: with `CODEX_THREAD_ID` set, a sandboxed `ctl` reported the thread as "not listed" instead of `cannot reach the router`, so the skill never asked to run it outside the sandbox. After the fix, it reports `cannot reach the router` (`08-d8-sandbox-after-fix.txt`).

### Not run live

- **Home mode** with two threads in different cwds. Setting up a test home needs launchd units or a provider block in the test home's `config.toml`. The session's permission policy refused both, so this needs approval first. `TestHomeThreadsReadTheirOwnProjectFile` covers the engine side. The launcher sessions in `r2` and `r3` showed, in one panel, each thread with its own mode and source.

## Classifier run

`TestLiveClassifierRoutes` (build tag `live`) sent 20 fixed prompts to Jev once each. It then routed each answer as a new session's first turn under every mode. See `classifier.md`.

- **Classification:** 10 of 20 prompts were classified other than expected. Eight were one tier off: Jev rated small edits as micro and two medium tasks as high. Three got a different activity: explore→ops, ops→code, ops→debug. Two runs gave the same labels and routes; only the probabilities moved, by up to 0.05.
- **Routing:** no routing bug. In every row, cost ≤ balanced ≤ quality in spend.
- **Mode policies change the tier, not only the pair.** On two micro prompts at 0.87–0.89, balanced went to low (`gpt-6-luna · high`). Quality kept the baseline (`gpt-6.1-sol · medium`), because its downgrade bar is higher.
- **Estimate:** one uncached turn of 50k input and 2k output tokens per prompt, at list prices:

| Mode | Est. cost | Models | Efforts |
| --- | --- | --- | --- |
| cost | $1.146 | Luna 11, Sol 9 | high 5, medium 15 |
| balanced | $1.830 | Luna 5, Sol 15 | high 11, medium 9 |
| quality | $2.172 | Luna 2, Sol 18 | high 3, medium 8, xhigh 9 |

## One task per mode

The same four-turn task ran in a fresh copy of a small Go repo under each mode's config. The turns: add `Median` with tests; return an error on empty input; add `Variance`; review without edits. All three runs ended with `go test ./...` passing.

| Mode | Steps | Routes | Route changes | Router spend | Codex tokens |
| --- | --- | --- | --- | --- | --- |
| cost | 17 | `gpt-6-luna · high` ×17 | 0 | $0.0089 | 37.3k (+311k cached), 5.1k out |
| balanced | 12 | `gpt-6.1-sol · medium` ×12 | 0 | $0.1082 | 30.0k (+192k cached), 3.6k out |
| quality | 15 | `gpt-6.1-sol · medium` ×2, `gpt-6.1-sol · high` ×13 | 1 | $0.1170 | 24.3k (+271k cached), 5.2k out |

- **Router spend** is the panel's estimate at list prices (`20-sessions-run2.json`). Each session also counts one switch away from Codex's starting model.
- **Cost:** it moved to its target on the first turn, before any reply was measured.
- **Quality:** on turn 1 it waited (`mode-pending`, classified low), then moved up when tool calls showed ops work.
- **Turn 4 (review):** classified high in all three modes, but the upgrade bar held the tier (`upgrade-pending`).
- **Finding:** on this small task, cost cut spend by about 92% against balanced with no visible loss: the same tests passed. Quality changed only effort, and spent about 8% more. One run per mode is a direction, not a benchmark.
- **Discarded:** a first attempt is not counted. Its driver typed the next prompt before turns ended, so the turns ran into each other.
