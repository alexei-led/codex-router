# codex-router: phase 1 research

Date: 2026-10-13. Codex CLI `codex-cli 0.162.1`. Source: `openai/codex` tag `rust-v0.162.1`,
commit `092d3acd6bec3e3a14bdc7e7a2810ab628ab759d`. All `codex-rs/...` paths are relative to
<https://github.com/openai/codex/tree/rust-v0.162.1>.

Evidence labels:

- **[spike]**: observed live in this session (logs in the session scratchpad; summarized below).
- **[src]**: read in Codex source at the tag above.
- **[local]**: command output or files on this machine.
- **[peer]**: reported by the Codex peer from source and not re-read by me. Treat as likely,
  not proven.
- **[unverified]**: inferred or not checked.

## 0. Decision (2026-10-13, user)

- **Architecture A: proxy only.** No shared router core across Claude, Codex and Pi; codex-router
  is self-contained.
- **UI:** a router TUI modeled on the Claude Code side panel (`/router` pane; its Now tab is
  being reworked in another session), plus the routed model in Codex's own footer. **No
  agterm-specific code.**
- How the footer works is described in §4.1.

## 1. Verdict

1. A local Responses proxy registered as a custom `model_providers.<id>` with
   `requires_openai_auth = true` reuses the ChatGPT login. **[spike]**
2. Per-step model and effort rewrite works **only inside a compatibility group**: same
   `comp_hash`, same Responses Lite mode, same tool mode. On this account that is every listed
   gpt-6.x and gpt-5.6 model. gpt-5.5 fails with HTTP 400. **[spike]**
3. Encrypted reasoning items replay without error across models in that group. **[spike]**
4. Prompt cache is per model. Each first visit to a model is cold. A detour does not evict the
   previous model's cache within minutes. An effort change on the same model keeps the cache.
   **[spike]**
5. Codex keeps context window, compaction and truncation from the configured model. The proxy
   must guard context fit itself. **[src]**
6. Native per-step switching (`step_model_switching` + app-server `turn/settings/update`)
   exists but is UnderDevelopment, off by default, and best-effort per step. **[src][peer]**
7. Hooks cannot route. They can show a one-line, honest `↳ Hook · router: …` entry in the TUI.
   **[src][peer]**

## 2. The existing routers

Sources: subagent read of both repos; file:line citations in the repos.

### claude-model-router 1.12.0 (Claude Code Mod, JS)

- Entry: `hooks/native-router.mjs` `register()` at `:1537`. It runs in-process in the Mod
  engine and rewrites `request.model` and `request.effort` at `:1691-1694`; subagents at
  `:1155-1168`.
- Per `turn.step`: the first step of a turn asks the classifier and runs `chooseRoute`
  (`:1648-1670`). Later steps run `reviewMidTurn`, `moveOnObserved` and `continueRoute`
  (`:1675-1677`).
- Tiers `micro, low, medium, high`; activities `code, debug, explore, plan, review, ops, docs`
  plus `uncertain` (`lib/config.mjs:3-7`).
- Classifier: advice `{choice, confidence, probabilities, continuation, activity}`
  (`lib/classifier-contract.mjs:6`). Adapters `system-one` (default Jev, 1500 ms),
  `openai-decisions`, `ollama` (`lib/classifier-apis.mjs`). The client has a total deadline,
  2 attempts, and a breaker of 3 failures that pauses for 60 s (`lib/classifier-client.mjs`).
- Policy `lib/policy.mjs:20-104`: escalation on a repeated tool error (hold 2 turns), the last
  3 votes, an upgrade bar of `0.75 + 0.15·tax/(tax+0.5)` with 2 votes, a downgrade bar of
  `0.9 + 0.08·…`, context fit at 80 % (`:108-121`).
- Transcript facts `lib/facts.mjs`: repeated tool-error signature (`:24-61`), observed
  activity from tool names (`:121-130`), verification gate (`:155-228`).
