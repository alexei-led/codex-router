# Configuration

Every field of the config file, with its default. To get started, and to find where the file lives, see [Configure](user-guide.md#configure) in the user guide. Words such as tier, route, activity and preset are defined in [Concepts](user-guide.md#concepts).

How the file is read:

- A missing file means the defaults below. Keys you omit keep their default.
- Objects such as `policy`, `context` and `cache` merge field by field.
- A map entry you name (a tier, an activity, a price) replaces the default entry whole.
- A `classifiers` entry changes only the fields it names, over the entry of the same name. Set a field to `""` to clear it.
- Unknown keys, trailing data and invalid values are errors. The router then runs plain `codex` with one line that says why.
- `$XDG_CONFIG_HOME` is used only when it is an absolute path.
- Edits apply to the next session. In a home set up with `codex-router setup`, they apply when its brain and edge restart (see [Check and restart the services](user-guide.md#check-and-restart-the-services)). The panel changes only the preset, and only for the running sessions of one Codex home. A preset picked in the panel for a set-up home outlives a brain restart and wins over `preset` here.

## Example

Cheaper low tier, a stronger plan route, a local classifier:

```json
{
  "baseline": "medium",
  "routes": {
    "low": { "model": "gpt-6-luna", "effort": "medium" }
  },
  "activities": {
    "plan": { "medium": { "effort": "high" } },
    "docs": { "medium": { "effort": "low" } }
  },
  "preset": "thrifty",
  "classifier": "ollama",
  "policy": { "minDwellRequests": 6 }
}
```

Because objects merge, `"policy": {"minDwellRequests": 6}` changes one field. Because map entries replace, `routes.low` above replaces the whole default `low` route.

## Fields

| Field         | Type                              | Default                                                      | Meaning                                                                                         |
| ------------- | --------------------------------- | ------------------------------------------------------------ | ----------------------------------------------------------------------------------------------- |
| `baseline`    | tier                              | `medium`                                                     | Tier of the route used when no thread model is known, and the target of the upward context fix. |
| `routes`      | map tier to route                 | see below                                                    | Model and effort per tier. All four tiers must exist.                                           |
| `activities`  | map activity to map tier to route | `debug` at `medium`                                          | Per-activity override of a tier's route. Optional.                                              |
| `prices`      | map model to price                | `gpt-6.1-sol`, `gpt-6-luna`                                  | USD list prices for the per-reply estimate. Optional.                                                |
| `policy`      | object                            | see below                                                    | Switching thresholds.                                                                           |
| `preset`      | `thrifty`, `balanced`, `quality`  | `balanced`                                                   | Overrides some policy fields. See [presets](#presets).                                          |
| `classifier`  | string                            | `jev`                                                        | Key of the active entry in `classifiers`, or `off`.                                             |
| `classifiers` | map name to classifier            | `jev`, `clef`, `clef-flash`, `clef-omni`, `openai`, `ollama` | Classifier endpoints.                                                                           |
| `context`     | object                            | `recentTurns 6`, `maxTextChars 1200`                         | What the classifier sees.                                                                       |
| `cache`       | object                            | `ttlMs 300000`, `warmMarginMs 30000`                         | Prompt-cache warmth rule.                                                                       |
| `proxy`       | object                            | `port 0`                                                     | Proxy listener.                                                                                 |
| `codexBin`    | string                            | `codex` on `PATH`                                            | The Codex executable. Optional.                                                                 |

Tiers, weakest first: `micro`, `low`, `medium`, `high`. Activities: `code`, `debug`, `explore`, `plan`, `review`, `ops`, `docs`. Efforts: `low`, `medium`, `high`, `xhigh`.

### routes and activities

A route is `{"model": "<slug>", "effort": "<effort>"}`. An empty `effort` keeps the effort Codex sent. A route's `model` is required in `routes`.

| Tier     | Default model | Default effort |
| -------- | ------------- | -------------- |
| `high`   | `gpt-6.1-sol` | `high`         |
| `medium` | `gpt-6.1-sol` | `medium`       |
| `low`    | `gpt-6-luna`  | `high`         |
| `micro`  | `gpt-6-luna`  | `medium`       |

The defaults use two models in one compatibility group. The router checks that a target model shares the group of the model Codex sent. A known unequal group is blocked and the request keeps its original route. If the current or target model is missing from the catalog, compatibility is unknown: the request still keeps its original route, but Policy reports the unknown model separately instead of calling it incompatible. An empty catalog is reported as unavailable. When the catalog becomes available, the next snapshot reflects its current groups.

Effort changes on one model keep its prompt cache. A switch between models can require an uncached prefix. See [evaluation](evaluation.md) for the small tests behind these defaults.

An `activities.<activity>.<tier>` entry overrides the tier's model and effort. An empty field inherits the tier's route. The default sends `debug` at `medium` to `gpt-6.1-sol` with `high` effort. An activity route applies when the classifier's activity probability reaches `policy.activityMass`.

### prices

`{"input": n, "cachedInput": n, "output": n}`, USD per million tokens, none negative. They drive the list-price estimates in the Sessions and Usage views, and they order models for the "same task, stronger or cheaper" check (by output price). The defaults are OpenAI list prices: `gpt-6.1-sol` 2 / 0.10 / 10 (input up to 272K tokens) and `gpt-6-luna` 0.10 / 0.01 / 0.50. An entry you name replaces that model's default price; the other default stays. For a complete estimate, price every routed model and every model Codex uses as the native model. A reply is covered only when both its routed and native model have prices. The panel shows dollar totals only when every eligible reply is covered; otherwise it says `estimate partial` or `estimate unknown` with the `N of M replies priced` coverage and hides the partial subtotal. A `/model` change can make the native model differ over the session, so totals use a per-reply native basis, not one model for the whole session. Plan billing is usage limits, so a complete figure is still an estimate, not a bill.

### policy

Counts are integers. Probability thresholds and context fill are in (0, 1]. Slopes are zero or positive.

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
| `minDwellRequests`      | 4       | 8                        | 16      |
| `escalationHoldTurns`   | 1       | 2                        | 4       |

The preset values express a preference. They have not been tuned against a representative workload.

Old configs can still contain `policy.downgradeHorizonTurns`. It is ignored. It is absent from the defaults and presets, and you can remove it from your file.

### classifiers

Each entry:

| Field       | Type   | Meaning                                                                                                 |
| ----------- | ------ | ------------------------------------------------------------------------------------------------------- |
| `label`     | string | Name shown in the panel.                                                                                |
| `api`       | string | Wire adapter: `system-one`, `openai-decisions` or `ollama`.                                             |
| `endpoint`  | string | URL the request is posted to. May hold `{NAME}` placeholders. Required.                                 |
| `model`     | string | Model name sent to the endpoint. Required.                                                              |
| `keyEnv`    | string | Name of the env var holding the bearer key. Empty means no key. The key itself is never in the config.  |
| `envFile`   | string | Dotenv file read for the `keyEnv` key and each placeholder when the env var is unset. Absolute or `~/`. |
| `timeoutMs` | int    | Total deadline for one ask, with up to two attempts inside it (min 1).                                  |

Defaults:

| Name         | `api`              | Endpoint                                                                                                 | Model        | `keyEnv`               | Timeout |
| ------------ | ------------------ | -------------------------------------------------------------------------------------------------------- | ------------ | ---------------------- | ------- |
| `jev`        | `system-one`       | `https://api.typesafe.ai/v1/systemone`                                                                   | `jev-1.13.0` | `TYPESAFE_API_KEY`     | 1500 ms |
| `clef`       | `system-one`       | `https://api.cloudflare.com/client/v4/accounts/{CLOUDFLARE_ACCOUNT_ID}/ai/run/@cf/cloudflare/clef`       | `clef`       | `CLOUDFLARE_API_TOKEN` | 3000 ms |
| `clef-flash` | `system-one`       | `https://api.cloudflare.com/client/v4/accounts/{CLOUDFLARE_ACCOUNT_ID}/ai/run/@cf/cloudflare/clef-flash` | `clef-flash` | `CLOUDFLARE_API_TOKEN` | 3000 ms |
| `clef-omni`  | `system-one`       | `https://api.cloudflare.com/client/v4/accounts/{CLOUDFLARE_ACCOUNT_ID}/ai/run/@cf/cloudflare/clef-omni`  | `clef-omni`  | `CLOUDFLARE_API_TOKEN` | 3000 ms |
| `openai`     | `openai-decisions` | `https://api.openai.com/v1/decisions`                                                                    | `gpt-6-luna` | `OPENAI_API_KEY`       | 3000 ms |
| `ollama`     | `ollama`           | `http://127.0.0.1:11434/api/chat`                                                                        | `qwen3.5:9b` | none                   | 5000 ms |

To use another service that speaks one of the three protocols, add an entry with every field and set `classifier` to its name. When `keyEnv` names a variable that is unset, the classifier is off for the session (logged), the router still runs, and pins work.

An `endpoint` may hold `{NAME}` placeholders, where `NAME` is upper case letters, digits and `_`, starting with a letter. Each is filled from the env var `NAME`, else from `envFile`, and is URL path-escaped. The Clef entries use `{CLOUDFLARE_ACCOUNT_ID}`, your Cloudflare account ID. A placeholder with no value turns the classifier off, logged as `<NAME> is not set`. A malformed placeholder is a config error.

`"classifier": "off"` disables classifier requests. Only pins and the context guard change the model. Codex still sends its normal coding requests to OpenAI.

The brain (`codex-router setup`) runs under launchd or systemd and does not see your shell's env. Point `envFile` at a file that holds the key, and the account ID for Clef. The other fields keep their defaults:

```json
{ "classifiers": { "jev": { "envFile": "~/.config/codex-router/.env" } } }
```

The file holds the line `TYPESAFE_API_KEY=...`. One file can hold several keys and the Cloudflare account ID (`CLOUDFLARE_API_TOKEN=...`, `CLOUDFLARE_ACCOUNT_ID=...`, `OPENAI_API_KEY=...`); each classifier reads only its own. The wrapper can read the same file, so the key need not be in your shell. Rules:

- The env var wins over the file. The file is read only when the variable is unset.
- Lines are `KEY=value` or `export KEY=value`. Quotes (`'…'`, `"…"`, no escapes) are allowed. A line that starts with `#` is a comment, and so is ` #…` after an unquoted value. Only the `keyEnv` key and the endpoint placeholders are read, the last assignment wins, and nothing from the file goes into the process env.
- The file must be private: any group or other permission (`0640`, `0604`, even `0620`) is refused with a log line naming it (`chmod 600` it). A missing file, a missing key or an empty value turns the classifier off, logged as above.
- The key is never logged or printed.

### Classifier data and privacy

Only the selected classifier is called. Its payload contains:

- The current prompt, clipped to `context.maxTextChars` characters.
- Up to `context.recentTurns` user or assistant messages, each clipped separately. These are messages, not user/assistant pairs.
- Fixed questions and labels for complexity, activity and continuation. The System One adapter also includes your configured tier model names and efforts.

It does not receive Codex HTTP headers or ChatGPT credentials. It uses its own `keyEnv` credential. Code or secrets pasted into a user or assistant message can be included in the clipped text. Clipping is a size limit, not secret removal.

With `jev`, text goes to `api.typesafe.ai`. Clef uses `api.cloudflare.com`, and the OpenAI classifier uses `api.openai.com`. The default Ollama endpoint is local. If you change its endpoint to another host, text goes there. `off` disables classifier traffic. In every case, Codex's coding requests still go to the OpenAI ChatGPT backend.

Provider retention and training policies are outside the router's control. Check the selected provider's terms before sending private work.

Failure rules: one routing ask gets up to two attempts inside `timeoutMs`. Ollama can also send a separate activity request within that same deadline. It gets one attempt. Three consecutive failures pause classification for 60 seconds. A missing key or cancelled call does not count as a failure. One ask runs at a time.

When a decision fails or times out, that request is forwarded unchanged. The router's decision deadline is the classifier timeout plus 200 ms. Explicit pins and the context guard can still affect other requests while classification is off.

### context

| Field          | Default | Meaning                                                              |
| -------------- | ------- | -------------------------------------------------------------------- |
| `recentTurns`  | 6       | User or assistant text messages sent to the classifier (min 1).                       |
| `maxTextChars` | 1200    | Each text is clipped to this many characters before sending (min 1). |

Set both low to send less text to a hosted classifier.

### cache

Codex's prompt cache is per model and ignores effort. A model counts as warm from its last response until `ttlMs - warmMarginMs` has passed. A switch to a cold model writes the whole prefix uncached; that is the tax the policy prices. Need `ttlMs > 0` and `0 <= warmMarginMs < ttlMs`.

### proxy

`port`: 0 binds any free port once per session and keeps it for the session. A fixed port stops a second session from starting. The listener is always `127.0.0.1`. `codex-router proxy --port N` overrides it. A set-up home ignores it: `codex-router setup` picks the home's port once and stores it in the home's state dir.

## model_context_window

Advice only. The router never edits this key: `codex-router setup` changes `config.toml` only inside its own marked blocks.

If you set `model_context_window` in `~/.codex/config.toml`, Codex sizes its context from that number whatever model the router picks. After a switch to a model with a smaller window, Codex would then send a thread that is too large for it. The fit guard rejects routes that are too small. Remove `model_context_window` to let Codex use the routed model's catalog entry. If you keep it, set it no larger than the smallest window among your routes.

## Environment

| Variable                                                                                                    | Used for                                                                                                                                                                                                                                                                                             |
| ----------------------------------------------------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `CODEX_ROUTER_CONFIG`                                                                                       | Config path.                                                                                                                                                                                                                                                                                         |
| the classifier's `keyEnv` (`TYPESAFE_API_KEY`) and endpoint `{NAME}` placeholders (`CLOUDFLARE_ACCOUNT_ID`) | Classifier bearer key and endpoint values.                                                                                                                                                                                                                                                           |
| `CODEX_HOME`                                                                                                | Where `models_cache.json` is read from, read-only (default `~/.codex`). Also the session's home in the panel, and the default `--home` of `setup`, `service` and `ctl`.                                                                                                                              |
| `XDG_CONFIG_HOME`, `XDG_STATE_HOME`, `XDG_RUNTIME_DIR`, `TMPDIR`                                            | Config, logs, session registry and runtime dir locations; `XDG_CONFIG_HOME` also places the systemd user units, and `TMPDIR` holds a brain's sockets when the state dir path is too long. Services do not see them, so `codex-router setup` writes the config path and the state dir into the units. |
| `CODEX_ROUTER_CONTROL`                                                                                      | Set by the launcher for Codex and the hook: the session's control socket.                                                                                                                                                                                                                            |
| `CODEX_ROUTER_BIN`                                                                                          | The binary the Codex plugin runs, before the one `codex-router setup` recorded in `<home>/codex-router.env` and before `PATH`.                                                                                                                                                                       |
| `CODEX_THREAD_ID`                                                                                           | Set by Codex for its shell commands. `codex-router ctl` acts on that thread's session.                                                                                                                                                                                                               |
| `LISTEN_PID`, `LISTEN_FDS`, `LISTEN_FDNAMES`                                                                | Set by systemd for the edge: the provider port's socket.                                                                                                                                                                                                                                             |
