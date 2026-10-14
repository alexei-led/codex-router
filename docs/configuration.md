# Configuration

Every field of the config file, with its default. To get started, and to find where the file lives, see [Configure](user-guide.md#configure) in the user guide. Words such as tier, route, activity and preset are defined in [Concepts](user-guide.md#concepts).

How the file is read:

- A missing file means the defaults below. Keys you omit keep their default.
- Objects such as `policy`, `context` and `cache` merge field by field.
- A map entry you name (a tier, an activity, a classifier, a price) replaces the default entry whole.
- Unknown keys, trailing data and invalid values are errors. The router then runs plain `codex` with one line that says why.
- `$XDG_CONFIG_HOME` is used only when it is an absolute path.
- Edits apply to the next session. In a home set up with `codex-router setup`, they apply when its brain and edge restart (see [Check and restart the services](user-guide.md#check-and-restart-the-services)). The panel changes only the preset, and only for the running sessions of one Codex home. A preset picked in the panel for a set-up home outlives a brain restart and wins over `preset` here.

## Example

Cheaper low tier, a price table for the Usage tab, a local classifier:

```json
{
  "baseline": "medium",
  "routes": {
    "low": { "model": "gpt-6-luna", "effort": "low" }
  },
  "activities": {
    "debug": { "medium": { "model": "gpt-6-sol", "effort": "high" } },
    "docs": { "medium": { "effort": "low" } }
  },
  "prices": {
    "gpt-6-luna": { "input": 0.5, "cachedInput": 0.05, "output": 3 },
    "gpt-6-sol": { "input": 2.5, "cachedInput": 0.25, "output": 15 },
    "gpt-6.1-sol": { "input": 5, "cachedInput": 0.5, "output": 30 }
  },
  "preset": "thrifty",
  "classifier": "ollama",
  "policy": { "minDwellRequests": 6 }
}
```

The prices are placeholders: use the list prices you want estimates against. Because objects merge, `"policy": {"minDwellRequests": 6}` changes one field. Because map entries replace, `routes.low` above replaces the whole default `low` route.

## Fields

| Field         | Type                              | Default                              | Meaning                                                                                         |
| ------------- | --------------------------------- | ------------------------------------ | ----------------------------------------------------------------------------------------------- |
| `baseline`    | tier                              | `medium`                             | Tier of the route used when no thread model is known, and the target of the upward context fix. |
| `routes`      | map tier to route                 | see below                            | Model and effort per tier. All four tiers must exist.                                           |
| `activities`  | map activity to map tier to route | `debug` at `medium`                  | Per-activity override of a tier's route. Optional.                                              |
| `prices`      | map model to price                | none                                 | List prices in USD per million tokens. Optional.                                                |
| `policy`      | object                            | see below                            | Switching thresholds.                                                                           |
| `preset`      | `thrifty`, `balanced`, `quality`  | `balanced`                           | Overrides some policy fields. See [presets](#presets).                                          |
| `classifier`  | string                            | `jev`                                | Key of the active entry in `classifiers`.                                                       |
| `classifiers` | map name to classifier            | `jev`, `ollama`                      | Classifier endpoints.                                                                           |
| `context`     | object                            | `recentTurns 6`, `maxTextChars 1200` | What the classifier sees.                                                                       |
| `cache`       | object                            | `ttlMs 300000`, `warmMarginMs 30000` | Prompt-cache warmth rule.                                                                       |
| `proxy`       | object                            | `port 0`                             | Proxy listener.                                                                                 |
| `codexBin`    | string                            | `codex` on `PATH`                    | The Codex executable. Optional.                                                                 |

Tiers, weakest first: `micro`, `low`, `medium`, `high`. Activities: `code`, `debug`, `explore`, `plan`, `review`, `ops`, `docs`. Efforts: `low`, `medium`, `high`, `xhigh`.

### routes and activities

A route is `{"model": "<slug>", "effort": "<effort>"}`. An empty `effort` keeps the effort Codex sent. A route's `model` is required in `routes`.

| Tier     | Default model | Default effort |
| -------- | ------------- | -------------- |
| `high`   | `gpt-6.1-sol` | `high`         |
| `medium` | `gpt-6-sol`   | `medium`       |
| `low`    | `gpt-6-luna`  | `medium`       |
| `micro`  | `gpt-6-luna`  | `low`          |

The four defaults share one model compatibility group, so switching between them mid-thread works **[verified]**. A model in a different group fails mid-thread, so the router refuses a route outside the group of the model Codex sent and keeps the request. The Routes tab lists such models as blocked.

An `activities.<activity>.<tier>` entry replaces the tier's model and effort field by field; an empty field inherits the tier's route. The default sends `debug` at `medium` to `gpt-6-sol` with `high` effort. An activity route applies when the classifier's activity probability reaches `policy.activityMass`.

### prices

`{"input": n, "cachedInput": n, "output": n}`, USD per million tokens, none negative. They drive the list-price estimate on the Now and Usage tabs, and they order models for the "same task, stronger or cheaper" check (by output price). With no price for a model, estimates stay in tokens and the panel says there is no baseline. Plan billing is usage limits, so the figures are estimates, not a bill.

### policy

Counts are integers, masses and fills are in (0, 1], slopes are not negative.

| Field                   | Default | Meaning                                                                                                                                                       |
| ----------------------- | ------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `upgradeVotes`          | 2       | Consecutive classifier answers above the running tier needed to move up (min 1). One is enough before the first measured reply.                               |
| `upgradeBase`           | 0.75    | Probability mass above the running tier needed to move up, before the cost term.                                                                              |
| `upgradeSlope`          | 0.15    | Adds `slope * tax / (tax + pivot)` to the bar, where tax is the uncached tokens the switch would write.                                                       |
| `upgradePivotTokens`    | 100000  | Tax at which the cost term is half the slope (min 1).                                                                                                         |
| `jumpConfidence`        | 0.95    | A jump of two or more tiers up needs only this mass above the running tier, with no vote streak.                                                              |
| `downgradeVotes`        | 2       | Consecutive answers at or below the target tier needed to move down (min 1).                                                                                  |
| `downgradeMass`         | 0.9     | Mass at or below the target tier needed to move down, before the cost term. Also the bar for a cheaper activity route on the same task.                       |
| `downgradeSlope`        | 0.08    | Cost term slope for moving down.                                                                                                                              |
| `downgradePivotTokens`  | 100000  | Pivot for moving down (min 1).                                                                                                                                |
| `downgradeHorizonTurns` | 5       | Validated (min 1) but not read by the engine yet; preset values have no effect.                                                                               |
| `continuationMass`      | 0.7     | When the classifier scores a new prompt this likely to continue the running task, it counts as the same task.                                                 |
| `escalationHoldTurns`   | 2       | After a repeated-tool-failure escalation, turns the tier is held (min 0).                                                                                     |
| `reviewEveryRequests`   | 4       | Background classifier review every N requests of a thread. 0 turns reviews off.                                                                               |
| `minDwellRequests`      | 8       | Requests since the last model change before a cheaper route on the same task is allowed. It also needs a passed test, build or lint check since then (min 0). |
| `activityMass`          | 0.6     | Activity probability needed to apply an activity route on a new task.                                                                                         |
| `contextFill`           | 0.8     | A route is used only if the thread's estimated size is at most this fraction of its window.                                                                   |

`escalationHoldTurns` and `minDwellRequests` depend on how the router reads tool results. That is described in [Escalation on repeated failures](user-guide.md#escalation-on-repeated-failures).

### presets

`balanced` keeps `policy` as configured. The others replace these fields, whatever `policy` says:

| Field                   | thrifty | balanced (as configured) | quality |
| ----------------------- | ------- | ------------------------ | ------- |
| `upgradeVotes`          | 3       | 2                        | 1       |
| `upgradeBase`           | 0.85    | 0.75                     | 0.6     |
| `jumpConfidence`        | 0.98    | 0.95                     | 0.9     |
| `downgradeVotes`        | 1       | 2                        | 3       |
| `downgradeMass`         | 0.8     | 0.9                      | 0.95    |
| `downgradeHorizonTurns` | 8       | 5                        | 3       |
| `minDwellRequests`      | 4       | 8                        | 16      |
| `escalationHoldTurns`   | 1       | 2                        | 4       |

The preset values are chosen for direction, not tuned on data.

### classifiers

Each entry:

| Field       | Type   | Meaning                                                                                                |
| ----------- | ------ | ------------------------------------------------------------------------------------------------------ |
| `label`     | string | Name shown in the panel.                                                                               |
| `api`       | string | Wire adapter: `system-one` or `ollama`.                                                                |
| `endpoint`  | string | URL the request is posted to. Required.                                                                |
| `model`     | string | Model name sent to the endpoint. Required.                                                             |
| `keyEnv`    | string | Name of the env var holding the bearer key. Empty means no key. The key itself is never in the config. |
| `envFile`   | string | Dotenv file read for the `keyEnv` key when the env var is unset. Absolute or `~/` path. Default empty. |
| `timeoutMs` | int    | Total deadline for one ask, with up to two attempts inside it (min 1).                                 |

Defaults:

| Name     | `api`        | Endpoint                               | Model        | `keyEnv`           | Timeout |
| -------- | ------------ | -------------------------------------- | ------------ | ------------------ | ------- |
| `jev`    | `system-one` | `https://api.typesafe.ai/v1/systemone` | `jev-1.13.0` | `TYPESAFE_API_KEY` | 1500 ms |
| `ollama` | `ollama`     | `http://127.0.0.1:11434/api/chat`      | `qwen3.5:9b` | none               | 5000 ms |

To use another service that speaks one of the two protocols, add an entry and set `classifier` to its name. When `keyEnv` names a variable that is unset, the classifier is off for the session (logged), the router still runs, and pins work.

The brain (`codex-router setup`) runs under launchd or systemd and does not see your shell's env. Point `envFile` at a file that holds the key, for example `"envFile": "~/.config/codex-router/.env"` with the line `TYPESAFE_API_KEY=...`. Rules:

- The env var wins over the file. The file is read only when the variable is unset.
- Lines are `KEY=value` or `export KEY=value`; quotes (`'…'`, `"…"`, no escapes) and `#` comments are allowed. Only the `keyEnv` key is read, and nothing from the file goes into the process env.
- The file must be private: one that group or others can read is refused with a log line naming it (`chmod 600` it). A missing file or key turns the classifier off, logged as above.
- The key is never logged or printed.

Only the active classifier is called. It receives the clipped prompt and recent dialogue (`context`), never headers or your Codex credentials, and only the `keyEnv` key as its bearer token. With `jev` that text goes to `api.typesafe.ai`. With `ollama` it stays on your machine.

Failure rules for every adapter: two attempts per ask inside `timeoutMs`; three failures in a row pause the classifier for 60 s, during which turns are forwarded unrouted. One ask runs at a time; another waits for it. A missing key and a cancelled call do not count as failures. The router also gives up a decision shortly after `timeoutMs` (plus 200 ms) and forwards the request.

### context

| Field          | Default | Meaning                                                              |
| -------------- | ------- | -------------------------------------------------------------------- |
| `recentTurns`  | 6       | Dialogue turns sent to the classifier (min 1).                       |
| `maxTextChars` | 1200    | Each text is clipped to this many characters before sending (min 1). |

Set both low to send less text to a hosted classifier.

### cache

Codex's prompt cache is per model and ignores effort. A model counts as warm from its last response until `ttlMs - warmMarginMs` has passed. A switch to a cold model writes the whole prefix uncached; that is the tax the policy prices. Need `ttlMs > 0` and `0 <= warmMarginMs < ttlMs`.

### proxy

`port`: 0 binds any free port once per session and keeps it for the session. A fixed port stops a second session from starting. The listener is always `127.0.0.1`. `codex-router proxy --port N` overrides it. A set-up home ignores it: `codex-router setup` picks the home's port once and stores it in the home's state dir.

## model_context_window

Advice only. The router never edits this key: `codex-router setup` changes `config.toml` only inside its own marked blocks.

If you set `model_context_window` in `~/.codex/config.toml`, Codex sizes its context from that number whatever model the router picks. After a switch to a model with a smaller window, Codex would then send a thread that is too large for it. The router's fit guard avoids routes that do not fit, but the cleanest setup is to remove `model_context_window` and let Codex take the window from the routed model's catalog entry. If you keep it, set it no larger than the smallest window among your routes.

## Environment

| Variable                                                         | Used for                                                                                                                                                                |
| ---------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `CODEX_ROUTER_CONFIG`                                            | Config path.                                                                                                                                                            |
| the classifier's `keyEnv` (`TYPESAFE_API_KEY`)                   | Classifier bearer key.                                                                                                                                                  |
| `CODEX_HOME`                                                     | Where `models_cache.json` is read from, read-only (default `~/.codex`). Also the session's home in the panel, and the default `--home` of `setup`, `service` and `ctl`. |
| `XDG_CONFIG_HOME`, `XDG_STATE_HOME`, `XDG_RUNTIME_DIR`, `TMPDIR` | Config, logs, session registry and runtime dir locations. Services do not see them, so `codex-router setup` writes the config path and the state dir into the units.    |
| `CODEX_ROUTER_CONTROL`                                           | Set by the launcher for Codex and the hook: the session's control socket.                                                                                               |
| `CODEX_ROUTER_BIN`                                               | The binary the Codex plugin runs, before the one `codex-router setup` recorded in `<home>/codex-router.env` and before `PATH`.                                          |
| `CODEX_THREAD_ID`                                                | Set by Codex for its shell commands. `codex-router ctl` acts on that thread's session.                                                                                  |
| `LISTEN_PID`, `LISTEN_FDS`, `LISTEN_FDNAMES`                     | Set by systemd for the edge: the provider port's socket.                                                                                                                |