- Cost: 5-minute cache TTL with a 30 s margin, switching tax, downgrade tax (`lib/cost.mjs`).
- UI: band above the prompt (`lib/band.mjs` `renderBand :268`) and the `/router` pane
  (`lib/panel.mjs` `renderPanel :160`), with tabs Now, Routes, Policy, Classifier and Usage.
  The visual reference for the prototypes.
- Host independence: `lib/` imports only `lib/`; there are no `node:`, `fetch` or `process`
  references. **But the data shapes are Anthropic:** Messages `tool_use`/`tool_result`, usage
  fields, Claude model IDs and cache-sharing lists (`facts.mjs`, `route.mjs`, `cost.mjs:6`,
  `config.mjs`). The docs make no explicit host-independence claim; only "pure" file headers.

### pi-model-router 0.14.0 (Pi extension, TypeScript)

- Entry `extensions/index.ts:35`. It registers provider `router` (`provider.ts:166`) and
  switches with `pi.setModel` / `pi.setThinkingLevel` (`index.ts:166-206`).
- Same tier and activity names (`types.ts:10-20`).
- No local heuristics, by mandate (`AGENTS.md:5,33`). The classifier goes through Pi's registry
  with no wire code (`AGENTS.md:31`). Candidate IDs are `tier|model|thinking`
  (`choice.ts:124-133`). Selection: confidence ≥ 0.65, otherwise the lowest tier whose
  cumulative mass reaches 0.8 (`choice.ts:214-256`).
- Policy is stateless: `evaluateSwitch` prices a cold first request plus warm requests over a
  horizon (`switch-policy.ts:104-210`). No votes or escalation. Context fill 0.9.
- UI: footer status plus a 2-line widget; commands status, usage, settings, profile, pin,
  thinking, log, widget, off (pause), reload, help.

### Shared-core implications

| Concept                     | Claude                            | Pi                                    | Shared-core decision needed                                                                    |
| --------------------------- | --------------------------------- | ------------------------------------- | ---------------------------------------------------------------------------------------------- |
| Wire protocol to classifier | own adapters                      | host registry                         | Core defines a `Classifier` port; adapters live outside the core                               |
| Policy state                | votes, hysteresis, escalation     | stateless per request                 | Core policy takes state in and returns state out (pure); hosts persist it                      |
| Cache model                 | TTL inferred from model names     | explicit per-alias sharing, no TTL    | Core takes a `CacheModel` port. Codex measured: per-model cache, effort-neutral                |
| Transcript facts            | Anthropic Messages                | none                                  | Core defines a neutral `Transcript` (turns, tool calls, errors, sizes); each host maps into it |
| Config keys                 | `routes`, `policy`, `classifiers` | `profiles`, `switchPolicy`, `advisor` | Pick one schema for the core; hosts translate                                                  |

Portable as-is: the classifier contract text and parser, the classifier client
(deadline, retry, breaker), `policy.mjs` with injected costs, and Pi's `selectRoute` cumulative
selection and `evaluateSwitch`.

## 3. Codex extension points

### 3.1 Model providers and auth

- Provider fields are in `codex-rs/model-provider-info/src/lib.rs:139-208`
  (`deny_unknown_fields`). `wire_api` accepts only `"responses"` (`:107-133`). **[src]**
- Built-in providers cannot be overridden by `model_providers.openai`; configured entries are
  inserted with `or_insert` (`:699-733`). The built-in base URL is changed only by the top-level
  `openai_base_url` (`codex-rs/config/src/config_toml.rs:426-427`). **[src]**
- With `requires_openai_auth = true` and no `env_key` or `experimental_bearer_token`, Codex
  sends `Authorization: Bearer <ChatGPT access token>` and `ChatGPT-Account-ID`
  (`codex-rs/model-provider/src/auth.rs:201-206,292-324`; `bearer_auth_provider.rs:31-46`).
  **[src]** Observed on the wire: `authorization` (redacted, length 1852) and
  `chatgpt-account-id`. **[spike]**
