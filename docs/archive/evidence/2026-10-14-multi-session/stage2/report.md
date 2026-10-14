# Stage 2 live test, 2026-10-14

Codex 0.162.1 on macOS, codex-router from `feat/multi-session`, driven by `scripts/live/stage2.sh`
in the agterm workspace `router-live-2`.

- Homes: the two logged-in test homes `~/.cache/codex-router-live/home-a` and `home-b`.
  `codex-router setup --home` ran on each, and the plugin was installed into each from the local
  marketplace.
- Sessions: 3 plain `codex` sessions in home A, 2 in home B, across throwaway repos and worktrees.
  One locked panel and one all-sessions panel.
- Classifier: on. The router config pointed `envFile` at the user's key file (passed as a path,
  never read by the test). Both brains logged `Jev ready`, and both homes show classifier
  decisions.
- Safety: the real `~/.codex/config.toml` and `~/.codex-work/config.toml` sha256 were unchanged
  (`c409e1eb…1486`). Setup ran only on the test homes. No codex-router units are left for real
  homes.

## Checks

| #   | Check                                    | Result                                                                                                                                                              | Evidence                             |
| --- | ---------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------ |
| 1   | 5 sessions under 2 homes, footers synced | pass. Every Codex footer shows the routed model.                                                                                                                    | `03-*`, `16-*`                       |
| 2   | `kill -9` brain A mid-turn               | pass. No `Reconnecting`, the turn finished, launchd restarted the brain within about 1 s, the paused session stayed paused, home B was unaffected, routing resumed. | `04-*`, `05-*`                       |
| 3   | `kill -9` edge A mid-reply               | pass. The new edge held the same socket (`lsof`), `/healthz` answered 0.108 s after the kill, Codex retried and the turn finished. Stall about 0.1 s.               | `06-*`                               |
| 4   | Brain upgrade (`launchctl kickstart -k`) | pass after fixes. No turn interrupted; the route was kept across the restart.                                                                                       | `07-*` (before fix), `08-*`, `09-*`  |
| 5   | Daemon A restart                         | pass. The brain reconnected and resubscribed; labels and footers stayed the same.                                                                                   | `10-*`                               |
| 6   | Home isolation                           | pass. No thread of one home in the other home's logs. Neither the raw ChatGPT account id nor its hash is in any router log or state file.                           | `11-isolation-and-account-check.txt` |
| 7   | `setup --undo` for both homes            | pass, with one caveat below. Router blocks removed, units gone.                                                                                                     | `17-down-undo.txt`, `rerun-undo/`    |

## Bugs found and fixed

Each product fix has a regression test that fails without it.

1. **`58f88d5`: every session went `manual` after its first turn, and the first turn was not
   routed.** Codex opens a thread's first request with an `additional_tools` item, which the
   router took for an earlier turn. And `thread/resume` reports the model's default effort as
   empty, which the router read as a `/model` pick. Tests:
   `TestHoldUnseededHoldsAThreadAlreadyInConversation`, `TestResumedOnAKnownThread`,
   `TestSettingsAtTheModelDefaultEffortAreNotAPick`.
2. **`3bb5d19`: `kill -9` of the brain mid-turn made the session `manual`.** The running turn's
   later steps still carry the settings the turn started with. Pick detection now needs an
   earlier request on the thread. Test: `TestBrainRestartMidTurnIsNotAPick`.
3. **`b0a27df`: after a brain restart mid-turn, the rest of the turn counted as a new turn**
   (an extra classifier call and a possible model switch mid-turn). Test:
   `TestHoldEndedByARunningTurnsReplyLeavesThatTurnUnrouted`.
4. **`3e3de5d`: after a brain restart mid-turn, the rest of the turn went back to Codex's own
   model**, which rewrites the prompt cache. Steps of a held turn now keep the held route. Test:
   `TestHeldTurnKeepsTheSeededRouteMidTurn`.
5. **`7598477`: the hook's switch line read `router: high ·  → …` on a pinned turn.** Empty parts
   are left out. Test: cases in `TestMessage`.
6. **`affaf8a`: the router skill failed inside Codex's sandbox**, which blocks unix sockets, and
   said `no running session`. `ctl` now says it cannot reach the router and to run outside the
   sandbox; the skill asks for escalated permissions. Test: `TestCtlNamesASandboxBlockedSocket`.
7. **`898f2fa`: every approval in a routed thread failed with `approval request failed`.** Also
   in stage 1. The router's app-server client answered approval requests with method-not-found,
   and the app-server takes the first answer from any client. The router now leaves them to the
   TUI. Test: `TestServerRequestIsLeftToTheTUI`. Evidence: `14-*`, `15-*`.
8. Test script: `stage2.sh down` ran `pkill -f "$RUN"`, which also killed its own output pipe, so
   the first undo output was lost. Fixed in the script; check 7 was re-run on a fresh run
   (`rerun-undo/`).

## Not fully verified

- **Undo is byte-identical except for one line Codex wrote itself** during the run
  (`"gpt-6.1-sol" = 1` → `2`, a counter in the TUI's own table). Undo removed exactly the router
  blocks. Byte identity without Codex writes is covered by `internal/setup` tests.
- **Link frames were not captured live.** `TestLinkFramesCarryOnlyTheAccountHash` covers them.
- A brief `Reconnecting` between the 4-5 s screen polls during the edge kill cannot be ruled
  out.

## Findings, not changed

1. The test homes' default `gpt-6.1-sol · low` maps to tier high (tier is by model; effort is
   ignored). A thread seeded there rewrote trivial turns to `gpt-6.1-sol · high` until downgrade
   votes allowed a move.
2. Codex TUIs do not restart a stopped daemon. They show `Reconnecting to server…` until
   `codex app-server daemon start`. After that they reconnected, and the brain resubscribed every
   thread.
3. A session paused after a brain restart writes back the seeded route (the router's last write),
   since Codex's own route is unknown then.
4. A brain started before a home's first Codex run has no model list and does not route until it
   restarts. Not exercised: both homes had `models_cache.json` before setup.
5. `TestPluginHooksReturnFastWithServicesDown` can miss its time limit under heavy host load and
   passes on re-run.
6. `scripts/live/stage1.sh` has the same `pkill` pattern as bug 8 (not changed).
