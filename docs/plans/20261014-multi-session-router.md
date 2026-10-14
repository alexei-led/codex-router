# Multi-session codex-router

## Overview

The user runs 1-20 Codex agents at once, in different git worktrees and projects, under two
Codex homes: `~/.codex` (personal account) and `CODEX_HOME=~/.codex-work` (work account). Today
each `codex-router` run serves one Codex session, and `codex-router tui` attaches to the newest
one only.

This plan delivers, in one run:

- **Stage 1.** A session registry and a multi-session TUI on top of today's per-session routers.
  `codex-router tui --session X` shows one session with no selection UI. `codex-router tui`
  shows all sessions of all homes: a top bar with `[` `]` and a Sessions tab. Any number of
  panels can be open at once.
- **Gate.** Live spikes for stage 2 on throwaway Codex homes. Stage 2 runs only if they pass.
- **Stage 2.** Per home, a small `edge` (holds the provider port, fail-open relay) and a
  restartable `brain` (engine, classifier, control, footer sync through that home's shared Codex
  daemon). Plain `codex` works. Installed as a Codex plugin plus `codex-router setup`. Runs
  under launchd (macOS) or systemd user units (Linux). The service manager holds the port, so
  an edge crash or upgrade does not free it.
- **Live tests** of both stages with several real Codex instances in agterm.

Design source: the "Один router на много сессий Codex" design note (artifact v2) and the peer
review with Codex on 2026-10-14. Facts about Codex 0.162.1 below are from source reading by two
research agents unless marked **[spike]**.

## Context (from discovery)

- Go module `github.com/alexei-led/codex-router`, Makefile targets `fmt lint test build`
  (`make all`), CI calls make targets.
- `internal/launcher/router.go:45` `codexHome` resolves `CODEX_HOME` (for `models_cache.json`).
- `internal/launcher/runtime.go:29,96` `RuntimeBase` (`XDG_RUNTIME_DIR`/`TMPDIR`) and
  `NewestControlSocket`. Discovery breaks across shells with different `TMPDIR`.
- `internal/control`: JSON-lines socket; `Message{snapshot|ack|error}`; `Handler{Apply,
Snapshot}`. `route.Command{Op, Tier, Preset}` has no target. `route.Snapshot` is one focus
  thread plus engine-wide `Mode`, `Pin`, `Preset`.
- `internal/tui`: Bubble Tea; `Options{Socket, Locate}`; pure `Render`/`RenderPlain` with golden
  files in `internal/tui/testdata`; `scripts/render-docs` renders doc images.
- `route.Engine` implements `proxy.Decider`, `proxy.Observer`, `control.Handler`. The launcher
  wires them. This seam becomes the edge-brain socket in stage 2.
- Live test precedent: `docs/archive/evidence/2026-10-14/live-test-report.md` (agterm splits,
  `agtermctl session text` captures).

Codex facts this plan relies on:

| Fact                                                                                                                                                                                                                                                                                  | Source                                                                                   |
| ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ---------------------------------------------------------------------------------------- |
| A plugin can ship skills, hooks, MCP servers, apps. It cannot set `model_provider` or `model_providers.*`.                                                                                                                                                                            | `core-plugins/src/manifest.rs:50-73`, `config/src/types.rs:1035`                         |
| Plugin manifest `.codex-plugin/plugin.json`; marketplace `.agents/plugins/marketplace.json`; install `codex plugin marketplace add owner/repo` then `codex plugin add NAME@MARKETPLACE`.                                                                                              | `core-plugins/src/store.rs:474`, `marketplace.rs:21-24`, `cli/src/marketplace_cmd.rs:66` |
| Hook env has `PLUGIN_ROOT`, `PLUGIN_DATA`; stdin has `cwd`, `session_id`, `model`. On success the process group is not killed; a daemon must redirect stdio. `async: true` exists.                                                                                                    | `hooks/src/engine/command_runner.rs:284,317-339`, `discovery.rs:264-270,533`             |
| Plain `codex` with the provider in `config.toml` runs its thread in the home's shared daemon, with the TUI's cwd. `-c`, `--profile`, `--no-daemon` run embedded.                                                                                                                      | `tui/src/daemon_startup.rs:66-131`, `app_server_session.rs:2300-2311`                    |
| Daemon socket `$CODEX_HOME/app-server-control/app-server-control.sock`, mode 0600, no peer check. `thread/loaded/list`, `thread/resume {excludeTurns}`, `thread/settings/update` (experimental). `thread/started` goes to all clients; `thread/settings/updated` only to subscribers. | `unix_socket.rs:31-71`, `common.rs:552-583,1962`, `outgoing_message.rs:199-207,771-781`  |
| The daemon unloads a thread only when idle with no subscribers: a subscriber must `thread/unsubscribe`.                                                                                                                                                                               | `thread_lifecycle.rs:436`                                                                |
| Each `thread/start` rebuilds config from the home, so a new provider should apply without a daemon restart. **Unverified at runtime.**                                                                                                                                                | `config_manager.rs:429-459`                                                              |
| TUIs reconnect to a restarted daemon (0, 1, 2, 4, then every 8 s) and resume their thread.                                                                                                                                                                                            | `tui/src/app/reconnect.rs:30-80`                                                         |
| Provider `base_url` is TCP only in 0.162.1 (no unix socket).                                                                                                                                                                                                                          | `model-provider-info/src/lib.rs`, Codex peer                                             |
| A dead provider port: the turn retries forever ("Reconnecting… waiting for network", 5 s doubling to 60 s) and resumes when the port listens again.                                                                                                                                   | `core/src/responses_retry.rs:93-118`, spike 1b **[spike]**                               |

## Development Approach

- **testing approach**: Regular (code first, then tests in the same task).
- complete each task fully before moving to the next; small, focused changes.
- **CRITICAL: every task MUST include new/updated tests** for code changes in that task: success
  and error paths, table-driven where inputs form a matrix. Mock only boundaries (filesystem,
  process, clock, network, `launchctl`/`systemctl`).
- **CRITICAL: all tests must pass before starting the next task**: `make fmt lint test build`.
- **CRITICAL: update this plan file when scope changes during implementation.**
- No backward-compatibility shims: the control protocol, TUI and hook change together.
- Never print or log header values, tokens, auth, or request/response text.