- The ChatGPT upstream is `https://chatgpt.com/backend-api/codex` + `/responses`
  (`lib.rs:80,427-445`; `codex-rs/codex-client/src/provider.rs:55-77`). **[src]** A request
  forwarded there returns 200. **[spike]**
- A custom provider also calls `GET {base_url}/models?client_version=0.162.1`. It ran twice per
  exec, and forwarding it to the backend returns 200. **[spike]**
- Transport: a custom provider defaults to `supports_websockets = false`, so it uses HTTP POST
  with `Accept: text/event-stream` (`codex-rs/codex-api/src/endpoint/responses.rs:135-149`).
  **[src][spike]**
- zstd request compression applies only when the provider `name == "OpenAI"`
  (`codex-rs/core/src/client.rs:1634`; `lib.rs:616`). No compression was observed with name
  `router-spike`. **[src][spike]**
- Request headers seen: `x-codex-beta-features: remote_compaction_v2`, `x-codex-window-id`,
  `x-codex-turn-metadata` (JSON with session_id, thread_id, turn_id, agent_name),
  `x-openai-internal-codex-responses-lite`, `x-client-request-id = session-id = thread-id`,
  `originator: codex_exec`, `user-agent: codex_exec/0.162.1 …`. **[spike]**
- **Header hazard.** Codex reads only the `openai-model` / `x-openai-model` response header to
  learn the server model, not `response.model` (`codex-rs/codex-api/src/sse/responses.rs:51,194-208`).
  A mismatch emits `ModelReroute` with a hard-coded "account was flagged for potentially
  high-risk cyber activity" warning (`codex-rs/core/src/session/mod.rs:3946-3984`). **[src]**
  The backend sent no such header in any spike request, so the warning never fired. **[spike]**
  The proxy must still strip or rewrite this header defensively.

### 3.2 Request body

- Fields are in `codex-rs/codex-api/src/common.rs:279-304`. `model` is the configured slug;
  base instructions go in as a developer input item; `store: false`;
  `include: ["reasoning.encrypted_content"]`; `prompt_cache_key` is always set
  (`codex-rs/core/src/client.rs:891-1013`). **[src]**
- Observed: `reasoning = {effort, summary: "concise", context: "all_turns"}`; top-level
  `tools` is empty and tools travel as an `additional_tools` developer item (Responses Lite);
  the first request body is about 99 KB; no `previous_response_id`; the full history is resent
  every step. **[spike]**
- WebSocket incremental mode (`previous_response_id`) exists only when the model, tools and
  reasoning are unchanged (`client.rs:341-393`). A router that changes the model per step
  defeats it anyway. **[src]**

### 3.3 Model metadata, context window, compaction

- The catalog comes from the bundled `models.json`, from `{base_url}/models`, or from
  `model_catalog_json` (`codex-rs/model-provider/src/provider.rs:388-420`). An unknown model
  falls back to a 272k window with a warning (`codex-rs/models-manager/src/model_info.rs:97-135`).
  **[src]**
- Effective auto-compaction is `min(config limit, 0.9 × window)`. The usable window is
  `window × effective_context_window_percent / 100` (`codex-rs/protocol/src/openai_models.rs:521-538`).
  **[src]**
- All of this follows the **configured** model, not the served one. The proxy cannot change it.
  **[src][peer]**
- Local catalog (`~/.codex/models_cache.json`, fetched 2026-10-13T15:06Z). **[local]**

| Model                              | Window / max | comp_hash | Lite | tool_mode      | effort updates |
| ---------------------------------- | ------------ | --------- | ---- | -------------- | -------------- |
| gpt-6.1-sol                        | 500k / 872k  | 3000      | yes  | code_mode_only | yes            |
| gpt-6-astra, gpt-6-sol, gpt-6-luna | 272k / 872k  | 3000      | yes  | code_mode_only | yes            |
| gpt-5.6-sol, -terra, -luna         | 272k / 872k  | 3000      | yes  | code_mode_only | no             |
| gpt-5.5 (hidden)                   | 272k / 272k  | 2911      | no   | none           | no             |

