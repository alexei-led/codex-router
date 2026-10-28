# Configuration

Every field of the config file, with its default. To get started, and to find where the file lives, see [Configure](user-guide.md#configure) in the user guide. Words such as tier, route, activity and mode are defined in [Concepts](user-guide.md#concepts).

How the file is read:

- A missing file means the defaults below. Keys you omit keep their default.
- Objects such as `policy`, `context` and `cache` merge field by field.
- A map entry you name (a tier, an activity, a price) replaces the default entry whole.
- A `classifiers` entry changes only the fields it names, over the entry of the same name. Set a field to `""` to clear it.
- Unknown keys, trailing data and invalid values are errors. The router then runs plain `codex` with one line that says why.
- `$XDG_CONFIG_HOME` is used only when it is an absolute path.
- Edits apply to the next session. In a home set up with `codex-router setup`, they apply when its brain and edge restart (see [Check and restart the services](user-guide.md#check-and-restart-the-services)). The panel and `codex-router ctl mode` change only one session's mode; they never write this file.

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
  "mode": "cost",
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
| `activities`  | map activity to map tier to route | none (default-owned preferences below)                       | Per-activity override of a tier's route. Optional.                                              |
| `prices`      | map model to price                | `gpt-6.1-sol`, `gpt-6.1-terra`, `gpt-6-luna`                 | USD list prices for the per-reply estimate. Optional.                                           |
| `policy`      | object                            | see below                                                    | Switching thresholds.                                                                           |
| `mode`        | `cost`, `balanced`, `quality`     | not set (`balanced`)                                         | The profile's routing mode. See [mode and modes](#mode-and-modes).                              |
| `modes`       | map mode to `{"policy": {...}}`   | `cost`, `quality`                                            | Policy fields each mode replaces. See [mode and modes](#mode-and-modes).                        |
| `classifier`  | string                            | `jev`                                                        | Key of the active entry in `classifiers`, or `off`.                                             |
| `classifiers` | map name to classifier            | `jev`, `clef`, `clef-flash`, `clef-omni`, `openai`, `ollama` | Classifier endpoints.                                                                           |
| `context`     | object                            | `recentTurns 6`, `maxTextChars 1200`                         | What the classifier sees.                                                                       |
| `cache`       | object                            | `ttlMs 300000`, `warmMarginMs 30000`                         | Prompt-cache warmth rule.                                                                       |
| `proxy`       | object                            | `port 0`, `upstream` unset                                   | Proxy listener and backend.                                                                     |
| `codexBin`    | string                            | `codex` on `PATH`                                            | The Codex executable. Optional.                                                                 |

Tiers, weakest first: `micro`, `low`, `medium`, `high`. Activities: `code`, `debug`, `explore`, `plan`, `review`, `ops`, `docs`. Efforts: `low`, `medium`, `high`, `xhigh`.

### routes and activities

A route is `{"model": "<slug>", "effort": "<effort>"}`. An empty `effort` keeps the effort Codex sent. A route's `model` is required in `routes`.

| Tier     | Default model   | Default effort |
| -------- | --------------- | -------------- |
| `high`   | `gpt-6.1-sol`   | `high`         |
| `medium` | `gpt-6.1-terra` | `medium`       |
| `low`    | `gpt-6-luna`    | `high`         |
| `micro`  | `gpt-6-luna`    | `medium`       |

These are the raw generic declarations; default-owned mode and activity preferences are listed below. The defaults use three models, subject to the runtime compatibility group. The router checks that a target model shares the group of the model Codex sent. A known unequal group is blocked and the request keeps its original route. If the current or target model or its compatibility hash is missing from the catalog, compatibility is unknown: the request still keeps its original route, but Policy reports the unknown model separately instead of calling it incompatible. An empty catalog is reported as unavailable. When the catalog becomes available, the next snapshot reflects its current groups.

Effort changes on one model keep its prompt cache. A switch between models can require an uncached prefix. See [evaluation](evaluation.md) for the small tests behind these defaults.

An `activities.<activity>.<tier>` entry overrides the tier's model and effort. An empty field inherits the tier's route. Explicit cells are user-owned and use the generic mode transforms for that cell, not the built-in preference matrix. Ownership is independent per cell: an explicit low cell does not change automatic preferences at undeclared medium or high cells. An explicit empty target cell also opts that cell out of built-in preferences. Set `activities` to `null` to disable built-in activity preferences while keeping generic mode preferences. An activity route applies when the classifier's activity probability reaches `policy.activityMass`.

### prices

`{"input": n, "cachedInput": n, "output": n}`, USD per million tokens, none negative. They drive the list-price estimates in the Sessions and Usage views, and they order models for the "same task, stronger or cheaper" check (by output price). The defaults are standard OpenAI list prices, input / cached input / output: `gpt-6.1-sol` 2 / 0.10 / 10, `gpt-6.1-terra` 1 / 0.05 / 5 and `gpt-6-luna` 0.10 / 0.01 / 0.50. These flat estimates exclude long-context and Fast multipliers and API cache-write charges. They are not subscription invoices; Codex credit billing has no separate cache-write charge. An entry you name replaces that model's default price; the other default stays. For a complete estimate, price every routed model and every model Codex uses as the native model. A reply is covered only when both its routed and native model have complete, positive prices for all three components; zero or missing components are unpriced. The panel shows dollar totals only when every eligible reply is covered; otherwise it says `estimate partial` or `estimate unknown` with the `N of M replies priced` coverage and hides the partial subtotal. A `/model` change can make the native model differ over the session, so totals use a per-reply native basis, not one model for the whole session. Plan billing is usage limits, so a complete figure is still an estimate, not a bill.

### policy

Counts are integers. Probability thresholds and context fill are in (0, 1]. Slopes are zero or positive.

| Field                  | Default | Meaning                                                                                                                                                       |
| ---------------------- | ------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `upgradeVotes`         | 2       | Consecutive classifier answers above the running tier needed to move up (min 1). One is enough before the first measured reply.                               |
| `upgradeBase`          | 0.75    | Probability mass above the running tier needed to move up, before the cost term.                                                                              |
| `upgradeSlope`         | 0.15    | Adds `slope * tax / (tax + pivot)` to the bar, where tax is the uncached tokens the switch would write.                                                       |
| `upgradePivotTokens`   | 100000  | Tax at which the cost term is half the slope (min 1).                                                                                                         |
| `jumpConfidence`       | 0.95    | A jump of two or more tiers up needs only this mass above the running tier, with no vote streak.                                                              |
| `downgradeVotes`       | 2       | Consecutive answers at or below the target tier needed to move down (min 1).                                                                                  |
| `downgradeMass`        | 0.9     | Mass at or below the target tier needed to move down, before the cost term. Also the bar for a cheaper activity route on the same task.                       |
| `downgradeSlope`       | 0.08    | Cost term slope for moving down.                                                                                                                              |
| `downgradePivotTokens` | 100000  | Pivot for moving down (min 1).                                                                                                                                |
| `continuationMass`     | 0.7     | When the classifier scores a new prompt this likely to continue the running task, it counts as the same task.                                                 |
| `escalationHoldTurns`  | 2       | After a repeated-tool-failure escalation, turns the tier is held (min 0).                                                                                     |
| `reviewEveryRequests`  | 4       | Background classifier review every N requests of a thread. 0 turns reviews off.                                                                               |
| `minDwellRequests`     | 8       | Requests since the last model change before a cheaper route on the same task is allowed. It also needs a passed test, build or lint check since then (min 0). |
| `activityMass`         | 0.6     | Activity probability needed to apply an activity route on a new task.                                                                                         |
| `contextFill`          | 0.8     | A route is used only if the thread's estimated size is at most this fraction of its window.                                                                   |

`escalationHoldTurns` and `minDwellRequests` depend on how the router reads tool results. That is described in [Escalation on repeated failures](user-guide.md#escalation-on-repeated-failures).

### mode and modes

A routing mode trades cost against answer quality. It never changes what the classifier is asked or what it answers.

With the exact default generic ladder, modes use explicit preferred pairs. Changing any generic model or effort makes the ladder custom: it keeps the deterministic transforms described after the tables. An explicit activity cell also uses those transforms for that cell. No OpenAI model or effort is injected into a custom declaration.

Generic default preferences:

| Tier     | cost         | balanced     | quality      |
| -------- | ------------ | ------------ | ------------ |
| `micro`  | Luna medium  | Luna medium  | Luna medium  |
| `low`    | Luna medium  | Luna high    | Terra medium |
| `medium` | Luna high    | Terra medium | Sol medium   |
| `high`   | Terra medium | Sol high     | Sol xhigh    |

Activity preferences on the default ladder:

| Activity           | balanced medium | cost high    | quality medium | balanced high |
| ------------------ | --------------- | ------------ | -------------- | ------------- |
| code               | Terra xhigh     | Terra xhigh  | Sol high       | Sol high      |
| debug              | Terra xhigh     | Terra xhigh  | Sol high       | Sol xhigh     |
| review             | Terra medium    | Terra medium | Sol high       | Sol xhigh     |
| plan               | Terra medium    | Terra medium | Sol high       | Sol xhigh     |
| explore, ops, docs | Terra medium    | Terra medium | Sol medium     | Sol high      |

All quality-high cells use Sol xhigh. Cost-medium always uses Luna high. Low follows the generic table for every activity; micro remains Luna medium in every mode. Unknown activity uses the generic cell. These preferences are a derived routing policy, not an entitlement or quality guarantee; see [Evaluation](evaluation.md).

A preferred pair is checked against the current Codex catalog's model, effort, compatibility and usable context window before classification. If it is ineligible, an eligible generic-transform pair can be used, else the eligible balanced pair for the same tier and activity. Only when neither fits does the existing context/group guard search other tiers or keep the original request. Diagnostics distinguish a preferred pair from that fallback. API context maxima are not Codex windows.

For custom declarations, `balanced` keeps the configured pair; `cost` takes the next lower tier's cheaper model (never below low), else one effort step down; `quality` takes one effort step up, else the next higher tier, never an ordered-weaker pair. Micro, pins, manual selections and paused sessions bypass mode transforms. Pins use the raw generic declaration, not the activity preferences.

The cost lever, including default preferences that change models, needs `prices` for both models. Without them, `cost` lowers effort only. An effort step uses only the efforts the Codex catalog lists for the model. A route without an `effort` gets no effort step: it runs the effort Codex sends, which is the one the router last wrote, so a step would repeat every turn. `cost` then keeps that route unless the cost lever applies, and `quality` takes the next higher tier's route unless that route is weaker.

Each mode also replaces some `policy` fields. The effective policy is `policy` with `modes.<mode>.policy` on top: a field the mode names wins over `policy`. A `modes` entry in your file changes only the fields it names over the built-in entry; set a field to `null` to drop a built-in one. Built-in values:

| Field                 | cost | balanced (`policy` as written) | quality |
| --------------------- | ---- | ------------------------------ | ------- |
| `upgradeVotes`        | 3    | 2                              | 1       |
| `upgradeBase`         | 0.85 | 0.75                           | 0.6     |
| `jumpConfidence`      | 0.98 | 0.95                           | 0.9     |
| `downgradeVotes`      | 1    | 2                              | 3       |
| `downgradeMass`       | 0.8  | 0.9                            | 0.95    |
| `minDwellRequests`    | 4    | 8                              | 16      |
| `escalationHoldTurns` | 1    | 2                              | 4       |

These values express a direction. They have not been tuned against a representative workload.

Example: a cost mode that waits longer before going down on the same task.

```json
{ "mode": "cost", "modes": { "cost": { "policy": { "minDwellRequests": 8 } } } }
```

Where a session's mode comes from, first match wins:

1. the session's own mode, set in the panel or with `codex-router ctl mode`;
2. the [project file](#project-file);
3. `mode` in this file;
4. `balanced`.

### Project file

A repository can choose a mode for its sessions in `.codex/router.json`. The file holds `mode` and nothing else:

```json
{ "mode": "quality" }
```

The router looks in the session's working directory and each parent up to the repository root (the nearest directory that holds `.git`); the nearest file wins. Outside a repository it looks only in the working directory. It reads the file at the start of each turn. The router ignores a file with another key, an unknown mode or bad JSON. The session then uses `mode` from this file, and the panel shows the problem under WORTH A LOOK.

`mode` only chooses among the routes you configured, so a cloned repository cannot point the router at other models. The file must be a regular file of at most 4 KiB; a symlink, a pipe, a device or a larger file is ignored with the same panel finding. Add `.codex/router.json` to `.gitignore` if the choice is yours alone.

### Upgrade to Terra defaults

After upgrading, restart the launcher or home services to load the new configuration. A partial config that omits routes inherits Terra. A full saved v0.9 configuration with explicit Sol medium/debug routes is custom and does not auto-migrate: remove those overrides if you want the new defaults. Existing explicit activity cells stay user-owned. No user config is rewritten.

### Upgrade from presets

v0.9.0 renamed `preset` to `mode` and `thrifty` to `cost`. A file that still has `preset` fails with `preset was renamed to mode in v0.9.0 (thrifty is now cost)`, and the router runs plain `codex`. Replace `"preset": "thrifty"` with `"mode": "cost"` and `"preset": "quality"` with `"mode": "quality"`, and delete `"preset": "balanced"`. As before, the mode's built-in values win over `policy` for the fields they name; to change one, set it under `modes`.

Old configs can still contain `policy.downgradeHorizonTurns`. It is ignored. It is absent from the defaults and modes, and you can remove it from your file.

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

The file holds the line `TYPESAFE_API_KEY=...`. One file can hold several keys and the Cloudflare account ID (`CLOUDFLARE_API_TOKEN=...`, `CLOUDFLARE_ACCOUNT_ID=...`, `OPENAI_API_KEY=...`). Each classifier reads only its own values. The wrapper can read the same file, so the key need not be in your shell. `codex-router doctor` does not inspect this file. It reports key presence from the process environment only. File-backed presence is unverified. Rules:

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

A queued ask waits for that single slot. `classifier.Health.QueueWait` records the most recent wait before slot acquisition, and `QueueTimeouts` counts asks whose own deadline expired while queued; a cancelled ask is not counted. The client logs `classifier-queue-timeout` with the wait duration. `Health.Latency` and `Advice.Latency` start after the slot is acquired, so they exclude queue wait. Queue observability does not change the one-slot limit, retries, breaker, or decision behavior.

When a decision fails or times out, that request is forwarded unchanged. The router's decision deadline is the classifier timeout plus 200 ms. Explicit pins and the context guard can still affect other requests while classification is off.

### context

| Field          | Default | Meaning                                                              |
| -------------- | ------- | -------------------------------------------------------------------- |
| `recentTurns`  | 6       | User or assistant text messages sent to the classifier (min 1).      |
| `maxTextChars` | 1200    | Each text is clipped to this many characters before sending (min 1). |

Set both low to send less text to a hosted classifier.

### cache

Codex's prompt cache is per model and ignores effort. A model counts as warm from its last response until `ttlMs - warmMarginMs` has passed. A switch to a cold model writes the whole prefix uncached; that is the tax the policy prices. Need `ttlMs > 0` and `0 <= warmMarginMs < ttlMs`.

### proxy

`port`: 0 binds any free port once per session and keeps it for the session. A fixed port stops a second session from starting. The listener is always `127.0.0.1`. `codex-router proxy --port N` overrides it. A set-up home ignores it: `codex-router setup` picks the home's port once and stores it in the home's state dir.

`upstream`: the backend the proxy forwards to. Unset, it is `https://chatgpt.com/backend-api/codex`. Set it to send routed requests through a gateway that proxies the same Codex API, for example one your organization requires:

```json
{ "proxy": { "upstream": "https://gateway.example.com/chatgpt/backend-api/codex" } }
```

The value must be an `https` URL with a host and no credentials, query or fragment. The proxy forwards Codex's headers unchanged, so **the gateway receives your ChatGPT credentials**: set only a host you trust with them. The router still picks the model and effort; the gateway sees the request after the rewrite. A set-up home's edge service reads this when it starts, so restart the service after a change. Use this when managed configuration forces `model_provider` to the gateway: the router cannot replace a managed provider, but it can sit in front of the gateway.

## model_context_window

Advice only. The router never edits this key: `codex-router setup` changes `config.toml` only inside its own marked blocks.

If you set `model_context_window` in `~/.codex/config.toml`, Codex sizes its context from that number whatever model the router picks. After a switch to a model with a smaller window, Codex would then send a thread that is too large for it. The fit guard rejects routes that are too small. Remove `model_context_window` to let Codex use the routed model's catalog entry. If you keep it, set it no larger than the smallest window among your routes.

The router uses the catalog's base window and effective percentage, not its maximum-window metadata or this native override. Once a successful response measures an unchanged input prefix, the fit estimate uses that measured total plus appended content; otherwise it uses the conservative full-body estimate. An effort-only change on the current model keeps its native context policy. See [context fit](architecture.md#from-request-to-route).

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