## Testing Strategy

- **unit tests**: required in every task (see above). Golden render tests for every new TUI view
  (`RenderPlain` into `internal/tui/testdata`, width 80 and 140).
- **integration tests** (normal `go test`): fake Codex app-server and fake daemon over a unix
  socket (pattern: `internal/launcher/fakecodex_test.go`); edge with `LISTEN_FDS` via a child
  process; registry with several endpoints.
- **live tests** (Tasks 12, 23): real Codex 0.162.1 in agterm, driven by `agtermctl`. Evidence
  in `docs/archive/evidence/<run-date>-multi-session/`. Not part of `make test`.

## Progress Tracking

- mark completed items with `[x]` immediately when done.
- add newly discovered tasks with ➕ prefix; document issues/blockers with ⚠️ prefix.
- live tests: each bug found gets a fix commit and a regression test, recorded in the stage's
  evidence report like the 2026-10-14 report.

## Workflow Execution Notes

- Task order is the dependency order. `Depends on` lines list hard dependencies. Only tasks
  marked **parallel group** may run at the same time; their Files blocks do not overlap. Every
  other task runs alone. Stage 2 tasks 15-22 run strictly in sequence (they share `route`,
  `proxy` and `cmd/codex-router/main.go`).
- **[live]** tasks need agterm, a real Codex, real accounts and the classifier key. They run
  serially, in the main checkout (not a worktree agent), one at a time.
- **Gate (Task 14)** decides stage 2. If any gate criterion fails, the workflow stops after
  writing the gate report. It does not start Task 15.
- Safety rules for every task and agent:
  - Never edit `~/.codex/config.toml` or `~/.codex-work/config.toml`, never install plugins or
    hooks into those homes, never copy their `auth.json`, and never stop, restart or connect
    to the user's shared daemons. (Codex itself writes rollouts, sqlite and token refreshes
    into a home it runs in; that is expected.)
  - Stage 1 live runs use the real homes through the launcher (`-c` overrides, private
    app-server), as the 2026-10-14 live test did.
  - Stage 2 runs use two dedicated **test homes** only: `~/.cache/codex-router-live/home-a` and
    `home-b`, each logged in once by the user with `codex login` (Task 13). Copying `auth.json`
    is forbidden: ChatGPT refresh tokens rotate, so a refresh in a copy logs the real home out.
  - Each live task asserts that the real `config.toml` files are unchanged (sha256 before and
    after) and runs with a temp `XDG_STATE_HOME` so its registry and state never mix with the
    user's.
  - Any cleanup that stops a daemon or deletes a dir first checks that the target path is under
    `~/.cache/codex-router-live/` or the run's `mktemp` prefix, and refuses otherwise.
  - Never run `codex-router setup` against a real home in this plan.

## Solution Overview

```mermaid
flowchart LR
  subgraph S1["Stage 1: per-session routers"]
    r1["codex-router<br/>(launcher)"] --> e1["endpoint.json<br/>kind=session"]
  end
  subgraph S2["Stage 2: per home"]
    ed["edge<br/>socket from launchd/systemd"] <-->|"decide, observe"| br["brain"]
    br --> e2["endpoint.json<br/>kind=brain"]
    br <-->|"daemon socket"| d["home's Codex daemon"]
  end
  reg[("registry<br/>$XDG_STATE_HOME/codex-router/registry")]
  e1 --> reg
  e2 --> reg
  tui["codex-router tui<br/>(one or all sessions)"] --> reg
```

Key decisions:

| #   | Decision                                                                                                                                                                                                                                                                                                                                                                                                                                                                               | Reason                                                                                                                                                                                                                                                                                        |
| --- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| M1  | **Session** = one Codex main thread, identity `(home_id, thread_id)`. Label `home · repo · branch` (+ 4-hex id suffix on collision) is display only.                                                                                                                                                                                                                                                                                                                                   | Same identity in both stages. `/new` in Codex is a new session. Codex peer: never key on `thread_id` alone.                                                                                                                                                                                   |
| M2  | `home_id` = first 12 hex of sha256 of the canonical `CODEX_HOME` path. Home name: `.codex` → `default`, `.codex-<x>` → `<x>`, else the basename.                                                                                                                                                                                                                                                                                                                                       | Stable, no setup needed, no config field.                                                                                                                                                                                                                                                     |
| M3  | Registry root `$XDG_STATE_HOME/codex-router/registry` (else `~/.local/state/...`), dir 0700, files 0600, atomic replace. An entry is a hint; a `hello` handshake confirms `endpoint_id` and `home_id`. Dead entries are pruned by readers. Service units get the resolved state dir as an explicit `--state-dir` flag, because launchd and systemd do not see the shell's `XDG_STATE_HOME`.                                                                                            | Shared by all shells regardless of `TMPDIR`. Codex peer review; plan review.                                                                                                                                                                                                                  |
| M4  | Control protocol v2: `hello`, `sessions` (list push), `subscribe {session}`, per-session snapshots, commands with `session` and `id`. The ack carries the id so `Send` can match it. Last-write-wins. No dedupe cache, no stale-revision rejects, no cross-panel event line in v1.                                                                                                                                                                                                     | Multi-panel, multi-session. Nothing retries commands, and one user with a few panels does not need optimistic concurrency. Upgrade trigger: lost-update or double-apply bugs.                                                                                                                 |
| M5  | Pause and pin are per thread. Pause also exists per home (engine-wide). Preset is per home. Manual hold stays per thread. No cross-home pause. The TUI sends a `scope=home` command to **every** endpoint of the focused home, and each endpoint applies it to its own threads, whatever its kind.                                                                                                                                                                                     | User asked for per-session control. One code path for stage 1 sessions and stage 2 brains. Codex peer review; plan review.                                                                                                                                                                    |
| M6  | Stage 2: one edge + one brain **per home**, separate port, token, state, units. No shared brain across homes.                                                                                                                                                                                                                                                                                                                                                                          | Isolates accounts, daemon connections and restarts. Codex peer review.                                                                                                                                                                                                                        |
| M7  | Edge-brain link: unix socket `$STATE/codex-router/homes/<home_id>/brain.sock` (0600), length-prefixed frames: JSON header + raw body. Decide has the existing deadline (classifier timeout + 200 ms); any failure = forward unchanged.                                                                                                                                                                                                                                                 | Reuses `proxy.Decider`/`Observer` 1:1. Bodies stay local; a memcpy per step is cheap.                                                                                                                                                                                                         |
| M8  | The service manager owns the edge's listening socket: systemd `.socket` unit (`LISTEN_FDS`, pure Go) on Linux; launchd `Sockets` + `launch_activate_socket` on macOS via `github.com/ebitengine/purego` (no cgo). Edge without activation (dev) binds itself and warns.                                                                                                                                                                                                                | Port never freed on edge crash or upgrade, so no port-steal window and no reconnect stall. purego keeps `CGO_ENABLED=0` cross-builds. Fallback if the gate spike fails: cgo bridge built on a macOS runner.                                                                                   |
| M9  | Session token: setup writes `http_headers = { "X-Codex-Router-Token" = "<token>" }` into the router provider block, and the token into `$STATE/.../token` (0600) for the edge. Setup refuses a symlinked `config.toml` unless `--follow-symlink`, and sets the file to 0600.                                                                                                                                                                                                           | The daemon does not inherit our env, so `env_http_headers` cannot work. A symlink usually means a dotfiles repo, where a token would get committed.                                                                                                                                           |
| M10 | `config.toml` edits: marked blocks (`# >>> codex-router >>>` … `# <<<`), `model_provider = "router"` inserted before the first table, provider table appended. Validate the result with `github.com/BurntSushi/toml` before writing. Refuse if the user already sets `model_provider`. Timestamped backup 0600. `--undo` removes the blocks.                                                                                                                                           | Minimal, reversible edits that keep the user's comments. A real TOML parser for validation is not a few lines.                                                                                                                                                                                |
| M11 | Brain restart resets learned state (tier, votes, escalation, ledger). On resume the brain seeds each thread's current route from the settings `thread/resume` returns, and holds the thread on that route until its first completed reply measures the cache; then it routes normally. User intent (home pause, thread pause, manual hold, pending pin, preset) is written atomically on change to `intent.json` and reloaded. Unreadable intent: fail open, panel shows `state lost`. | With an empty ledger the engine treats the cache as cold and would switch on one vote, breaking a warm cache. Holding the current route avoids that and a switch back to Codex's native model. Crashes skip SIGTERM handlers, so intent is written on change. Codex peer review; plan review. |
| M12 | Account binding: the proxy puts `account = sha256(ChatGPT-Account-ID)[:12]` in `Meta`, never the raw value. A thread binds to its first account; a different account resets that thread's state. A missing account changes nothing.                                                                                                                                                                                                                                                    | Homes are already isolated (M6); this covers a re-login to another account inside one home. Raw auth-adjacent headers stay upstream-only.                                                                                                                                                     |
| M13 | The stage 1 launcher stays a supported mode (no setup, no config edits). Both modes register in the same registry, and one TUI shows both.                                                                                                                                                                                                                                                                                                                                             | Users who do not want setup keep today's behavior.                                                                                                                                                                                                                                            |

## Technical Details

**Endpoint file** (`registry/<endpoint_id>.json`):

```json
{
  "endpoint_id": "8c1f…",
  "kind": "session",
  "protocol": 2,
  "pid": 4242,
  "started": "…",
  "home": {
    "id": "3fa9c0d1e2b4",
    "path": "/Users/u/.codex-work",
    "name": "work"
  },
  "socket": "/…/control.sock"
}
```

**Control v2 messages** (JSON lines):

- client → server: `{"op":"hello","protocol":2}`,
  `{"op":"subscribe","session":"<home_id>/<thread_id>"}` (empty = sessions list only),
  `{"op":"pin|unpin|pause|resume|preset","session":"…","scope":"session|home","tier":…,"preset":…,"id":"c-…"}`.
- server → client: `hello {endpoint_id, kind, home}`, `sessions {seq, items:[SessionInfo]}`,
  `snapshot {session, snapshot}`, `ack {id}`, `error {id, error}`.
- `SessionInfo`: `id, home, thread_id, label, cwd, mode, tier, route, replies, strip (last 24
tiers), last_switch, last_active, attention (manual|paused|escalation|unknown|state-lost),
footer_synced, saved_usd, spent_usd` (USD fields only when `prices` is configured). Every
  number in `SessionInfo` and `SnapshotFor` comes from that thread's own tallies; home totals in
  the TUI are sums over the home's sessions.

**TUI focus rules**: `--session` resolves once (exact id, unique id prefix, unique label or label
suffix). Ambiguous → exit 2 listing candidates; none → exit 1. Locked mode: no top bar, no
Sessions tab; on session end shows `ended` and exits after 3 s. All mode: initial focus = session
whose cwd equals or contains the panel's cwd (deepest match), else the most recently active.

**Keys** (all mode): `[`/`]` previous/next session, `0` Sessions tab, `1-5` tabs, `↑↓`+`enter`
in Sessions, `space` pause/resume focused session, `P` pause/resume focused home, `p` pin next
(focused session), `c` next preset (focused home), `q` quit.

**Stage 2 files**: `$STATE/codex-router/homes/<home_id>/{port,token,intent.json,brain.sock,logs/}`.
If the `brain.sock` path exceeds the unix socket limit (104 bytes on macOS, 108 on Linux), the
brain uses `$TMPDIR/codex-router-<uid>/<home_id>.sock` (0700 parent) and records that path in
the state dir for the edge.
Units: launchd `com.github.alexei-led.codex-router.<home_id>.{edge,brain}`; systemd
`codex-router-edge@<home_id>.{socket,service}`, `codex-router-brain@<home_id>.service`.

## What Goes Where

- **Implementation Steps** (`[ ]`): code, tests, scripts, live runs in agterm, docs.
- **Post-Completion**: Linux live run on a real Linux box, setup on the user's real homes, plugin
  publication.

## Implementation Steps

### Stage 1: registry and multi-session TUI