The user's config sets `model = "gpt-6.1-sol"`, `model_context_window = 872000` and
`model_auto_compact_token_limit = 780000`. **[local]** A proxy that sends such a thread to
gpt-6-luna (272k default window) needs a context-fit guard. Either the client limits cover
every route (window = min over routes), or the guard refuses a smaller route when the request
does not fit. The guard must never clip history.

### 3.4 Switching models mid-thread

- Codex filters no reasoning items on a model change. `encrypted_content` is replayed as-is
  (`codex-rs/core/src/context_manager/history.rs:600`). **[src][peer]**
- A native switch (`turn/start.model`, `thread/settings/update`) injects a `<model_switch>`
  developer fragment and may compact with the previous model when `comp_hash` differs or the
  window shrinks (`codex-rs/core/src/session/turn.rs:1333-1432`). An invisible proxy rewrite
  skips both. That is safe only within one `comp_hash` group. **[src][peer]**
- Ranked failure modes of an invisible rewrite **[peer]**:
  1. compaction checkpoint incompatibility;
  2. Responses Lite / tools-in-history mismatch;
  3. context and truncation limits taken from model A;
  4. effort-update items;
  5. prompt, tool or policy contract mismatch;
  6. reasoning continuity;
  7. cache loss.

  Items 1, 2 and 5 are removed by the group restriction. Item 3 needs the context guard. Item 4
  does not apply: a provider not named `OpenAI` disables effort updates, so a plain
  `reasoning.effort` rewrite applies (`codex-rs/core/src/session/reasoning_effort.rs:28-139`).
  Item 6 is covered by spike 5. Item 7 is a cost-model input.

### 3.5 Feature flags **[local][src]**

- `step_model_switching`: UnderDevelopment, default false
  (`codex-rs/features/src/lib.rs:406-407,1912-1917`). It gates `turn/settings/update`, which
  changes model, effort, summary or tier for the later steps of a running turn. Without it the
  call fails with "turn settings updates require the step_model_switching feature"
  (`codex-rs/core/src/session/step_activation.rs:229-245`).
- `model_catalog_in_context`: UnderDevelopment, default false. It moves `spawn_agent` model
  choices into context (`codex-rs/core/src/session/world_state.rs:377-393`). It is not
  self-routing.
- Also present, related: `reasoning_effort_override` (UnderDevelopment, off). **[local]**
  Behaviour not read. **[unverified]**

### 3.6 App-server

- Socket: `$CODEX_HOME/app-server-control/app-server-control.sock`
  (`codex-rs/app-server-transport/src/transport/mod.rs:53-70`); it exists on this machine.
  **[src][local]** The transport is WebSocket over a unix socket
  (`codex-rs/app-server-daemon/src/client.rs:63-74`). **[peer]**
- `thread/start` takes `model`, `modelProvider`, `config` (overrides). `turn/start` takes
  `model`, `effort`, `summary`, and experimental `responsesapiClientMetadata`; the last is
  flattened into `x-codex-turn-metadata` on every request (`v2/turn.rs:167-293`). That makes
  it a hint channel from a frontend to the proxy. **[src]**
- Notifications include `turn/started`, `turn/completed`, `item/*`,
  `thread/tokenUsage/updated` (with `modelContextWindow`), `model/rerouted` and
  `thread/settings/updated` (`common.rs:1935-2045`). **[src]**
- A second client can `thread/resume` a running thread, which rejoins it and subscribes
  (`v2/thread.rs:340-353`). **[src]**
- A sidecar controller is **partly viable** **[peer]**:
  - `turn/settings/update` and `thread/settings/update` have no owner check
    (`app-server/src/request_processors/turn_processor.rs:213-254`).
  - Both need `initialize.capabilities.experimentalApi = true`
    (`initialize_processor.rs:75-83`).
  - The feature can be set per new thread through `thread/start.config`, but not retrofitted
    on a running TUI thread ("config overrides were provided and ignored while running",
    `thread_processor.rs:214-217`).
  - `item/completed` is not a step barrier. Core captures the next step's settings before the
    request (`core/src/session/mod.rs:3728-3740`), so an update can miss the next sample.
  - Even when applied, the turn loop keeps the initial `TurnContext` for context and
    compaction.
