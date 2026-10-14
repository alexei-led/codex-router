# Stage 1 live test, 2026-10-14

Codex 0.162.1, codex-router from `feat/multi-session`, driven by `scripts/live/stage1.sh` in the
agterm workspace `router-live`.

Repos were under a temp run dir: `alpha` (worktrees `feat-a`, `feat-b`) and `beta` (worktrees
`feat-c`, `feat-d`). Sessions:

| #   | Home    | Repo / worktree |
| --- | ------- | --------------- |
| 1   | default | alpha · feat-a  |
| 2   | default | alpha · feat-b  |
| 3   | default | beta · main     |
| 4   | work    | beta · feat-c   |
| 5   | work    | beta · feat-d   |

Session 1's split ran `codex-router tui --session 'default · alpha · feat-a'` (locked panel).
Session 6 ran `codex-router tui` (all-sessions panel). Every process had the run's own
`XDG_STATE_HOME` and `XDG_RUNTIME_DIR`.

The classifier key was not in the fish login env, so every router ran `Jev off` and turns were
routed by pins only. Classifier decisions were not exercised here; the stage 2 run did exercise
them ([stage 2 report](../stage2/report.md)).

The real `~/.codex/config.toml` sha256 was the same before and after (`c409e1eb…1bc1486`;
`~/.codex-work/config.toml` is a symlink to it).

## Checks

All 9 pass.

| #   | Check                                                                                                                                                     | Evidence                                          |
| --- | --------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------- |
| 1   | `sessions --json` lists 5 sessions with the right homes (`68a7f56701a9` default, `367e0181334d` work) and branch labels                                   | `02-sessions.json`                                |
| 2   | All-sessions panel: 2 home groups, `]` / `[` cycle all 5, Sessions tab rows match                                                                         | `03-*`                                            |
| 3   | Locked panel shows only its own header: no top bar, no tab 0                                                                                              | `02-locked-panel.txt`                             |
| 4   | A pinned prompt in session 1 changed only session 1: footer GPT-6-Luna high → GPT-6-Sol medium, 1 reply. Sessions 2-5 stayed at 0 replies                 | `05-*`                                            |
| 5   | `space` paused only session 2; `P` reached only the two work routers; both resumes worked                                                                 | `07-*`, `08-*`, `09-*`, `09-route-commands.jsonl` |
| 6   | Pin from the locked panel (high), then from the all-sessions panel (medium): the last pin won, exactly one `reason=pinned` decision, next turn not pinned | `04-*`, `06-*`                                    |
| 7   | A `/model` pick in session 3 showed `◐ manual` on that row only                                                                                           | `10-*`                                            |
| 8   | `kill -9` of session 2's router: row `disconnected` within 1.5 s, registry entry pruned, row gone after 10 s; session 5 still routed                      | `12-*`                                            |
| 9   | `/new` in session 4 added a row; the old row left once its thread had been idle 10 min                                                                    | `11-*`, `13-*`                                    |

## Bugs found and fixed

1. `9fbb8b2`: every session was listed twice. After the first prompt, Codex starts a side thread
   that writes the thread title. Its requests look like a user turn (`request_kind=turn`, a
   `turn_id`, no subagent header), but carry `thread_source="thread_title"`. Requests whose
   source is set and is not `user` now pass through unchanged. Tests: `TestClassify`,
   `TestSideThreadIsNotASession`. Check 1 re-run: 5 sessions.

Found later in the stage 2 run and also present in stage 1: the router's app-server client
answered approval requests, which broke every approval (`898f2fa`, see the stage 2 report).

## Observations, not changed

- With the classifier off, a thread that was never routed reports tier `micro`, so its first
  switch reads `micro→medium`. Display only.
- `kill -9` of a router leaves its private app-server running; it was stopped by hand.
- Router logs are saved as `.jsonl` because `.gitignore` ignores `*.log`.
