# codex-router: phase 1 brief (explore, brainstorm, design)

## Goal

Build a model router for the OpenAI Codex CLI with the same function as the two
routers the user already built:

- `~/Workspace/claude-model-router`: Claude Code Mod plugin. It rewrites
  `request.model` and `request.effort` on every `turn.step`
  (`hooks/native-router.mjs`).
- `~/Workspace/pi-model-router`: the Pi agent extension (TypeScript).

The router switches model and reasoning effort by:

- task **complexity**: a tier such as micro, low, medium or high;
- current **activity**: code, debug, explore, plan, review, ops or docs.

It must show its decisions in the UI, at least minimally, inside Codex. A proxy
design may also have a richer TUI of its own that works like the `/router` pane
and the band above the prompt in claude-model-router.

Phase 1 is research and design only. Do not implement the router yet.

## What is already known

These facts come from a research pass on codex-cli 0.162.1. Re-verify them; do
not trust them blindly.

### Codex app-server

- It speaks JSON-RPC over stdio, or WebSocket over a unix socket.
- A shared daemon serves each `CODEX_HOME`. Its socket is
  `~/.codex/app-server-control/app-server-control.sock`.
- These clients use it: TUI, `codex exec`, the VS Code extension, the desktop app.

### Protocol schema

- Dump it with `codex app-server generate-json-schema --out <dir>`. TypeScript
  bindings come from `generate-ts`.
- `thread/start` takes `model` and `modelProvider`. The provider is set per thread.
- `turn/start` takes `model` and `effort`. The override applies to this turn and
  to later turns, so set it on every turn.

### Hooks cannot route

- Events: `userPromptSubmit`, `preToolUse`, `postToolUse`, `subagentStart`,
  `subagentStop`, `stop`, `sessionStart`, `sessionEnd`, `preCompact`,
  `postCompact`, `permissionRequest`, `interrupt`.
- Hook output can only block a request or add context. It cannot change model or
  effort.

### The most promising path

Register a local Responses-API proxy as a custom `model_providers.<id>` with
`wire_api = "responses"`.

- Codex sends the proxy every model request, which means every step.
- Each request holds the full history: prompt, tool calls and tool outputs. The
  proxy can derive activity, tool-error facts and context size from it, then
  rewrite `model` and `reasoning.effort`.
- One proxy covers every client.

Alternatives:

- Your own app-server client that sets `turn/start` model and effort. It only
  works when it owns the UI.
- A `codex exec` wrapper. It decides once per invocation.

### Risks to verify with small spikes

1. **Auth.** The user is logged in with ChatGPT. Check whether a custom provider
   can reuse that auth (`requires_openai_auth`?), and which upstream endpoint the
   proxy must call.
2. **Switching models mid-thread.** Check that it keeps working with encrypted
   reasoning items, and what happens to prompt caching. This affects any cost
   model.
3. **Context window.** Codex computes the window and compaction from the
   configured model's metadata, so routing to a smaller window needs a
   context-fit guard.
4. **UI surface inside Codex.** Find what exists: status line or footer config,
   terminal title (OSC), notifications, hook `additionalContext`, the agterm
   status and HUD (`agtermctl session status` and `session hud`).
5. **Upcoming feature flags.** Check the state of `step_model_switching` and
   `model_catalog_in_context`; both are off by default.

### Reuse from claude-model-router

These modules are host-independent per its docs:
`lib/classifier-contract.mjs`, `lib/classifier-apis.mjs`, `lib/policy.mjs`
(votes, hysteresis, escalation), `lib/cost.mjs`, `lib/facts.mjs`, and
`lib/activity-stats.mjs`.

Compare them with pi-model-router and decide what a shared core could look like.

## How to work

1. **Explore.** Read both routers: architecture, routing signals, classifier
   contract, policy, UI, config, tests. Read the Codex sources and docs (GitHub
   `openai/codex`, `codex-rs/app-server*`, hooks, model providers). Use web or
   DeepWiki research where needed.
2. **Brainstorm with Codex through peer chat.** Load the `peer-chat` skill.
   Start a Codex peer in this agterm session with
   `~/.local/bin/peer-chat start --kind codex`.
   - Use it as an adversarial co-designer: ask it to challenge the architecture,
     verify Codex internals it knows well, and attack the risks above.
   - You are the only writer in this repo; the Codex peer stays read-only.
   - Keep each message to one focused question.
3. **Spikes are allowed, with limits.** Use small throwaway spikes to confirm
   the risky assumptions, mainly auth and switching models mid-thread.
   - Pass Codex `-c` overrides in a scratch session.
   - Do not edit `~/.codex/config.toml`, `~/.codex-work`, or the running Codex
     daemon.
   - Never print or log auth tokens.
4. **Design.**

## Deliverables (in this repo)

1. `docs/research.md`
   - Verified findings with evidence: file paths, URLs, command output.
   - Mark everything unverified.
   - Include the spike results.
2. `docs/architecture-options.html`
   - Two to four architecture options, for example: proxy only;
     proxy + app-server observer; own app-server frontend; shared router core
     with host adapters.
   - Compare them on modularity, balanced coupling (integration strength,
     distance, volatility), performance (latency per step), security (auth
     handling, local exposure), reliability (proxy failure, fail-open versus
     fail-closed, daemon restarts) and effort.
   - Include a diagram per option and a clear recommendation the user can pick
     from.
3. Three HTML UI prototypes: `docs/ui-prototype-1.html`, `docs/ui-prototype-2.html`
   and `docs/ui-prototype-3.html`.
   - Three alternative concepts. Each one shows:
     - the minimal in-Codex surface: what the user sees in the Codex TUI or
       terminal when the router picks a tier, activity and model;
     - the proxy TUI: a live view of decisions, tiers, activity, cost and
       context, and controls such as pin model, change policy, pause.
   - Base them on surfaces that really exist; label any surface that is
     hypothetical.
   - Use the claude-model-router band and `/router` pane as the reference
     experience.
   - Use static HTML with inline CSS and JS and realistic sample data.

When all four HTML files and `research.md` are ready, stop. Summarize the
options and your recommendation, and wait for the user to choose. Do not start
implementation.