- `codex exec` always runs an in-process app-server (`codex-rs/exec/src/lib.rs:731-752`).
  **[src]** The TUI uses the shared daemon unless `--no-daemon`, any `-c` override or
  `--profile` is given (`codex-rs/tui/src/daemon_startup.rs:80-144`). **[src]** So
  `codex -p router` runs an embedded app-server and leaves the daemon alone.

### 3.7 Hooks **[src]**

- Events: PreToolUse, PermissionRequest, PostToolUse, PreCompact, PostCompact, SessionStart,
  SessionEnd, UserPromptSubmit, SubagentStart, SubagentStop, Stop, Interrupt
  (`codex-rs/protocol/src/protocol.rs:1595-1608`).
- Output schemas use `deny_unknown_fields` and have no model or effort field
  (`codex-rs/hooks/src/schema.rs`). Hooks cannot route.
- Inputs include `session_id`, `turn_id` and `model` (`schema.rs:279-290`). A hook can therefore
  ask the router for its decision on that turn, matched through `x-codex-turn-metadata`.

### 3.8 Failure modes of a proxy (adversarial review)

From the Codex peer's final review. Items marked [src] I re-read.

- **A dead proxy blocks Codex.** `request_max_retries = 4` gives 5 HTTP attempts
  (`codex-rs/model-provider-info/src/lib.rs:66-75`) [src]. The stable feature
  `unbounded_connection_retries` defaults to true (`codex-rs/features/src/lib.rs:1382`) [src],
  so a dead listener can leave the turn reconnecting indefinitely
  (`codex-rs/core/src/responses_retry.rs:92-119`) [peer]. Fail-open covers decision failures
  only while the listener is alive. Mid-turn there is no automatic switch back to the backend.
  Mitigation: a launcher starts the proxy and health-probes it before Codex starts, the port
  is stable, and the transport path stays tiny and separate from the classifier. Never retry
  a request after upstream may have started or SSE has been emitted; propagate the
  cancellation and let Codex recover the stream.
- **Compaction requests reach the proxy.** A custom provider defaults to
  `remote_compaction: Unsupported` unless named `OpenAI` or Azure; config can override it with
  `capabilities.remote_compaction = "v2"` (`codex-rs/model-provider/src/capabilities.rs:30-52`)
  [src]. Remote v2 compaction posts to `/responses` with compaction metadata
  (`codex-rs/core/src/compact_remote_v2_attempt.rs:87-110`) [peer]. The router must recognise
  compaction, memory and guardian requests and never re-route them. Choosing local or remote
  compaction for the router provider is a phase-2 decision: the user's normal setup uses
  remote v2.
- **State gets attributed to the wrong thread.** `x-openai-subagent` names a role, not an
  identity. `prompt_cache_key` can be shared, and `x-client-request-id` equals the thread id
  rather than being unique per request (`codex-rs/core/src/responses_metadata.rs:325-359`;
  `client.rs:581-604`) [src/peer]. Key router state by account + thread id + turn id + a
  request fingerprint. Keep no global "current tier". Pin the decision for retries of the same
  request.
- **Catalog drift.** Pass the `/models` query and etag through unchanged. Compute
  compatibility groups from the refreshed catalog. An unknown or incompatible model means the
  request is forwarded unchanged.
- **Credentials and prompts leak.** A 0600 control socket does not protect the loopback TCP
  listener. Requirements: bind 127.0.0.1 only; a fixed HTTPS upstream and path allow-list; no
  redirects. Auth headers go upstream only, never to the classifier, logs, the HUD or the
  event bus. No header or body logging by default. Other processes of the same user stay
  trusted; socket modes cannot remove that.
