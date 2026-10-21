# Routing modes: `cost | balanced | quality` (v0.9.0)

## Overview

- One setting trades cost against answer quality: `cost`, `balanced` (default, today's behaviour) and `quality`.
- The mode transforms the route the policy aims at. It does not change the classifier, its prompt or its labels.
- The mode comes from four layers: session override > project file > profile config > `balanced`.
- The `preset` config key and home-wide preset command are replaced by `mode`, `modes.<m>.policy` and a thread-scoped `mode` command.
- Source design: `router-modes/design.md` (manager scratchpad). Brief: `router-modes/brief-codex-router.md`.
- Branch `feat/routing-modes`, worktree `~/Workspace/codex-router.worktrees/routing-modes`, from `origin/main` at `e150b13`.

## Phase 1 findings

### Claims checked against the code

| Claim                                                         | Result                                                                                                                                                                           |
| ------------------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Presets `thrifty, balanced, quality` at `config.go:58-66`      | Confirmed at `config.go:57-64`.                                                                                                                                                  |
| Default preset at `default.go:60`                             | Confirmed.                                                                                                                                                                       |
| `Effective()` patches 7 fields and overwrites user policy     | Confirmed at `default.go:116-142`. `Load` decodes over `Default()` (`load.go:41-44`), so after decoding an explicit value and a default value look the same.                       |
| Docs call the values untuned                                  | Confirmed at `docs/configuration.md:101-115`.                                                                                                                                    |
| `preset` op is home-only                                      | Confirmed at `route/control.go:206-213`. The op is also allowed in `control/server.go:18-21`, and `route.go:54` has `Command.Preset`.                                            |
| TUI `s` and `1-3`                                             | Confirmed at `tui/keys.go:45,50` and `tui/model.go:248-250,303-305,325-326`. The Policy view has clickable preset buttons (`tui/screens.go:45-53`). |
| No ctl subcommand or env var for presets                      | Confirmed: `ctlOps` at `main.go:403`.                                                                                                                                            |
| Config path: flag, `$CODEX_ROUTER_CONFIG`, XDG                 | Confirmed at `main.go:274-281` and `load.go:14-53`. It uses `DisallowUnknownFields`. There is no project file.                                                                   |
| Per-thread intent is persisted                                | **Only in home mode.** The brain writes `intent.json` (`brain/intent.go`). Launcher mode wires no `Intent` or `SaveIntent` (`launcher/router.go:45-48`). See D2.                  |
| `route.Mode` name collision                                   | Confirmed at `route.go:14-21`. `Snapshot.Mode` already uses the wire name `"Mode"` (`route.go:168`).                                                                            |
| Decision log and `Decision` fixtures                          | Confirmed at `engine.go:706-707` and `route.go:66-88`. The fixtures are in `control/testdata/protocol-v2/`.                                                                     |
| Same-tier lateral path skips vote and dwell gates             | Confirmed at `choose.go:76-92,138-158`. The lateral path also blocks the cost model lever. See D3.                                                                               |
| Dwell resets on any route change                              | Confirmed at `engine.go:682-691`. It compares `d.Route` to `th.sent`, so it already compares pairs.                                                                             |
| Incumbent and finish rebuild from config                      | Confirmed at `choose.go:32-35`, `choose.go:104`, `engine.go:651,664`. See D4.                                                                                                    |
| The cache is effort-neutral                                   | Confirmed at `cost/cost.go:25-26` and `config.go:125`.                                                                                                                         |
| Import rules                                                  | Confirmed at `architecture_test.go:46-56`. `route` may import `catalog, classifier, config, cost, facts, home, policy, responses` only.                                           |
| Tests to rename                                               | `config_test.go:163 TestEffectivePresetValues`, `engine_test.go:697 TestPresetChangesTheEffectivePolicy`, and also `intent_test.go:109 TestRestartKeepsOnlyAPresetACommandSet`, `keys_test.go:376 TestToastResumeAndPreset`. |
| USD prices                                                    | Confirmed at `default.go:39-42`. Sol input is 2, Luna input is 0.1.                                                                                                              |
| Other worktrees overlap                                       | None. `feat/ops-diagnostics`, `perf/passthrough-fastpath` and `fix/tui-route-correctness` are all merged into `main`, with no uncommitted changes. No merge-order risk.            |

### Divergences

- **D1. One process serves many projects.**
  - In home mode, one brain serves every thread of a Codex home (`brain/doc.go`, `docs/architecture.md:277-312`).
  - The engine already gets each thread's cwd through `Deps.Cwd(threadID)`. In the brain it comes from the app-server `Thread.Cwd` (`brain/threads.go:33-55`). In launcher mode it is the launcher's own cwd (`launcher/router.go:52-58`).
  - Fix: resolve the project file per thread from `Deps.Cwd`, not once per process.
- **D2. Launcher mode does not persist intent.**
  - The user runs launcher mode. Five registry entries are `kind: session`, and `~/.config/fish/functions/codex.fish` wraps `codex-router --`. No brain `intent.json` exists under `~/.local/state/codex-router`.
  - So "a session override survives resume" fails for the user's setup unless launcher mode stores the override. See O1.
- **D3. The lateral economics rule blocks the cost model lever.**
  - `lateral` refuses a cheaper same-tier route whenever the switch tax is above 0 on a measured thread (`choose.go:147-152`).
  - A model change always has tax, because the target's prefix is cold.
  - Result: cost mode would never move a running medium-tier thread from Sol to Luna inside the tier. Adding dwell and verification is not enough. The pair change needs the bars a tier switch uses. See O2.
- **D4. "Stay" results would apply a new mode at once.**
  - `incumbent()`, `observedMove` and `finish` rebuild the route as `RouteFor(tier, activity)`.
  - After a mode switch, every stay result (uncertain, no-advice, hold, pending, `continue`) would rebuild the new mode's pair and switch without a gate.
  - Fix: the incumbent route is `th.sent`, the route in use. A `cell` carries the route it chose, and `finish` sends that route unless the fit guard moves the tier.
- **D5. Effort ladder.**
  - The catalog lists `max` and `ultra` beyond `xhigh` (`~/.codex/models_cache.json`). `config.Efforts` is `low..xhigh`.
  - `proj` stays inside `config.Efforts ∩ catalog levels`. With no catalog levels it uses `config.Efforts`. An effort outside the ladder, such as an empty route effort resolved to an unknown effort Codex sent, gets no effort lever.
  - The model lever needs both input prices above 0. An unpriced model gets the effort lever only.
- **D6. Wire names.** `Snapshot.Mode` is taken, so the new fields are `RoutingMode` and `RoutingModeSource` on `Snapshot`, `Decision` and `SessionInfo`. The log keys are `mode` and `modeSource`, as the design says.
- **D7. Partial policy patch.** Policy fields decode over defaults, so `modes.<m>.policy` needs a patch type with pointer fields. Validation runs on each mode's effective policy, so a patch cannot produce an invalid policy.
- **D8. `CODEX_THREAD_ID` has no in-repo evidence.**
  - Only docs assert it: `configuration.md:216`, `user-guide.md:380` and the plugin `SKILL.md:8`.
  - Today the plugin skill runs `ctl` from a Codex shell command with sandbox escalation, because the sandbox blocks local sockets.
  - Phase 3 checks it live, together with the `!` shell escape.
- **D9. Quality mode with the Codex defaults always lands on `xhigh`.**
  - The results are Luna/xhigh at low, Sol/high at medium (debug Sol/xhigh), and Sol/xhigh at high.
  - `default.go:20-24` records that xhigh "cost more and planned no better than high" in the 2026-10-15/16 lab.
  - This is a risk, not a blocker. See O7.

Expected results with the built-in routes (micro Luna/medium, low Luna/high, medium Sol/medium (debug Sol/high), high Sol/high):

| Tier         | cost                                   | balanced    | quality      |
| ------------ | -------------------------------------- | ----------- | ------------ |
| micro        | Luna/medium                            | Luna/medium | Luna/medium  |
| low          | Luna/medium (effort lever)             | Luna/high   | Luna/xhigh   |
| medium       | Luna/high (model lever, D = low)       | Sol/medium  | Sol/high     |
| medium debug | Luna/high (model lever)                | Sol/high    | Sol/xhigh    |
| high         | Sol/medium (D = medium, same model → effort lever) | Sol/high | Sol/xhigh |

### Risks

- **Seeding after restart.** A launcher or brain restart seeds the tier from the model (`cellForModel`). In cost mode, Luna/high seeds as `low` even when the thread ran at medium. The next medium answer is an upgrade vote with an unchanged pair. That is harmless: dwell resets only on a pair change.
- **Per-turn project file read.** There is one `stat` and a small read per user turn. Resolution happens at the turn's first step, so an edited file applies on the next turn.
- **Mixed versions during upgrade.** Five v0.8.0 launchers are running now. The O4 choice decides how a v0.9 panel shows them.
- **Session mode store growth (O1a).** Each thread given an override leaves one small file. It is not pruned.
- **Path traversal.** Thread ids name store files. Ids are checked against a strict charset before use.

### Open choices (recommendation first)

- **O1. Session override across `codex-router resume` in launcher mode.**
  - (a, recommended) Store a per-thread mode override in a file: `<state>/codex-router/modes/<home id>/<thread id>.json`, `{"mode":"cost"}`, written atomically with `statedir`. Launcher and brain use the same store, and the mode stays out of `intent.json`. Pins and pauses keep today's behaviour.
  - (b) Keep launcher overrides in memory only, like pins and pauses today. This breaks the design rule for the user's actual setup.
- **O2. Gate for a mode-driven same-tier pair change** (the activity is the same and only the pair differs).
  - (a, recommended) Gate it as a tier switch in a new pure `policy` function.
    - Cheaper: the mass at or below T must reach `downgradeMass + downgradeSlope·share(tax)`, the streak of votes ≤ T must reach `downgradeVotes`, and on the same task `DownAllowed` (dwell + passed check) is needed.
    - Stronger: the mass at or above T must reach `UpgradeBar(tax)`, with a streak of votes ≥ T of at least `upgradeVotes`.
    - The escalation hold allows only a stronger pair.
    - Activity-driven lateral moves keep today's rule, so `balanced` is unchanged.
  - (b) Only add dwell and verification to `lateral`. Because of D3, the cost model lever then never fires on a measured thread.
- **O3. Migration of `preset`.**
  - (a, recommended) Return an error that names the rename. Unknown field `preset` gives: `preset was renamed to mode in v0.9.0 (thrifty is now cost); see docs/configuration.md`. A `mode` of `thrifty` gives the same hint.
    - Nothing old is accepted, so this is not a compatibility shim. Remove it in a later minor.
    - The user's config has no `preset` key.
    - A stored `intent.json` `preset` is ignored by `json.Unmarshal`. No set-up home exists on this machine.
  - (b) Rewrite the user's file automatically. This writes user files and is a shim.
- **O4. Control protocol.**
  - (a, recommended) Stay on v2 with additive changes: add the `mode` op, remove the `preset` op, append `RoutingMode` and `RoutingModeSource`, and drop `Snapshot.Preset`. `SupportParts` is the precedent.
    - A v0.8 panel pressing `s` on a v0.9 engine gets "unknown op".
    - A v0.9 panel shows the mode of a v0.8 engine as `—`.
  - (b) Move to v3. The refusal is clean, but the new panel cannot show the 5 running v0.8 sessions until they restart.
- **O5. Project file.**
  - (recommended) The file is `.codex/router.json`. Lookup walks up from the thread's cwd to the nearest directory that holds `.git`, inclusive. The first file found wins. The file is read at each turn's first step.
  - An invalid file (any key other than `mode`, a bad value or bad JSON) is ignored. It is logged once per change and shown as a WORTH A LOOK finding, and the mode falls through to the profile config.
- **O6. A default mode per profile on the user's machine.**
  - Both homes share `~/.config/codex-router/config.json`. `codex-work.fish` sets only `CODEX_HOME`.
  - (a, recommended) Set `CODEX_ROUTER_CONFIG=~/.config/codex-router/work.json` in the work wrapper. That file holds the classifier keys plus `mode`.
  - (b) Add a per-home `mode` to the config. That is new scope beyond the design.
  - The mode for each profile is the user's call. Suggestion: personal `balanced`, work `cost`.
- **O7. Quality → xhigh (D9).** (recommended) Implement as designed, and say in the docs that the policy and quality values are untuned. Otherwise the manager changes the design.

## Development approach

- Test-first for the transform, the gate and config. Keep each step small, follow the repo style, and commit logically.
- Use a focused `go test ./internal/<pkg>` while working. Before the phase report, run the full gate: `make fmt-check vet lint test`, and `make build`.
- Time-dependent tests use `testing/synctest` and `net.Pipe`. Do not add const→var seams.
- Keep the plan's checkboxes current. Mark new tasks with ➕ and blockers with ⚠️.

## Solution overview

- **Transform.** `internal/route/mode.go` has one pure function:

  ```go
  func target(cfg config.Config, mode config.RoutingMode, t config.Tier, a config.Activity,
      sent config.Effort, levels func(model string) []config.Effort) config.Route
  ```

  - It implements design §2 from the configured baseline pairs (`resolve(cfg.RouteFor(...), sent)`).
  - It never starts from `th.sent`.
- **Per-step route function.** `step.routeFor(t, a)` replaces every `resolve(cfg.RouteFor(...))` in `choose`, `observedMove`, `chooseActivity`, `finish` and `window`. A pinned turn steps with mode `balanced`, so the pin bypasses the mode. A manual or paused thread does not route.
- **The route in use.**
  - `incumbent()` returns `th.sent` once the thread has sent a route. Before that it returns `routeFor(th.tier, th.activity)`.
  - `cell` gains `route`. `choose` sets it to the target, or to the incumbent route when the policy stays or a gate holds. `finish` sends `c.route` unless `FitTier` picks another tier.
- **Pair-aware gate (O2a).** In `choose`, when the policy keeps the tier, the activity is unchanged and `target ≠ incumbent route`, the new `policy.Lateral` bars decide. An activity change keeps the existing `lateral` rule. Uncertain and no-advice results keep the incumbent pair.
- **Mode resolution.**
  - It happens at `begin()`, per thread, in this order: `th.mode` (session) > `Deps.ProjectMode(threadID)` > `cfg.Mode` > `balanced`.
  - The source is one of `session`, `project`, `config` or `default`.
  - The step's policy is `cfg.Effective(mode)`.
- **Config.**
  - `Mode RoutingMode` (empty means not set, which resolves to `balanced` with source `default`).
  - `Modes map[RoutingMode]ModeConfig{Policy PolicyPatch}`. Built-in `cost` and `quality` patches use the current preset numbers. A user entry decodes over the built-in one, as `Classifiers` does.
- **Project file.** `config.ProjectMode(cwd)` lives in `internal/config/project.go`. The launcher and brain inject it as `Deps.ProjectMode`. The route package does no file I/O.
- **Session override store (O1a).** `internal/modestore` is used by the launcher and brain through `Deps.LoadMode` and `Deps.SaveMode`. A thread with an override counts as intent for LRU eviction.
- **Command surface.**
  - Control op `mode` (session scope; `cost|balanced|quality|default`, where `default` clears).
  - `codex-router ctl mode <m>`.
  - TUI: `s` opens the mode popover for the selected session: `1 cost  2 balanced  3 quality  4 default`.
  - Plugin skill row.
- **Visibility and logging.**
  - `RoutingMode` and `RoutingModeSource` appear on `Snapshot`, `Decision` and `SessionInfo`.
  - The panel detail header and the Policy view show them, e.g. `cost (session)`. `ctl status` prints them.
  - The `route decision` log gains `mode`, `modeSource` and `classified` (the classifier's tier answer, or empty). The route in use is already logged as `model` and `effort`.
  - `Snapshot.Routes` shows the effective base routes for the session's mode.

## Implementation steps

### Code

#### Task 1: Config `mode` and `modes`

**Files:** Modify `internal/config/config.go`, `default.go`, `validate.go`, `load.go`, `config_test.go`, `testdata/default.json`

- [ ] Replace `Preset` with `RoutingMode` (`cost|balanced|quality`) and `Config.Mode`, and add `ModeConfig` and `PolicyPatch` (pointer fields for every `Policy` field except the deprecated one)
- [ ] Built-in `Modes`: `cost` and `quality` with the current preset numbers; `balanced` empty
- [ ] `Modes.UnmarshalJSON`: decode each entry over the built-in one; unknown keys are errors
- [ ] `Effective(mode)`: `{...policy, ...modes[mode].policy}`
- [ ] Validation: `mode` is empty or a known mode; `modes` keys are known; each mode's effective policy validates; `preset` and `thrifty` get the rename hint (O3a)
- [ ] Tests: table for `Effective(mode)` (patch beats policy, user patch merges with built-in), validation errors, rename hints (rename `TestEffectivePresetValues` to `TestEffectiveModePolicy`)

#### Task 2: Project file reader

**Files:** Create `internal/config/project.go`, `project_test.go`

- [ ] `ProjectMode(cwd string) (mode RoutingMode, path string, err error)`: walk up to the nearest `.git` directory and read `.codex/router.json`
- [ ] Strict decode: `mode` is the only key; the value must be a known mode
- [ ] Tests (temp dirs): found in cwd, found in parent, stops at the repo root, absent, unknown key, bad value, bad JSON

#### Task 3: Transform

**Files:** Create `internal/route/mode.go`, `mode_test.go`

- [ ] `target(...)` per design §2 with `proj` over `config.Efforts ∩ levels`; model lever only when both input prices are > 0; floor `low`; `micro` untouched
- [ ] Table test: mode × tier × activity × levels → pair, with the defaults table above, a model without `xhigh` (quality falls to `P(T+1)`), unpriced models, an empty route effort resolved from `sent`, an effort outside the ladder, no catalog levels, and a check that a cost result never lands on micro

#### Task 4: Pair-aware gate

**Files:** Modify `internal/policy/policy.go`, `policy_test.go`

- [ ] `Lateral(in Input, tier Tier, cheaper bool) (ok bool, reason Reason, est *Estimate)` with the O2a bars; reuse `massAtOrBelow`, `massAbove`, `UpgradeBar`, `share`, `trailing`
- [ ] Tests: cheaper held on the same task without `DownAllowed`, cheaper passes after it, tax raises the bar, stronger needs the upgrade bar, an escalation hold refuses a cheaper pair, fresh threads need one vote

#### Task 5: Engine uses the route in use

**Files:** Modify `internal/route/choose.go`, `engine.go`, `engine_test.go`

- [ ] `step.mode`, `step.modeSource` and `step.routeFor`; a pinned turn uses `balanced`
- [ ] `incumbent()` from `th.sent`; `cell.route`; `finish` sends `c.route` unless the fit guard moves the tier
- [ ] In `choose`, a same-tier, same-activity pair change goes through `policy.Lateral`; uncertain and no-advice keep the incumbent pair
- [ ] Tests: in balanced, the existing engine tests pass unchanged; a stay reason after a mode switch keeps the incumbent route (D4); repeated cost steps do not compound; a pin bypasses the mode

#### Task 6: Mode resolution and session override

**Files:** Modify `internal/route/route.go`, `control.go`, `engine.go`, `snapshot.go`, `text.go`, `intent_test.go`, `engine_test.go`

- [ ] `Deps.ProjectMode`, `Deps.LoadMode`, `Deps.SaveMode`; `thread.mode`; resolution at `begin()`
- [ ] `OpMode` (session scope only; `default` clears; validation errors); remove `OpPreset`, `presetSet` and `Intent.Preset`
- [ ] `hasIntent()` includes the override, so eviction keeps it
- [ ] `Snapshot` and `Decision`: `RoutingMode` and `RoutingModeSource`; `Policy` is the thread's effective policy; `Routes` holds the mode's base routes; the finding text no longer says "preset"
- [ ] Tests: precedence (session > project > config > default) as a table, `default` clears, a new thread does not inherit, a project error falls through with a finding, the mode op rejects home scope and unknown values (rename `TestPresetChangesTheEffectivePolicy` to `TestThreadModeChangesTheEffectivePolicy`, and replace `TestRestartKeepsOnlyAPresetACommandSet`)

#### Task 7: Session mode store (O1a)

**Files:** Create `internal/modestore/modestore.go`, `modestore_test.go`; Modify `internal/launcher/router.go`, `internal/brain/brain.go`, `cmd/codex-router/architecture_test.go` if a rule is needed

- [ ] Load and save per thread under `<state>/codex-router/modes/<home id>/`, written atomically, `0700`/`0600`; thread id charset check
- [ ] Wire it into the launcher and brain together with `ProjectMode(Deps.Cwd(id))`
- [ ] Tests: round trip, clear deletes the file, a bad id is refused, an unreadable file is logged and ignored

#### Task 8: Decision log

**Files:** Modify `internal/route/engine.go`, `engine_test.go`

- [ ] Add `mode`, `modeSource` and `classified` to `route decision`; log `route command` with `mode`
- [ ] Test: capture the slog record and assert the fields

#### Task 9: Control wire and `ctl mode`

**Files:** Modify `internal/control/server.go`, `testdata/protocol-v2/*`, `wire_test.go`, `internal/route/sessions.go`, `cmd/codex-router/main.go`, `main_test.go`

- [ ] Allow the `mode` op; update the fixtures (commands, snapshots, decision, sessions list)
- [ ] `ctl mode <cost|balanced|quality|default>` through `CODEX_THREAD_ID` or cwd, as the other subcommands do; `ctl status` prints the mode and its source
- [ ] Tests: argument errors, sent command, output text, status line

#### Task 10: TUI

**Files:** Modify `internal/tui/keys.go`, `model.go`, `overlay.go`, `screens.go`, `detail.go`, `mouse.go`, `tui.go`, `render.go`, tests and goldens

- [ ] `s` opens the mode popover for the selected session (`1-4`); the buttons on the Policy view act on the session; the toast names the session
- [ ] The detail header and Policy view show `mode (source)`; an older engine shows `—`
- [ ] Tests: key and mouse actions send a session-scoped `mode` command, `4` sends `default`, goldens updated (rename `TestToastResumeAndPreset`)

#### Task 11: Plugin skill

**Files:** Modify `plugins/codex-router/skills/router/SKILL.md`

- [ ] Add a `ctl mode` row and say what `default` does

### Tests (cross-cutting, beyond the per-task tests)

- [ ] Brief regression: during an unverified medium continuation before the dwell ends, switch balanced→cost while the advice stays medium. The router keeps Sol/medium, then moves to Luna/high only after the dwell, a passed check and the bars, with exactly one switch recorded (`Stats.Switches` +1, one `switchedAt` change)
- [ ] Same with quality: Sol/medium → Sol/high with no cache tax
- [ ] Resume keeps the override (launcher restart with the store; brain restart); a new thread starts without it
- [ ] Every failure path returns a clear error: bad config `mode`, bad `modes` key, bad project file, bad op value, bad ctl argument

### Docs

- [ ] `docs/configuration.md`: `mode`, `modes` (untuned values, merge rule), project file (`.codex/router.json`, mode only, may be gitignored), migration note
- [ ] `docs/user-guide.md`: what each mode does, where it comes from, when a change applies (pending, not immediate; pin to force), panel keys, `ctl mode`, glossary
- [ ] `docs/architecture.md`: transform, pair-aware gate, route in use, precedence, store, wire, log fields (with a mermaid diagram of the precedence and the gate)
- [ ] README: one line on modes, if the front page lists features
- [ ] Screenshots: `go run ./scripts/render-docs` (needs `rsvg-convert` for PNG); update the sample data with the mode; regenerate `router-tui-policy`, `router-tui-popover`, `router-tui-help`, plus any image showing the detail header; `go test ./scripts/render-docs`

### Config and migration

- [ ] Rename hints (O3a) and a migration section in `docs/configuration.md` and the release notes
- [ ] Stored state: a `preset` in `intent.json` is ignored (no `intentVersion` bump); release notes say a home-wide preset picked in the panel is gone
- User machine (list only; the manager applies after release with the user's approval):
  - `~/.config/codex-router/config.json`: no change needed (keys: `classifier`, `classifiers`). Optional: add `"mode": "<personal default>"`.
  - Work profile (O6a): create `~/.config/codex-router/work.json` (classifier keys + `"mode": "<work default>"`), and in `~/.config/fish/functions/codex-work.fish` set `CODEX_ROUTER_CONFIG="$HOME/.config/codex-router/work.json"`.
  - After installing v0.9.0: restart the running `codex-router` launchers (5 now, all v0.8.0) to pick up the new code.
  - No `intent.json` exists under `~/.local/state/codex-router`, so stored state needs no migration.

### Live agterm verification (Phase 3)

Setup:

- Build `.bin/codex-router` in the worktree.
- Use isolated `XDG_CONFIG_HOME`, `XDG_STATE_HOME` and `--config` under `~/.cache/codex-router-live/routing-modes/`, and the existing logged-in test homes `~/.cache/codex-router-live/home-{a,b}`.
- Create my own sessions: `agtermctl session new --workspace-name ai-thingz --name "codex-router live test" --no-select`.
- Never touch the installed binary, the user's config or state, or other sessions.

Scenarios. Evidence for each is `agtermctl session text` captures plus `route decision` log lines from the isolated state dir.

- [ ] Each mode on the same prompt set (micro, low, medium, high): the routes match the table above
- [ ] A mode switch mid-session (balanced→cost): pending first, the switch after the gates; one switch in the log
- [ ] Session override vs project file vs profile config; `default` clears the override
- [ ] `codex-router resume` keeps the override; a new session does not inherit it
- [ ] A pin bypasses the mode
- [ ] The panel shows `mode (source)`; capture the doc screenshots
- [ ] Log records carry `mode`, `modeSource`, `classified`, `model`, `effort`
- [ ] D8: from inside Codex, check `CODEX_THREAD_ID` in a shell command and whether `!codex-router ctl mode cost` works in the sandbox; document the result
- [ ] Close the test sessions

### Release (Phase 4)

- [ ] `plugins/codex-router/.codex-plugin/plugin.json` version `0.9.0`; `make release-check VERSION=v0.9.0`
- [ ] `docs/releases/v0.9.0.md`: what changed, migration (`preset`→`mode`, `thrifty`→`cost`, home preset gone, restart launchers), untuned values, live evidence summary
- [ ] Full gate: `make fmt-check vet lint test vuln build`
- [ ] Move this plan to `docs/archive/plans/`
- [ ] Push `feat/routing-modes`, open the PR, stop before merge, tag or publish

## Post-completion (manager, with the user's approval)

- Merge, then tag `v0.9.0` (annotated, `--cleanup=verbatim -F docs/releases/v0.9.0.md`); verify the release workflow.
- Apply the user-machine changes listed above and restart the launchers.
