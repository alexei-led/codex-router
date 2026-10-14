# Live test, 2026-10-14

Codex CLI 0.162.1, `codex-router` from `feat/phase2-impl`, a throwaway Go repo with a failing
`Backoff` test. Codex ran in an agterm split; the panel (`codex-router tui`) ran in a second
session; transcripts are `agtermctl session text` captures. Logs are the session logs from
`~/.local/state/codex-router/logs/` (facts and ids only). Home and temp paths are shortened.

The test found six bugs, each fixed on the branch with a regression test (commits named below).
Files prefixed `01`–`10` predate some of the fixes; the check table says which file shows what.

## Checks

| Check                 | Result                                                                                                                                                                                                                                                                                                  | Evidence                                                                                        |
| --------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ----------------------------------------------------------------------------------------------- |
| Launch                | Pass. Panel shows `footer synced`; Codex footer shows the configured model.                                                                                                                                                                                                                             | `01-tui-now-before-fix.txt`                                                                     |
| Footer follows route  | Pass after fix 1. Footer `GPT-6-Luna high` → `GPT-6-Sol medium` mid-session; panel "This turn" matches.                                                                                                                                                                                                 | `02-codex-turn2-footer-switch.txt`, `02-tui-now.txt`                                            |
| Per-step rewrite      | Pass. Each step logs one `route decision` with the turn's route; mid-turn rewrites `from` the model the turn started on; upstream 200 on every request.                                                                                                                                                 | `06-router-session2.log`                                                                        |
| Manual `/model`       | Pass. Session-only pick (`s`) of `gpt-6-astra · high` → panel `◐ manual`, log `route manual`, next turn forwarded with no decision. `~/.codex/config.toml` mtime unchanged.                                                                                                                             | `03-tui-manual.txt`, `03-tui-now-manual.txt`                                                    |
| Resume                | Pass. Space in the panel: manual → on.                                                                                                                                                                                                                                                                  | `04-tui-paused.txt` (log `route command op=resume`)                                             |
| Pin next              | Pass. `p` → high: next turn `reason=pinned` on `gpt-6.1-sol · high`, pin consumed. The following turn stayed high with `downgrade-pending` (see fix 4).                                                                                                                                                 | `05-tui-pin-next.txt`, `06-router-session2.log`                                                 |
| Pause                 | Pass after fix 2. Requests forwarded unchanged (2 requests, 0 rewrites); footer restored to the user's `/model` pick.                                                                                                                                                                                   | `04-codex-paused-turn.txt`, `12-tui-pause-after-manual.txt`, `12-router-pause-after-manual.log` |
| Kill launcher         | As designed, with a known gap. `kill -9` mid-turn: Codex shows `Reconnecting… waiting for network`; the Codex TUI and private app-server are orphaned and keep drawing in the pane. `codex-router proxy --port <same port>` recovered the running turn to completion. A stale runtime dir stays behind. | `07-proxy-restart.out`                                                                          |
| Escalation            | Pass after fixes 4–6. `reason=escalation` low → medium, then `hold: escalation holds 2 turns`.                                                                                                                                                                                                          | `11-router-escalation.log`, `11-tui-escalation.txt`, `11-codex-escalation.txt`                  |
| Context guard         | Not run at >220k tokens. Fix 1 came from the guard wrongly holding every step (`reason=fit` at ~27k).                                                                                                                                                                                                   | `01-router-before-fix.log`                                                                      |
| Compaction            | Pass. `/compact`: one request, no decision, thread continues.                                                                                                                                                                                                                                           | `06-codex-escalation-probe-and-compact.txt`, `06-router-session2.log`                           |
| Classifier down       | Pass after fix 3. Key unset: log `classifier-disabled`, 3 requests / 0 rewrites, panel `Jev off`.                                                                                                                                                                                                       | `08-router-classifier-off.log`, `08-tui-classifier-off.txt`                                     |
| Panel follows restart | Pass after fix 5b. Panel without `--socket` reconnects to the restarted session.                                                                                                                                                                                                                        | — (observed: `disconnected` → `on`)                                                             |
| Hook line             | Not run: `-c hooks…` needs a persisted trust entry, which would edit `config.toml`.                                                                                                                                                                                                                     | —                                                                                               |
| Session token (after review) | Pass. With `2940f18`, Codex's requests through the real launcher and app-server carry the token and route normally; `curl` without it gets 401 on `/v1/models` and `/v1/responses` (log `unauthorized`), `/healthz` 200; the token is not in the app-server argv. | — (observed) |

## Bugs found and fixed

1. `0e21176` The live `/models` body has no `effective_context_window_percent`; the catalog
   treated every window as unknown, so the context guard kept every step on the original model.
   Now defaults to Codex's 95.
2. `a917651` Pause after a `/model` pick, then resume, restored the session-start model instead
   of the pick.
3. `3e2a7da` With no classifier key the panel said `Jev ready`; now `Jev off`.
4. `1a33aed`, `381768c`, `a8224e3` Every catalog model is `code_mode_only`: tools run inside a
   JavaScript `exec` cell whose output reports only how the script ended. Facts never saw an
   exit status, command or edit, so escalation never fired and the verification gate never
   passed (sticky `downgrade-pending`). Facts now read the nested calls and the printed status
   (`exit_code=N`, `exit code: N`, a stringified nested result).
5. `9e965e8`, `2682543` The panel kept redialing a dead session's socket: it resolved the newest
   session once, and the control client's own redial kept its updates open so the panel never
   saw the loss.

## Observations, not fixed here

- Escalation can fire on the first step of a turn from a repeated failure in the previous turn
  (inside the 40-item window) even when that turn ended green (`11-router-escalation.log`).
- In code mode an exit status exists only when the model prints it; a cell that prints nothing
  counts toward neither escalation nor verification.
- Codex refuses `-s`/`-a` with `resume` under `--remote` ("Permission overrides are not
  supported when resuming a remote task").
- SIGKILL of the launcher orphans the Codex TUI and the app-server (see Kill launcher).
- The orphaned app-server keeps sending the ChatGPT bearer token to the dead proxy port, and whoever binds that port next receives it (the recovery above shows such a listener gets the turn). No launcher-side fix exists on macOS; the docs tell the user to kill the orphan promptly.