- **An observer cannot see an embedded session.** `codex -p router` runs an embedded
  app-server, so a daemon observer cannot see its threads. Cold-resuming that thread in the
  daemon would start a second execution, not observe the first. Option B requires the routed
  threads to run in the daemon itself (provider set in `config.toml`, or `thread/start` with
  `modelProvider`).

## 4. UI surfaces

| Surface                                                    | Real?                                                                                                                                                                                           | Per step?                                                                        | Verdict                                                                                                                                                        |
| ---------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | -------------------------------------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Hook `systemMessage` (UserPromptSubmit, PostToolUse, Stop) | yes **[src]**                                                                                                                                                                                   | per tool call or per turn, not per inference **[peer]**                          | Build. Renders `↳ Hook · router: …` (`codex-rs/tui/src/history_cell/hook_cell.rs:272-340`) **[peer]**. Never use `additionalContext`: it enters model context. |
| `tui.status_line`                                          | yes **[local]**                                                                                                                                                                                 | shows the configured model only                                                  | No custom item. Optionally the `thread-title` item via `thread/name/set`, but that overwrites the conversation name persistently **[peer]**. Opt-in only.      |
| `tui.terminal_title` / OSC                                 | yes                                                                                                                                                                                             | Codex owns and refreshes it                                                      | Only if the user sets `terminal_title = []` and the router owns the title. Whether `[]` fully disables it is **[unverified]**.                                 |
| `notify` program                                           | yes                                                                                                                                                                                             | per turn end; payload has no model (`codex-rs/hooks/src/legacy_notify.rs:14-42`) | Useful only as a trigger.                                                                                                                                      |
| agterm `session status` / `context` / `hud`                | yes **[local]**: `agtermctl session status <idle\|active\|completed\|blocked> --color --shape`; `session context` (title bar); `session hud open\|update\|close --markdown --detail --position` | yes, the proxy can update on every request                                       | Build as the per-step surface when running in agterm.                                                                                                          |
| Proxy-owned TUI (separate pane)                            | ours                                                                                                                                                                                            | yes                                                                              | Build: the `/router` pane equivalent.                                                                                                                          |
| Injected SSE reasoning summary                             | displayable                                                                                                                                                                                     | yes                                                                              | **Do not ship.** It impersonates the model, and an altered `done` item persists and replays to the model **[peer]**.                                           |
| `warning` / MCP status / `model/verification`              | no generic channel **[peer]**                                                                                                                                                                   | –                                                                                | Do not spoof.                                                                                                                                                  |

### 4.1 Footer status that works in any terminal: a private app-server

The footer's `model-with-reasoning` item can show the routed model natively, in any terminal,
if the router is a second client of the app-server that the TUI uses.

- **The TUI follows thread settings.** On `thread/settings/updated` the TUI copies `model` and
  `effort` into its collaboration-mode settings, then calls `refresh_status_surfaces()`
  (`codex-rs/tui/src/chatwidget/settings.rs:395-411,539-543`) [src]. The app-server emits that
  notification whenever applied thread settings change
  (`codex-rs/app-server/src/bespoke_event_handling.rs:1235-1250`) [src].
- **Any client can change them.** `thread/settings/update` (experimental; needs
  `experimentalApi`) takes `model` and `effort` "for subsequent turns"
  (`codex-rs/app-server-protocol/src/protocol/v2/thread.rs:235-290`) [src]. It has no owner
  check [peer].
- **Live check (spike 7).** On a standalone `codex app-server --listen stdio://`:
  `thread/start` (gpt-6.1-sol, high), then `thread/settings/update {model: gpt-6-luna, effort:
low}`. Result: one `thread/settings/updated` with `{model: gpt-6-luna, effort: low}`.
  Repeating the same settings emits nothing. [spike]
