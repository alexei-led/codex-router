# User guide

Install codex-router, configure it, read what it shows, and fix it when something goes wrong. For how it works inside, see [architecture](architecture.md). For every config field, see the [configuration reference](configuration.md).

## Requirements

- macOS or Linux. Windows is not supported.
- The Codex CLI on `PATH`, signed in with ChatGPT. API-key sign-in is not supported, because the router forwards to the ChatGPT backend only. It was tested with Codex 0.162.1. Other versions may work, but the footer sync relies on experimental Codex surfaces (see [Codex versions](#codex-versions)).
- A classifier: a key for the hosted default (Jev), or a local [Ollama](https://ollama.com) model. Without either, the router still runs, but only pins change the model.

## Install

Prebuilt binaries for macOS and Linux (amd64, arm64) are on the [releases page](https://github.com/alexei-led/codex-router/releases), with a `checksums.txt`.

```sh
os=$(uname -s | tr '[:upper:]' '[:lower:]')                   # darwin or linux
arch=$(uname -m | sed 's/x86_64/amd64/;s/aarch64/arm64/')     # amd64 or arm64
curl -fsSL -o codex-router \
  "https://github.com/alexei-led/codex-router/releases/latest/download/codex-router-$os-$arch"
sudo install -m 0755 codex-router /usr/local/bin/codex-router
codex-router version
```

With Go 1.27.2 or newer:

```sh
go install github.com/alexei-led/codex-router/cmd/codex-router@latest
```

From a checkout, `make build` writes `.bin/codex-router`.

## First run

```sh
export TYPESAFE_API_KEY=...   # classifier key; see "Choose a classifier"
cd your-repo
codex-router                  # instead of `codex`
```

Codex looks and behaves as usual. The footer now shows the model that the router picked for the turn. To watch the decisions, run the panel in a second terminal or split:

```sh
codex-router tui
```

Run as many sessions as you like, in other repos, worktrees or Codex homes. One panel shows them all (see [Several sessions and homes](#several-sessions-and-homes)).

If any startup step fails, the router prints one line, `codex-router: <reason>; running plain codex`, and starts plain `codex` with your arguments.

### Pass arguments to Codex

```sh
codex-router [--config PATH] [--] [codex args...]
```

Everything after the router's own flags goes to Codex unchanged. Use `--` when a Codex flag could be taken for a router flag:

```sh
codex-router resume --last
codex-router --config ./router.json -- --model gpt-6-sol
```

In this mode, Codex refuses `-s` and `-a` together with `resume` ("Permission overrides are not supported when resuming a remote task"). Drop those flags when you resume.

### What starts

`codex-router` does these steps in order:

1. Reads the config. A missing file means the defaults.
2. Creates a private runtime directory (mode `0700`) for its sockets.
3. Starts the proxy on `127.0.0.1` and checks that it answers.
4. Starts a private `codex app-server` for this session, with `-c` overrides only. Your `~/.codex/config.toml` is not touched.
5. Runs the Codex TUI against that app-server in your terminal. It returns Codex's exit code when Codex quits.

The reasons for this design are in [architecture](architecture.md#why-a-private-app-server).

## Concepts

The router uses these words in the footer, the panel, the config and the docs.

| Term                    | Meaning                                                                                                                                        |
| ----------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------- |
| **Session**             | One Codex conversation (a main thread) routed by codex-router. `/new` in Codex starts a new session.                                           |
| **Home**                | A Codex home (`CODEX_HOME`, default `~/.codex`) with its own login. `~/.codex` is named `default`, `~/.codex-work` is named `work`.            |
| **Turn**                | One prompt from you. Codex can send several requests for it, called **steps**.                                                                 |
| **Tier**                | The complexity level that the router gives a turn: `micro`, `low`, `medium` or `high`, weakest first.                                          |
| **Route**               | A model and a reasoning effort. Each tier maps to one route (`routes` in the config).                                                          |
| **Classifier**          | A small model that reads your prompt and recent dialogue and returns probabilities for each tier and activity.                                 |
| **Activity**            | The kind of work: `code`, `debug`, `explore`, `plan`, `review`, `ops` or `docs`. An activity can give a tier a different route (`activities`). |
| **Policy**              | The rules that decide when to switch tier. A switch needs enough repeated classifier answers (votes), and costly switches need more support.   |
| **Compatibility group** | Models that Codex can switch between in one thread. The router never routes outside the group of the model that Codex sent.                    |
| **Pin**                 | A tier that you choose in the panel for a session's next turn. The classifier is not asked for that turn.                                      |
| **Pause**               | Routing off for one session, or for every session of a home. Requests go out unchanged.                                                        |
| **Manual hold**         | What a `/model` pick in Codex causes. The router keeps your model for that thread until you resume.                                            |
| **Preset**              | A named set of policy thresholds: `thrifty`, `balanced` or `quality`.                                                                          |

## Configure

### Where the config lives

The config is one JSON file. The router uses the first of these:

1. `--config PATH`
2. `$CODEX_ROUTER_CONFIG`
3. `$XDG_CONFIG_HOME/codex-router/config.json`
4. `~/.config/codex-router/config.json`

A missing file means the defaults. Keys that you leave out keep their defaults. An invalid file is not ignored: the router prints why and runs plain `codex`. Changes apply to the next session. In a running session, only the preset can change, from the panel.

### Choose a classifier

The classifier scores each new turn. Only the active one is called. It gets the clipped prompt and recent dialogue, never your Codex credentials.

| Classifier      | Where text goes       | Setup                                                                   |
| --------------- | --------------------- | ----------------------------------------------------------------------- |
| `jev` (default) | `api.typesafe.ai`     | `export TYPESAFE_API_KEY=...`                                           |
| `ollama`        | stays on your machine | `ollama pull qwen3.5:9b`, then `{"classifier": "ollama"}` in the config |

When the key variable is unset, the classifier is off for the session. The panel shows `Jev off`, requests keep their model, and pins still work. To send less text to a hosted classifier, lower [`context`](configuration.md#context).

### Common changes

```json
{
  "routes": { "low": { "model": "gpt-6-luna", "effort": "low" } },
  "preset": "thrifty",
  "prices": {
    "gpt-6-luna": { "input": 0.5, "cachedInput": 0.05, "output": 3 },
    "gpt-6-sol": { "input": 2.5, "cachedInput": 0.25, "output": 15 }
  }
}
```

- **`routes`**: the model and effort for each tier. Keep all models in one compatibility group. The defaults are in one group.
- **`preset`**: `thrifty` moves up reluctantly and down readily. `quality` does the reverse. `balanced` (the default) uses `policy` as written.
- **`prices`**: list prices in USD per million tokens. They are needed only for the savings estimate in the panel. The values above are placeholders.
- **`activities`**: a different route for an activity at a tier. The default sends `debug` at `medium` to high effort.

The [configuration reference](configuration.md) has every field and its default.

### `model_context_window`

If `~/.codex/config.toml` sets `model_context_window`, Codex sizes its context from that value whatever model the router picks. Remove it, or keep it no larger than the smallest window among your routes. See [configuration](configuration.md#model_context_window).

## Use

### Footer

![Codex footer showing the routed model, GPT-6-Sol medium](images/codex-footer.svg)

The footer shows the routed model and effort, for example `GPT-6-Sol medium`. It repaints when the route changes, also while a turn runs. The window and context figures follow the routed model.

### Several sessions and homes

Each `codex-router` run is one session. Start one per repo or worktree, and pick the Codex home with `CODEX_HOME` as you would for plain `codex`:

```sh
cd ~/src/api && codex-router
cd ~/src/api-worktrees/feat-auth && CODEX_HOME=~/.codex-work codex-router
```

- **Home name.** `~/.codex` is `default`, `~/.codex-<x>` is `<x>`, any other home is its directory name.
- **Session label.** `home · repo · branch`, read from the `.git` of the directory Codex started in. A linked worktree shows its main repo's name. A detached HEAD shows the short commit. Outside a repo, the label is `home · <directory>`. When two sessions get the same label, each gets four hex characters derived from its id appended, as in `default · notes · 9f3c`.
- **Session id.** `<home id>/<thread id>`. The home id is derived from the home's path. The thread id is the one Codex shows.
- **New and idle sessions.** `/new` in Codex starts a new session, which joins the list at its first prompt. A session with no prompt or reply for 10 minutes leaves the list, unless it is its router's most recent one.

Each router lists itself in the session registry, `$XDG_STATE_HOME/codex-router/registry` (else `~/.local/state/codex-router/registry`), and removes its entry when it exits. Panels in any shell find sessions there. Use the same `XDG_STATE_HOME` (or none) in every shell.

To list the running sessions:

```sh
codex-router sessions [--json]
```

It prints one line per session: id, home, label, mode and route, in the panel's order. With no session it prints nothing. `--json` prints the full list, each entry with its router's `endpoint_id`, for scripts.

### Panel

```sh
codex-router tui [--session ID|LABEL] [--socket PATH]
```

Without flags, the panel shows every running session of every home. The top bar lists them, the focused session first, then the rest in list order, with the focus position on the right (`▸ 1/5`). Each session has its home's colored dot, and a mark when it needs a look: `◐` manual hold, `‖` paused, `↑` escalation, `?` unknown (a restarted brain holds the session's route until its next reply), `!` state lost (the brain could not read its saved pause, pin and preset, so the home is paused until `P` resumes it), `?` also for any other flag. The panel first focuses the session whose directory is the panel's directory or the closest one above it, else the most recently active session. Tabs 1-5 show the focused session.

**Sessions** (`0`) lists every session, grouped by home:

![Router panel, Sessions tab with the top bar](images/router-tui-sessions.svg)

- **Home line**: the number of routers (endpoints) serving the home, the home's brain when it has one (`brain up 2h`, or `brain down: default model` while Codex's requests pass through unchanged), how many other routers are lost, and the totals of its sessions: replies, switches, and the saving when `prices` is set. A brain that goes down stays on the line until it is back; its sessions leave after 10 s.
- **Rows**: the label without the home, the state (`on`, `paused`, `manual`, `escalation`, `unknown`, `state lost`, `disconnected`), the route, the last 24 routed replies colored by tier with the reply count, and the last switch. Below 100 columns the panel drops the replies and last switch columns.
- `↑` `↓` move the selection, and `enter` focuses that session and opens Now. A long list scrolls.

`--session` locks the panel to one session: no top bar and no Sessions tab, only a header line `● home · repo · branch  <id>`. When that session's router exits, the panel shows `ended` and closes 3 s later. A session served by a brain never ends this way: a restarted brain lists it again at its next prompt, so the panel shows `disconnected` meanwhile. The value can be:

1. the session id or the bare thread id, exact
2. a unique prefix of either
3. the exact label
4. the end of a label that starts at a part boundary, for example `api · feat/auth` or just `feat/auth`, but not `auth`

The first rule with any match decides. A label shared by several sessions matches all of them. When several sessions match, `tui` lists them and exits with 2. When none matches, it exits with 1. Use `codex-router sessions` to see the ids and labels.

`--socket PATH` shows only the router at that control socket and skips the registry.

When a router goes away, its sessions show `disconnected`, and leave the list 10 s later. If the router comes back, the panel dials it again. With no session at all, the header reads `no sessions` and the panel waits. Quitting the panel does not affect any session.

| Key                | Action                                                                  |
| ------------------ | ----------------------------------------------------------------------- |
| `[`, `]`           | previous, next session (all sessions only)                              |
| `0`                | Sessions tab (all sessions only)                                        |
| `1`–`5`            | Now, Routes, Policy, Classifier, Usage                                  |
| `tab`, `shift+tab` | next, previous tab                                                      |
| `↑`, `↓`, `enter`  | on the Sessions tab: select a session, focus it                         |
| `p`                | pin the focused session's next turn: none → high → medium → low → micro |
| `space`            | pause the focused session, or resume it                                 |
| `P`                | pause every session of the focused session's home, or resume them       |
| `c` (Policy tab)   | next preset for the focused session's home: thrifty, balanced, quality  |
| `q`, `ctrl+c`      | quit the panel                                                          |

The header shows the focused session's mode (`on`, `manual`, `paused`), the classifier state, and whether the footer is synced.

**Now** shows the current decision and the session so far:

![Router panel, Now tab](images/router-tui-now.svg)

- **Next turn**, or **This turn** while a turn runs: the tier, the model and effort, and the model that Codex sent (`base`) when it was rewritten.
- **Activity**: the classifier's activity and probability, the two likeliest alternatives, and the probability at which an activity route applies.
- **Why**: the policy's reason, and the support it weighed against its bar.
- **Pin next**: one button per tier with the classifier's support. `▶` marks the tier in use.
- **Session**: routed replies as a strip colored by tier, counts by tier and activity, the last switch, and the estimated saving against the baseline route. The saving needs `prices`.
- **Worth a look**: things to act on, each naming the tab where you change it. Examples: the classifier is paused or failing, a manual hold, the context guard kept a larger window, or cache reuse is low after switches.
- **Codex**, **Context**, **Cache**: what the footer shows, how full the smallest window is, and prompt-cache reuse.

**Routes** lists each tier's route, the compatibility group, models blocked because they are outside it, and the last six decisions.

![Routes tab](images/router-tui-routes.svg)

**Policy** shows the presets and the thresholds in effect. **Classifier** shows its state, latency, failures, and the pause time while it is paused (`off` with no key). **Usage** shows requests, switches, cache reuse, and the list-price estimate for this session. Plan billing counts usage, not dollars, so the dollar figure is only an estimate.

### Pin, pause, preset

- **Pin** (`p`): the focused session's next turn runs on that tier for all its steps, and the classifier is not asked. The turn uses up the pin. Other sessions are not affected.
- **Pause** (`space`): every request of the focused session goes out on Codex's own model, unchanged, and its footer goes back to it.
- **Resume** (`space` again): routing of that session continues. Resume also clears its manual hold.
- **Pause home** (`P`): pauses every running session of the focused session's home, and no other home. When all of them are paused, `P` resumes the home: that clears the manual holds there, but keeps the sessions you paused with `space` paused. Resume those with `space`.
- **Preset** (`c` on the Policy tab): applies at once to every running session of the focused session's home, until they exit. The config file is not changed.

### Pick a model yourself

A `/model` pick in Codex starts a manual hold for that session. The panel shows `◐ manual`, and requests keep your model until you focus the session and press `space` in the panel. If you do not want Codex to save the model to its config, use the session-only choice in Codex's picker.

### What the router leaves alone

These requests go out unchanged, with no classifier call:

- compaction (`/compact` or automatic)
- subagent steps
- Codex's own side threads, such as the one that writes the thread title after your first prompt
- requests whose turn metadata is missing or unreadable, or whose model is not in Codex's model catalog

### Context guard

A route is used only if the thread fits in `policy.contextFill` (default 80%) of that model's context window. When the chosen tier does not fit, the router takes the lowest tier that does. When none fits, the request keeps Codex's model. The router never trims history. Not verified live: the guard has not been tested on a thread above about 220k tokens.

### Escalation on repeated failures

When the same command fails twice with an edit in between, the turn moves up one tier without asking the classifier. It stays there for `policy.escalationHoldTurns` turns. Only a pair whose second failure is in the current turn counts, and not once a later check passed.

Models in code mode (`tool_mode: code_mode_only`) run commands inside JavaScript cells, and Codex reports only how the script ended. The router reads a command's exit status from the cell output in three cases:

- the script printed it on its own line (`exit_code=1` or `exit code: 1`, as models usually do)
- the script printed the nested result as JSON (`text(JSON.stringify(r))`), read for its `exit_code`
- the script failed

A completed cell that prints no status counts neither as a failure nor as a passed check. The passed check is what `policy.minDwellRequests` needs before a move down on the same task.

### Router line in the transcript (optional hook)

`codex-router hook` can print one line in the Codex transcript when the route changes, for example:

```text
router: high · debug → gpt-6.1-sol · high · escalate: same tool failure twice, edit in between
```

The footer already shows the model. The hook adds the reason, for its own session only. Under `codex-router`, it reaches the router through `CODEX_ROUTER_CONTROL`, which the router sets. Otherwise it looks in the registry for a router of its Codex home that runs its thread. It answers within 200 ms and exits 0 even when no router is running.

Codex runs a hook only after you trust it, and it reads trust only from your own `~/.codex/config.toml`. So the router cannot register the hook for you. To add it yourself:

```toml
[[hooks.UserPromptSubmit]]
[[hooks.UserPromptSubmit.hooks]]
type = "command"
command = "/usr/local/bin/codex-router hook"

[[hooks.Stop]]
[[hooks.Stop.hooks]]
type = "command"
command = "/usr/local/bin/codex-router hook"
```

Then approve the hooks in Codex's hook review. Keep the command string stable, because changing it makes Codex mark the hook `modified`. Not verified live: the hook has not been run against a real Codex, so the exact approval steps are unconfirmed.

### Proxy only, for `codex exec`

```sh
codex-router [--config PATH] proxy [--port N]
```

This runs only the proxy and the routing engine: no footer sync, no panel, no pins. It prints the `-c` flags to pass to `codex exec` in another shell. Logs go to stderr. Stop it with Ctrl-C.

This proxy checks no session token, so any local user can reach it. Use it for debugging, not on a shared machine.

## Troubleshoot

| Symptom                                                              | Cause and fix                                                                                                                                                                      |
| -------------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `codex-router: <reason>; running plain codex`                        | The config is invalid, or a startup step failed. The reason names it. Fix the config, or check `codex --version` and the session log.                                              |
| Panel says `Jev off`; the model never changes                        | The classifier key variable is unset in the shell that started `codex-router`. Export it and start a new session.                                                                  |
| Panel says `paused until …`                                          | The classifier failed 3 times in a row and pauses for 60 s. Turns go unrouted meanwhile. Check the network, the key, or that Ollama is running.                                    |
| `footer not synced`                                                  | Settings writes to the app-server are pending or failed. Routing still works. If it persists, your Codex version may not support the sync (see [Codex versions](#codex-versions)). |
| Panel says `no sessions`; `codex-router sessions` prints nothing     | No session is registered for your user under this state dir. Start a session, or use the same `XDG_STATE_HOME` (or none) in the panel's shell as in the session's shell.           |
| `codex-router: the panel will not list this session: …`              | The router could not write its registry entry, usually because the registry dir is not a private directory of yours (mode `0700`). The session routes as usual.                    |
| `codex-router: tui: "x" matches N sessions:`                         | `--session` named several sessions. Pass one of the listed ids, or a longer label.                                                                                                 |
| Panel shows `disconnected`                                           | The session's router ended or restarted. Its rows leave after 10 s. A restarted router shows up again by itself. Routing of other sessions is unaffected.                          |
| Locked panel shows `ended` and closes                                | The session's router exited. Start the panel again with the new session's id or label.                                                                                             |
| Second session fails to start                                        | `proxy.port` is fixed, so the port is taken. Use `0` (the default) for any free port.                                                                                              |
| Model stays put after a `/model` pick                                | That is the manual hold. Press `space` in the panel to resume routing.                                                                                                             |
| `Permission overrides are not supported when resuming a remote task` | Codex refuses `-s` and `-a` with `resume` in remote mode. Drop them.                                                                                                               |
| Codex shows `Reconnecting… waiting for network`                      | The router process died, and the proxy with it. See [If the router is killed](#if-the-router-is-killed).                                                                           |

How the router fails open (classifier down, upstream error, cut stream) is in [architecture](architecture.md#failure-handling). In short: the request goes out unchanged, and Codex retries on its own. The router never re-sends a request.

### If the router is killed

On a normal exit, everything stops together. After `kill -9` or an out-of-memory kill, the Codex TUI and the private app-server keep running with no proxy behind them. The orphaned app-server keeps sending your ChatGPT token to the dead proxy's port. Any local user who binds that port receives the token.

1. Quit Codex.
2. Kill the leftover `codex app-server` promptly.
3. Start `codex-router` again.

To let a running turn finish first, run `codex-router proxy --port <old port>` to serve it.

### Signals

Ctrl-C and Ctrl-\ reach Codex through the terminal. The router ignores them. SIGTERM and SIGHUP are passed to Codex.

### Logs

The router writes one JSON-lines file per session: `$XDG_STATE_HOME/codex-router/logs/<id>.log`, else `~/.local/state/codex-router/logs/<id>.log` (directory `0700`, file `0600`). Logs hold thread and turn ids, decisions, statuses, sizes and timings. They never hold request or response text or credentials. Nothing is printed to the terminal while Codex owns it.

### Codex versions

The footer sync needs `codex --remote` and the app-server's `thread/settings/update`, both experimental. A Codex release can change them. When that happens, routing keeps working and the panel shows `footer not synced`. The router was tested with Codex 0.162.1.

## Exit codes

| Command                     | Code                                                                                     |
| --------------------------- | ---------------------------------------------------------------------------------------- |
| `codex-router [codex args]` | Codex's exit code, 128+N for signal N, 1 when neither routed nor plain Codex could start |
| `tui`                       | 0 on quit, 1 on error or when no session matches `--session`, 2 when several match       |
| `sessions`                  | 0, also when no session runs. 1 on error                                                 |
| `hook`                      | 0, also when the router is not running. 1 only if writing stdout fails                   |
| `proxy`                     | 0 after SIGINT or SIGTERM, 1 on error                                                    |
| any                         | 2 on a usage error                                                                       |