### Task 1: Home identity package

**parallel group A** · Depends on: none

**Files:**

- Create: `internal/home/home.go`, `internal/home/doc.go`, `internal/home/home_test.go`
- Modify: `internal/launcher/router.go` (use `home.Resolve` instead of `codexHome`)

- [x] `home.Resolve(getenv)` → `Home{ID, Path, Name}` per M2; canonical path via `filepath.EvalSymlinks` with fallback to the cleaned absolute path when the dir does not exist yet
- [x] replace `codexHome` in the launcher; `models_cache.json` path from `Home.Path`
- [x] tests: table for name derivation (`.codex`, `.codex-work`, `codex-x`, trailing slash, relative `CODEX_HOME` rejected), symlinked home gives the same ID, unset `CODEX_HOME` → `~/.codex`
- [x] run `make fmt lint test build`

### Task 2: Session label from cwd

**parallel group A** · Depends on: none

**Files:**

- Create: `internal/home/label.go`, `internal/home/label_test.go`

- [x] `RepoBranch(cwd)` reads `.git` without running git: walk up to `.git` dir or file; worktree `.git` file → `gitdir:` → `commondir` gives the main repo name; branch from `HEAD` (`ref: refs/heads/x` → `x`, detached → short sha)
- [x] `Label(homeName, cwd)` → `home · repo · branch`; non-repo cwd → `home · <dir basename>`
- [x] `Disambiguate([]labels, ids)` appends ` · <4-hex>` to colliding labels
- [x] tests with temp dirs: plain repo, linked worktree, detached HEAD, nested subdir, no repo, unreadable `.git`, collisions
- [x] run `make fmt lint test build`

### Task 3: Registry package

**parallel group A** · Depends on: none

**Files:**

- Create: `internal/registry/registry.go`, `internal/registry/doc.go`, `internal/registry/registry_test.go`