- **Reaching the app-server under option A.** A `codex -p router` embedded app-server cannot
  be reached from outside, so the launcher runs a **private** app-server per session instead:
  `codex app-server --listen unix://$RUNTIME/app.sock -c model_provider=router …`. The TUI
  attaches with `codex --remote unix://$RUNTIME/app.sock`, and the router opens a second
  connection. Both flags exist in 0.162.1 [local]. The shared daemon and `config.toml` stay
  untouched.
- **Semantics.**
  - The proxy routes each step. The router mirrors each decision into thread settings, so the
    footer shows the current route.
  - Codex starts the next turn natively on that model: it re-resolves the window, adds the
    `<model_switch>` fragment, and compacts itself if needed. So between turns Codex's own
    context accounting matches the route, and the proxy rewrites only mid-turn changes.
  - A model the user picks with `/model` also arrives as `thread/settings/updated`. The router
    must treat that as a manual pin, not as its own echo: match against the last value it
    wrote.
- **Not verified live:**
  - two clients on one `--listen unix://` app-server, with the TUI attached via `--remote`
    (this is the same mechanism the shared daemon uses for TUI + VS Code);
  - the footer repaint while a turn is running;
  - feature parity of a `--remote` TUI against an embedded one.

  Spike these first in phase 2.

Fallbacks that also work in any terminal: a hook `systemMessage` line on model switches, and
the terminal title via OSC 2 written to the launcher's tty, with `tui.terminal_title = []` set
in the router profile so Codex does not overwrite it.

## 5. Spike results

Method: a throwaway Node proxy (`proxy.mjs`) on `127.0.0.1`. It logged header names only
(values redacted), the body shape, and SSE event types and usage. It optionally rewrote
`model` and `reasoning.effort` per POST, and forwarded to `https://chatgpt.com/backend-api/codex`.
Codex ran as `codex --no-daemon exec --ephemeral -s read-only` with
`-c model_provider="router"` and `-c model_providers.router={…, requires_openai_auth=true}`.
The user ran the scripts, because the auto-mode classifier refused an unsandboxed Codex run
from the agent. No config files were edited, and the daemon was not touched.

| #   | Spike                                                                                                                                              | Result                                                                                                                                                                                                                                          |
| --- | -------------------------------------------------------------------------------------------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| 1   | Auth: one-shot prompt via custom provider                                                                                                          | 200; ChatGPT bearer + account id forwarded; `GET /models` ×2 forwarded, 200. Answer `OK`.                                                                                                                                                       |
| 2   | Control: 3-step tool task, gpt-6-luna low, no rewrite                                                                                              | Cached input per step 4,864 → 20,224 → 20,224. Turn: 62,643 in, 45,312 cached (72 %). Answer `42`.                                                                                                                                              |
| 3   | Switch per step in group: luna medium → sol low → luna low; upstream `openai-model` passed through                                                 | All 200. Sol step cache **0** (cold); back on luna cache **20,224** (warm survived detour; effort medium→low kept cache). Turn 62,709 in, 25,088 cached (40 %). Upstream sent **no** `openai-model` header, so no reroute warning. Answer `42`. |
| 4   | Cross group: luna high → gpt-5.5                                                                                                                   | Step 2 **HTTP 400** `"This model is not supported when using X-OpenAI-Internal-Codex-Responses-Lite."` Turn failed.                                                                                                                             |
| 5   | Encrypted reasoning across group: sol high → luna → 5.6-terra → 6.1-sol → sol, each step replaying 1–3 `encrypted_content` items from other models | All 200; correct answer. Cache per step 0, 1,792, 0, 0, then **20,864** on return to sol. Turn 106,546 in, 22,656 cached (21 %).                                                                                                                |
| 5c  | Control for 5: sol high ×4                                                                                                                         | Cache 0 → 20,864 → 20,992 → 21,120. Turn 85,123 in, 62,976 cached (74 %).                                                                                                                                                                       |
| 6   | Proxy rewrite cost (local bench, Node)                                                                                                             | parse + rewrite + stringify: 96 KB 0.14 ms; 957 KB 1.34 ms; 7.6 MB 10.7 ms.                                                                                                                                                                     |
| 7   | Footer path: standalone `codex app-server --listen stdio://`, `experimentalApi`, `thread/start` then `thread/settings/update {model, effort}`      | `thread/settings/updated` emitted once with `gpt-6-luna` / `low`; identical repeat emitted nothing. No model calls.                                                                                                                             |