- [x] `Root(getenv)` per M3; create with 0700, refuse a non-dir, a symlink, or a dir not owned by the user (copy the rules of `checkPrivate` from `launcher/runtime.go`; Task 6 switches the launcher to the registry's exported `CheckPrivate` and deletes its copy, so this task does not touch `internal/launcher`)
- [x] `Endpoint` type (Technical Details); `Write` (temp file + rename, 0600), `Remove`, `List` (skip unparseable files, report them as skipped)
- [x] `Prune(isLive func(Endpoint) bool)` removes entries whose socket does not answer and whose pid is gone
- [x] tests: write/list round trip, atomic replace under concurrent writers, bad perms refused, unparseable entry skipped, prune keeps live and drops dead
- [x] ➕ `Alive(Endpoint)`: the default `isLive` for `Prune` (socket answers or pid exists); `Root` also checks the parent `codex-router` dir is private. The not-owned-by-user case is not unit-tested (needs a second uid)
- [x] run `make fmt lint test build`

### Task 4: Engine: per-thread state, pause, pin and snapshots

**parallel group A** · Depends on: none

**Files:**

- Modify: `internal/route/route.go`, `internal/route/control.go`, `internal/route/snapshot.go`, `internal/route/engine.go`
- Create: `internal/route/sessions.go`, `internal/route/sessions_test.go`
- Modify: `internal/route/*_test.go` affected by `Command`/`Snapshot` changes

- [x] move the engine-wide tallies into `thread`: `stats`, `recent` decisions (bounded per thread), `last` decision; drop the global `focus` as the snapshot source (today `route/engine.go:58-68`); engine-wide totals become sums over threads
- [x] `Command` gains `Session` (thread id), `Scope` (`session|home`), `ID`; pause/resume with `Scope=home` keep today's engine-wide behavior; pin requires a thread; preset is home-scoped
- [x] thread state gains `paused` and `pin`; `Decide` honors thread pause and consumes the thread pin under the engine lock (exactly once)
- [x] `SnapshotFor(threadID)` returns today's snapshot for that thread; `Sessions()` returns `[]SessionInfo` (Technical Details) for main threads seen, with `last_active` and attention flags
- [x] `Deps.Cwd func(threadID string) string` feeds `SessionInfo.cwd` (stage 1: the launcher's cwd for all threads)
- [x] tests: interleaved requests from 3 threads keep separate replies, strip, last switch and recent lists (a busy thread never pushes another thread's turns out); thread pause leaves other threads routed; home pause covers all; two pins on one thread → last wins, consumed once by the next turn; pin on thread A never used by thread B; `Sessions()` order and attention flags; existing engine tests updated
- [x] ➕ `Deps.Home`: `Sessions()` fills `id` (`<home_id>/<thread_id>`), `home` and `label` (`home.Label`, outside the engine lock) itself; `Snapshot` gains `ThreadID`
- [x] ➕ no engine-wide tally is left; home totals are sums over `Sessions()`. `Snapshot()` stays for control v1 as the most recently active thread's snapshot, until Task 5 replaces the `Handler`
- [x] ➕ an empty `Scope` means `session` when `Session` is set, else `home`. A session command on an unseen thread creates its state, so a pin or pause set before its first step holds; `Sessions()` lists only threads with a user step. Home resume clears manual everywhere and keeps session pauses. Attention covers `paused`, `manual`, `escalation`; `unknown` and `state-lost` come with Task 18
- [x] ➕ the v1 TUI's pin/unpin and `launcher/fakecodex_test.go` send the snapshot's thread, since the engine now rejects a pin without a session
- [x] run `make fmt lint test build`

### Task 5: Control protocol v2

Depends on: Task 4

**Files:**

- Modify: `internal/control/control.go`, `internal/control/server.go`, `internal/control/client.go`
- Modify: `internal/control/*_test.go`

- [x] `Handler` becomes `Apply(Command) error`, `Sessions() []SessionInfo`, `SnapshotFor(string) Snapshot`, `Hello() Hello`
- [x] server: `hello` first (reject other first lines and unknown protocol versions with an error line); push the sessions list on change (coalesced, `PublishInterval`); per-connection subscription to one session's snapshots; ack/error carry the command id
- [x] client: `Dial(path)` does the handshake; channels for `Sessions` and `Snapshot`; `Subscribe(session)`; `Send(cmd)` waits for the ack with that id
- [x] tests: handshake, version mismatch, subscription switch, two clients on one session both receive snapshots, ack matched by id with two commands in flight, slow client does not block others
- [x] ➕ wire details: `session` is `<home_id>/<thread_id>`; the server maps it to the thread id and rejects another home's. One publisher goroutine computes pushes, each client has its own writer. The sessions list is pushed only when it changed (`seq` moves). Any command refreshes every subscription (a home command changes all). The `snapshot` op stays: it re-sends the subscribed snapshot (TUI heartbeat). `Send` assigns the id. The client drops snapshots of another session or with an older `Seq`
- [x] ➕ consumers changed only to keep the build green: the launcher passes `Deps.Home` and serves the engine with its hello (endpoint id = runtime id, kind `session`; Task 6 adds the registry entry); the one-session TUI panel subscribes to the most recently active session (Task 7 replaces it); the hook subscribes to its own thread (Task 10 adds the registry lookup); `Engine.Snapshot` moved into the route tests
- [x] run `make fmt lint test build`

### Task 6: Launcher registers its endpoint

Depends on: Tasks 1, 3, 5

**Files:**

- Modify: `internal/launcher/launcher.go`, `internal/launcher/runtime.go`, `internal/launcher/router.go`
- Modify: `internal/launcher/launcher_test.go`, `internal/launcher/session_test.go`, `internal/launcher/e2e_test.go`

- [x] after the control socket is up, write `endpoint.json` (kind `session`, home, socket, pid); remove it on every exit path including fallback
- [x] pass `Deps.Cwd` = launcher cwd; drop `NewestControlSocket` (replaced by the registry in Task 7); use `registry.CheckPrivate` and delete `checkPrivate` from `runtime.go`
- [x] tests: entry exists while running and is gone after exit; startup failure leaves no entry; e2e with the fake Codex sees `hello` with the right home
- [x] ➕ a registry that cannot be written does not stop the session: it routes, logs `registry-write-failed` and prints one stderr line that the panel will not list it. `session.close` removes the entry first, so every exit path (fallback, signal during startup, codex exit) drops it
- [x] ➕ `cmd/codex-router/main.go` (outside the Files list, to keep the build green): `tui` without `--socket` locates the newest live registry endpoint instead of `NewestControlSocket`, until Task 7 replaces it with the fleet
- [x] run `make fmt lint test build`

### Task 7: TUI fleet client

Depends on: Tasks 2, 3, 5, 6

**Files:**

- Create: `internal/tui/fleet.go`, `internal/tui/fleet_test.go`
- Modify: `internal/tui/tui.go`, `internal/tui/model.go`

- [x] fleet watches the registry (poll 1 s), dials each endpoint, prunes dead entries, merges `SessionInfo` from all endpoints, groups by home, applies `Disambiguate`
- [x] focus model per Technical Details: locked (`--session`) vs all; initial focus by panel cwd, else most recent; focus survives list reorders by session id
- [x] lost endpoint → its sessions show `disconnected`, then are removed after 10 s; redial when the entry reappears
- [x] `P` and `c` (preset) send one `scope=home` command to every endpoint of the focused home (M5); no branch on endpoint kind
- [x] tests with fake endpoints: merge and grouping, focus by cwd (deepest match), locked mode ignores other sessions, endpoint loss and return, home-scoped pause and preset reach every endpoint of the focused home and none of the other home
- [x] ➕ dead entries are pruned only after a failed dial (`registry.Alive` then `Remove`), so the 1 s poll does not dial every live socket twice. A hello whose endpoint or home differs from the entry is dropped. An initial pick of the most recent session stays provisional until every connected endpoint has sent its first list, and a later cwd match replaces a most-recent pick (never a cwd pick)
- [x] ➕ `space` now pauses/resumes the focused session (it sent a home pause to one endpoint, against M5); `P` is new. `Options` is `{Socket, Registry, Session}`: `Session` locks the panel (Task 9 adds the flag), `--socket` bypasses the registry
- [x] ➕ `cmd/codex-router/main.go` (outside the Files list): `tui` passes the registry root and no longer fails when no session runs; `newestSession` and its test are gone. `docs/user-guide.md` lines on the newest-session panel and home-wide `space` stay stale until Task 11
- [x] run `make fmt lint test build`

### Task 8: TUI views: top bar, Sessions tab, locked header

Depends on: Task 7

**Files:**

- Modify: `internal/tui/render.go`, `internal/tui/model.go`, `internal/tui/tui.go`
- Create: `internal/tui/sessions.go` (rendering), golden files in `internal/tui/testdata/`
- Modify: `internal/tui/tui_test.go`

- [ ] top bar: `◂ <focused label> <others…> ▸ i/n` with home dot color and attention marks (`◐` manual, `‖` paused, `↑` escalation, `?` unknown); truncates with `…` to width
- [ ] Sessions tab `0`: grouped by home; columns SESSION, STATE, ROUTE, REPLIES strip, LAST SWITCH; home header with endpoint state and the home's totals (sum of its sessions: replies, switches, saved USD when priced); below 100 columns drop REPLIES and LAST SWITCH
- [ ] locked header: one line `● home · repo · branch  <id>`; no bar, no tab 0; `ended` state
- [ ] keys per Technical Details
- [ ] golden tests: all mode with 5 sessions in 2 homes (80 and 140 cols), locked mode, ended, disconnected, empty registry, 20 sessions (scroll)
- [ ] run `make fmt lint test build`

### Task 9: CLI: `tui --session`, `sessions`

Depends on: Task 8

**Files:**

- Modify: `cmd/codex-router/main.go`, `cmd/codex-router/main_test.go`

- [ ] `codex-router tui [--session ID|LABEL] [--socket PATH]`; resolution rules and exit codes per Technical Details
- [ ] `codex-router sessions [--json]`: one line per session (`id  home  label  mode  route`), `--json` prints `[]SessionInfo` with endpoint id; used by live tests and scripts
- [ ] tests: exact id, prefix, label, ambiguous (exit 2 with candidates), not found (exit 1), `--socket` bypasses the registry, `sessions --json` shape
- [ ] run `make fmt lint test build`

### Task 10: Hook on protocol v2

Depends on: Tasks 1, 3, 5, 9 (runs after Task 9; shares no files with it)

**Files:**

- Modify: `internal/hook/hook.go`, `internal/hook/hook_test.go`

- [ ] the hook finds its endpoint: `CODEX_ROUTER_CONTROL` when set (stage 1 launcher), else the registry entries of `home.Resolve(CODEX_HOME)` whose sessions include its thread (stage 2 plain `codex`); whole lookup within the existing 200 ms budget
- [ ] the hook dials with `hello`, subscribes to its own thread (hook stdin `session_id` is the thread id, as today) and reports that thread's switch only, from that thread's own recent decisions
- [ ] tests: two threads on one endpoint, the hook reports only its own; lookup by registry when the env var is unset; two homes, the hook picks its own; endpoint missing → silent no-op; slow registry stays within budget
- [ ] run `make fmt lint test build`

### Task 11: Stage 1 docs

Depends on: Tasks 9, 10

**Files:**

- Modify: `docs/user-guide.md`, `docs/architecture.md`, `docs/configuration.md` (if any field changes), `README.md` (one line on multiple sessions)
- Modify: `scripts/render-docs` and `docs/images/` (Sessions tab and top bar images)

- [ ] user guide: running several sessions and homes, `tui --session`, all-sessions keys, `sessions`
- [ ] architecture: registry, session identity, protocol v2, per-thread pause/pin (mermaid)
- [ ] regenerate doc images with the render pipeline
- [ ] run `make fmt lint test build`

### Task 12: [live] Stage 1 in agterm with real Codex

Depends on: Tasks 9, 10

**Files:**

- Create: `scripts/live/stage1.sh` (up/down: throwaway repos and worktrees, agterm workspace, sessions)
- Create: `docs/archive/evidence/<run-date>-multi-session/stage1/` (captures, logs, `report.md`)

- [ ] `make build`; record sha256 of both real `config.toml` files; export a temp `XDG_STATE_HOME` for every process the script starts
- [ ] `scripts/live/stage1.sh up`: temp repo with 2 worktrees + a second temp repo; agterm workspace `router-live`; 5 sessions via `agtermctl session new --command` (fish login wrapper so the classifier key is in env): 3 × `codex-router` under `~/.codex` (worktree A, worktree B, repo 2), 2 × `CODEX_HOME=~/.codex-work codex-router` (repo 2 worktrees); split in session 1 runs `codex-router tui --session <its label>`; a 6th session runs `codex-router tui`
- [ ] checks, each with an `agtermctl session text` capture or log excerpt:
  - `codex-router sessions --json` lists 5 sessions with the right homes and labels
  - all-panel: 2 home groups, `[` `]` cycle all 5, Sessions tab rows match
  - locked panel shows only its session and no bar
  - a prompt in session 1 updates only session 1's row and footer
  - `space` on session 2 pauses only it; `P` on the work home pauses both work sessions only; resume
  - pin from the locked panel and the all-panel on one session: next turn pinned once
  - `/model` in session 3 → `◐ manual` on that row only
  - `kill -9` one router → its row `disconnected`, then gone; others keep routing; registry entry pruned
  - `/new` in session 4 → a new row; the old row leaves after the thread goes idle
- [ ] each bug: fix + regression test + entry in `report.md`; re-run the failed check
- [ ] `scripts/live/stage1.sh down`; assert real `config.toml` sha256 unchanged
- [ ] run `make fmt lint test build`

### Gate

### Task 13: Spike scripts for stage 2

Depends on: Task 12

**Files:**

- Create: `scripts/spikes/stage2-homes.sh` (test homes check/reset), `scripts/spikes/stage2/daemon_test.go` (build tag `live`), `scripts/spikes/activation/` (its own `go.mod`, so purego stays out of the main module until Task 16; minimal edge, plist, units)

- [ ] test homes `~/.cache/codex-router-live/home-a` and `home-b` (0700). The script checks each has a valid login (`CODEX_HOME=<home> codex login status`). **If not logged in: stop the workflow and ask the user to run `! CODEX_HOME=~/.cache/codex-router-live/home-a codex login` (and `home-b`, ideally with the work account) once.** Never copy `auth.json`
- [ ] `reset`: rewrite only `config.toml` in a test home (minimal, router provider pointing at a spike proxy `codex-router proxy`); trap cleanup stops the test home's daemon (`CODEX_HOME=<home> codex app-server daemon stop`) after the path guard from the safety rules
- [ ] daemon client spike (Go, tag `live`) using `internal/appserver` against `<home>/app-server-control/app-server-control.sock`
- [ ] activation spike: launchd plist with `Sockets`, a tiny Go binary that gets the fd through purego `launch_activate_socket`
- [ ] no access to real homes beyond the sha256 checks
- [ ] run `make fmt lint test build` (the `live` tag and the separate module keep the spikes out of normal lint and test)

### Task 14: [live] Gate run and decision

Depends on: Task 13

**Files:**

- Create: `docs/archive/evidence/<run-date>-multi-session/gate/report.md`
- Modify: this plan (Gate outcome section)

- [ ] G1: two plain `codex` TUIs in agterm with `CODEX_HOME=<home-a>`; spike client sees both threads in `thread/loaded/list` with their cwd; `thread/resume` returns current settings; `thread/resume` + `thread/settings/update` repaints both footers
- [ ] G2: provider block added to `<home-a>/config.toml` while its daemon runs; a new thread uses the router without a daemon restart (if not: setup must restart the daemon; record and continue only if a restart is safe for other sessions)
- [ ] G3: a static `http_headers` token in the provider reaches the spike proxy
- [ ] G4: macOS activation: launchd holds the port; `kill -9` the spike edge mid-request; the next connection waits in the backlog and is served by the restarted edge; measure the stall. Linux part: unit test with `LISTEN_FDS` (Task 16) stands in
- [ ] G5: a local plugin marketplace in `<home-a>` with a SessionStart hook: hook env has `CODEX_HOME` (or it can be derived), a detached child survives the hook; a hook command can reach the registry (Task 10 lookup)
- [ ] write the gate report and the Gate outcome section. **If G1, G3, G4 or G5 fails: stop the workflow here.** G4 failing with purego but passing with a cgo bridge: switch M8 to cgo + macOS release runner (add ➕ task to the release workflow) and continue
- [ ] cleanup (test homes stay, logged in, for Task 23); real `config.toml` sha256 unchanged

#### Gate outcome

_(filled by Task 14)_

### Stage 2: per-home edge and brain

### Task 15: Edge-brain link package

Depends on: Task 14 (gate passed)

**Files:**

- Create: `internal/link/link.go`, `internal/link/client.go`, `internal/link/server.go`, `internal/link/doc.go`, `internal/link/link_test.go`

- [ ] frame format per M7 (4-byte length + JSON header + body); messages `hello{home_id}`, `decide`, `rewrite|none`, `observe`
- [ ] client implements `proxy.Decider` and `proxy.Observer`: deadline per call, reconnect with backoff, breaker; every failure returns "no rewrite"; `observe` is fire-and-forget with a bounded queue (drop when full)
- [ ] server wraps a `route.Engine`; checks `home_id` in `hello`; socket 0600 in a 0700 dir
- [ ] the decide header carries `Meta`, which holds only the account hash once Task 19 lands (no raw headers in frames)
- [ ] tests: round trip, brain down → pass-through within the deadline, brain slow → pass-through, brain restart → client reconnects, wrong home rejected, 32 MiB body, observe queue full does not block
- [ ] run `make fmt lint test build`

### Task 16: Socket activation and `codex-router edge`

Depends on: Task 15

**Files:**

- Create: `internal/listen/listen.go`, `internal/listen/systemd.go`, `internal/listen/launchd_darwin.go`, `internal/listen/launchd_other.go`, `internal/listen/listen_test.go`
- Modify: `cmd/codex-router/main.go`, `cmd/codex-router/main_test.go`, `internal/proxy/proxy.go` and its tests (accept an external listener), `internal/launcher/router.go` (shared proxy wiring if reused)
- Modify: `go.mod`, `go.sum` (`github.com/ebitengine/purego`, unless the gate switched to cgo)

- [ ] `listen.Activated(name)`: systemd `LISTEN_PID`/`LISTEN_FDS`/`LISTEN_FDNAMES` (pure Go, clears the env after use); launchd via `launch_activate_socket`; neither → `ErrNotActivated`
- [ ] `codex-router edge --home PATH --state-dir DIR`: listener from activation, else self-bind on the stored port with a warning; token from state; proxy with the link client; `/healthz` reports `brain: up|down`
- [ ] tests: systemd path in a child process with fd 3 and `LISTEN_FDS=1` (runs on Linux and macOS); wrong `LISTEN_PID` ignored; edge with brain down forwards unchanged (fake upstream); token required
- [ ] run `make fmt lint test build`

### Task 17: Brain: daemon client and thread tracking

Depends on: Task 16

**Files:**

- Create: `internal/brain/brain.go`, `internal/brain/threads.go`, `internal/brain/doc.go`, `internal/brain/brain_test.go`
- Modify: `internal/appserver/appserver.go`, `internal/appserver/transport.go` and their tests (daemon socket path, `thread/loaded/list`, `thread/read`, `thread/unsubscribe`, `thread/started` handling)
- Modify: `internal/launcher/writer.go` (export the settings writer for reuse, or move it to `internal/brain` and import it back)
- Modify: `cmd/codex-router/main.go`, `cmd/codex-router/main_test.go` (`brain --home PATH --state-dir DIR`)

- [ ] connect to the home's daemon socket with `experimentalApi`; on start `thread/loaded/list` and resume each main thread (skip ephemeral and subagent threads); on `thread/started` resume the new thread; cwd per thread feeds `Deps.Cwd`
- [ ] reuse the settings writer for footer sync; `thread/unsubscribe` when a thread's TUI is gone (no turn and no settings event for 10 min, or the thread reports closed)
- [ ] daemon restart: reconnect with backoff, resubscribe, resync footers
- [ ] serve the link socket and control v2 (kind `brain`), register `endpoint.json`
- [ ] tests with a fake daemon: two threads resumed, new thread picked up, unsubscribe after idle, daemon restart resync, ephemeral thread skipped
- [ ] run `make fmt lint test build`

### Task 18: Brain intent persistence and restart semantics

Depends on: Task 17

**Files:**

- Create: `internal/brain/intent.go`, `internal/brain/intent_test.go`
- Modify: `internal/route/engine.go`, `internal/route/control.go`, `internal/route/sessions.go` and their tests (seeded-route hold; export/import of intent)

- [ ] M11: intent file written atomically on every intent change; loaded at start; unreadable → fail open + `state lost` finding
- [ ] after a brain start each resumed thread is seeded with the route from `thread/resume` settings and held there (attention `unknown`) until its first completed reply; then normal routing
- [ ] tests: pause/manual/pin/preset survive a restart; learned state does not; seeded thread does not switch before its first measured reply; corrupt file → `state lost` and pass-through; concurrent writes keep the last intent
- [ ] run `make fmt lint test build`

### Task 19: Account binding

Depends on: Task 18

**Files:**

- Modify: `internal/proxy/proxy.go` (hash `ChatGPT-Account-ID` into `Meta.Account`), `internal/responses/` (the file that defines `Meta`), `internal/route/engine.go`
- Modify: matching tests

- [ ] M12 in the proxy (both edge and stage 1 launcher) and the engine
- [ ] tests: thread keeps state with a stable account; a different account resets; a missing account changes nothing; raw header value never appears in Meta, logs or link frames
- [ ] run `make fmt lint test build`

### Task 20: `codex-router setup` and service units

Depends on: Task 19

**Files:**

- Create: `internal/setup/setup.go`, `internal/setup/config_toml.go`, `internal/setup/setup_test.go`, `internal/setup/testdata/`
- Create: `internal/service/service.go`, `internal/service/launchd.go`, `internal/service/systemd.go`, `internal/service/service_test.go`, `internal/service/testdata/`
- Modify: `cmd/codex-router/main.go`, `cmd/codex-router/main_test.go` (`setup`, `service ensure|status`), `go.mod`, `go.sum` (`github.com/BurntSushi/toml`)

- [ ] `setup --home PATH [--dry-run] [--undo] [--follow-symlink]`: print the resolved home first; M9/M10 edits with diff output, backup, validation; pick and store a free port; write token; install and load units for this home only, passing `--home` and the resolved `--state-dir` to both units (M3)
- [ ] `--undo`: remove marked blocks (result equals the backup minus our blocks), unload and delete units, keep state dir unless `--purge`
- [ ] `service ensure`: for `$CODEX_HOME`, start units if set up and down, else print one hint line; returns at once; `service status`
- [ ] `internal/service` runs `launchctl`/`systemctl --user` through an injected runner (mocked in tests); units rendered from templates (golden files)
- [ ] tests: config with comments and tables keeps them; existing `model_provider` refused; symlink refused; dry-run writes nothing; undo round trip byte-identical; two homes get distinct ports, tokens and unit names; unit golden files for both OSes
- [ ] run `make fmt lint test build`

### Task 21: Codex plugin packaging

Depends on: Task 20

**Files:**

- Create: `.agents/plugins/marketplace.json`, `plugins/codex-router/.codex-plugin/plugin.json`, `plugins/codex-router/hooks/` (SessionStart → `codex-router service ensure`, stdio redirected, `async: true`; existing status hook), `plugins/codex-router/skills/router/SKILL.md`
- Modify: `cmd/codex-router/main.go`, `cmd/codex-router/main_test.go` (`ctl status|pause|resume|pin --cwd DIR` for the skill)
- Create: `cmd/codex-router/plugin_test.go` (manifest validation)

- [ ] manifest and marketplace in the formats from Context; the plugin calls `codex-router` from PATH and ships no binary
- [ ] skill: status, pause/resume and pin for the session whose cwd matches, through `codex-router ctl`
- [ ] tests: manifests parse and reference existing files; `ctl` resolves the session by cwd; hook command returns within 1 s when services are down
- [ ] run `make fmt lint test build`

### Task 22: TUI with brain endpoints

Depends on: Task 21 (sequential; needs brain from 17 and intent from 18)

**Files:**

- Modify: `internal/tui/render.go`, `internal/tui/sessions.go`, `internal/tui/fleet.go`, `internal/tui/tui_test.go`, golden files

- [ ] home header shows brain state (`brain up 2h`, `brain down: default model`); `state lost` and `unknown` attention marks
- [ ] mixed fleet: stage 1 sessions and a brain in the same home render in one group
- [ ] golden tests: brain up, brain down, mixed, `state lost`
- [ ] run `make fmt lint test build`

### Task 23: [live] Stage 2 in agterm with real Codex

Depends on: Tasks 20, 21, 22

**Files:**

- Create: `scripts/live/stage2.sh` (temp homes, setup, sessions, cleanup)
- Create: `docs/archive/evidence/<run-date>-multi-session/stage2/` (captures, logs, `report.md`)

- [ ] record real `config.toml` sha256s; temp `XDG_STATE_HOME`; `scripts/live/stage2.sh up`: the two logged-in test homes from Task 13 (reset `config.toml` only), `codex-router setup --home` for each, plugin installed into each from the local marketplace; agterm workspace `router-live-2`: 3 plain `codex` sessions with `CODEX_HOME=<home-a>` (worktrees), 2 with `<home-b>`; one locked panel, one all-panel
- [ ] checks with captures:
  - all 5 sessions appear under 2 homes; footers sync in every TUI
  - `kill -9` brain A mid-turn: no `Reconnecting`, turn completes on the default model; brain A back → routing resumes; a paused session stays paused; home B unaffected throughout
  - `kill -9` edge A mid-turn: port stays bound (`lsof`), turn continues; record the stall
  - brain upgrade simulation (`launchctl kickstart -k`): no turn interrupted
  - temp daemon A restart: brain resyncs footers, sessions keep their labels
  - home isolation: B's threads never appear under A; link frames carry only hashed accounts (log check)
  - `setup --undo` for both homes: configs byte-identical to backups, units gone
- [ ] each bug: fix + regression test + `report.md` entry
- [ ] `scripts/live/stage2.sh down`; real `config.toml` sha256 unchanged; no `codex-router` units left for real homes
- [ ] run `make fmt lint test build`

### Task 24: Verify acceptance criteria

Depends on: Task 23 (or Task 12 when the gate stopped stage 2)

- [ ] every Overview item delivered for the stages that ran; gate outcome recorded
- [ ] edge cases covered by tests: ambiguous labels, 20 sessions, endpoint loss, account change, corrupt intent, symlinked config
- [ ] state plainly in the summary which Linux paths are covered only by unit tests (systemd units, `LISTEN_FDS`, setup on Linux) and which were run live (none in this plan; see Post-Completion)
- [ ] full suite: `make fmt lint test build`; `make vuln`
- [ ] coverage not below the current level (`make coverage`)
- [ ] release cross-build still works: `make release-build`

### Task 25: [Final] Update documentation

Depends on: Task 24

- [ ] README: one picture and three lines for multi-session and setup
- [ ] user guide: setup per home, plugin install, undo, service status, troubleshooting for brain down / edge down / state lost
- [ ] configuration reference: any new fields; architecture: stage 2 diagrams (edge, brain, daemon, units), failure table rows, security (token, account hash, socket activation)
- [ ] evidence reports linked from the architecture doc's further reading
- [ ] move this plan to `docs/archive/plans/`

## Post-Completion

_Items requiring manual intervention or external systems_

**Manual verification:**

- Linux live run on a real Linux box with systemd user units: setup, edge `kill -9` with the
  socket held, brain restart. CI covers the Linux code paths only through unit tests.
- Setup on the user's real homes (`~/.codex`, `~/.codex-work`) when the user decides to switch:
  review the dry-run diff first.

**External system updates:**

- Publish the marketplace by pushing the repo; check `codex plugin marketplace add
alexei-led/codex-router` from a clean machine.
- VS Code extension and the desktop app read the same `config.toml`: check their behavior with
  the router provider (unverified).