Upstream time to first byte was 0.8–1.2 s, with one outlier of 2.7 s. The proxy adds a
localhost hop plus the times in row 6. That is negligible next to the classifier deadline
(claude-model-router: 1.5 s for Jev).

What the spikes prove and do not prove:

- They prove auth reuse, the group restriction, and encrypted reasoning carry-over on these
  pairs and short threads.
- Not tested:
  - long threads that pass auto-compaction (remote compaction v2) and then switch models;
  - a `comp_hash` change inside one thread;
  - parallel subagent threads (`x-openai-subagent`);
  - the TUI (only `exec` was run);
  - VS Code and the desktop app;
  - how much a cold cache costs on a 500k-token thread.

  All **[unverified]**.

## 6. Risk register (brief §"Risks to verify")

| Risk                 | Status                                                 | Design consequence                                                                                                                                                                                                                               |
| -------------------- | ------------------------------------------------------ | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| 1. Auth              | **Resolved** [spike]                                   | Use `requires_openai_auth = true`, no `env_key`. Proxy is a pass-through for `Authorization`; never logs or stores it. Do **not** name the provider `OpenAI` (that enables zstd and Codex-backend extras).                                       |
| 2. Mid-thread switch | **Resolved within a group** [spike]; cross-group fails | Router config declares groups. Policy never routes outside the configured model's group. Cache cost is per model: hysteresis and a switching-cost model are mandatory.                                                                           |
| 3. Context window    | Confirmed hazard [src]                                 | Context-fit guard in the proxy using the request size and the catalog window per route; refuse a smaller route rather than clip. Recommend `model_context_window` ≤ smallest routed window, or the guard simply never downshifts a large thread. |
| 4. UI surface        | Mapped [src][peer][local]                              | Minimal: hook line + agterm status/HUD. Rich: proxy TUI. Status line keeps showing the configured model; label it as such.                                                                                                                       |
| 5. Feature flags     | Confirmed off [local][src]                             | `step_model_switching` is a future path for an app-server controller, not a basis for phase 2.                                                                                                                                                   |
| 6. Proxy outage      | Confirmed hazard [src][peer]                           | Launcher with a health probe; supervised proxy on a stable port; minimal transport path; no automatic re-send after upstream started. Phase 2 starts with fault tests: kill before bind, before upstream, after first output item.               |
| 7. Compaction        | Confirmed [src]                                        | Recognise compaction requests and never re-route them. Decide local or remote (`capabilities.remote_compaction = "v2"`) deliberately.                                                                                                            |

## 7. Unverified

- Behaviour after auto-compaction followed by a switch. Local compaction was the default in
  the spikes, because a custom provider has remote compaction off.
- Proxy fault behaviour: no fault-injection runs yet.
- Whether the backend ever returns `openai-model`; it did not in 5 spikes.
- Whether `tui.terminal_title = []` fully disables Codex title writes.
- Daemon config hot-reload of a new `model_providers` entry. Matters only if the provider goes
  into `config.toml` for all clients.
- `reasoning_effort_override` feature semantics.
- VS Code extension and desktop app behaviour with a custom provider.
- Peer-only items marked **[peer]** above (source-read by the Codex peer, not re-read here).

## 8. Process notes

- The Codex peer (agterm right split) acted as adversarial co-designer and answered five
  questions: transport and auth, mid-thread rewrite risks, sidecar controller viability, UI
  surfaces, and an attack on the recommended architecture. It stayed read-only.
- Sandbox: `codex exec` needs write access to `~/.codex` (in-process app-server state), so it
  cannot run inside this agent's sandbox. The user ran the spike scripts.
