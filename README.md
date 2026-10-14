# codex-router

Routes each step of a Codex CLI session to a model and reasoning effort picked by a small classifier, and shows the choice in Codex's own footer.

- A local loopback proxy sits between Codex and the ChatGPT backend and rewrites only `model` and `reasoning.effort` on user steps.
- A classifier (Jev by default, or a local Ollama model) scores each new turn. A policy with votes, hysteresis and a prompt-cache cost bar decides when to switch.
- Any failure forwards the request unchanged. Your Codex login is forwarded upstream and never stored or logged.

![Codex while a turn runs: the footer shows GPT-6-Sol with medium effort, the model the router chose](docs/images/codex-footer.svg)

The router writes each decision into the session's private Codex app-server, so the footer repaints with the routed model in any terminal. A separate panel shows why:

![Router panel, Now tab: next turn on gpt-6.1-sol high, activity debug 72%, pin buttons with classifier support, a strip of 144 routed replies by tier, findings, context and cache](docs/images/router-tui-now.svg)

The pictures use made-up sample data. Regenerate them with `go run ./scripts/render-docs` (see [docs/images](docs/images)).

## Install

Needs Go 1.27 and the Codex CLI on `PATH`. Developed and measured against Codex 0.162.1; other versions are untested.

```sh
go install github.com/alexei-led/codex-router/cmd/codex-router@latest
# or from a checkout
make build        # writes .bin/codex-router
```

## Quick start

```sh
export TYPESAFE_API_KEY=...      # classifier key; see docs/configuration.md
cd your-repo
codex-router                     # runs Codex through the router; codex args pass through
```

In a second terminal or a multiplexer split:

```sh
codex-router tui                 # panel for the newest running session
```

Without the key the router still starts, the classifier is off, and requests keep their model unless you pin a tier in the panel. If the router cannot start, it prints one line and runs plain `codex`.

## How routing works

- Only user steps are routed: `request_kind = "turn"` with no `x-openai-subagent` header. Compaction, subagents and anything unparsable pass through untouched.
- The classifier is asked on the first step of each turn, and again in the background every 4 requests.
- Four tiers (`micro`, `low`, `medium`, `high`) map to a model and effort. An optional activity (`debug`, `code`, ...) can replace the route for a tier.
- Moving up needs votes and enough probability mass. Moving down needs more, and on the same task also waits for a passed check. The bar rises with the uncached tokens a switch would write.
- Routes stay inside one model compatibility group, and a route is used only if the thread fits 80% of its context window.
- A model you pick with `/model` pins the thread. Routing holds it until you resume in the panel.

## Safety model

- The proxy binds `127.0.0.1` only and talks to one fixed upstream, `https://chatgpt.com/backend-api/codex`. It refuses redirects.
- `Authorization` and `ChatGPT-Account-ID` go upstream only. They are never logged and never reach the classifier.
- The classifier gets the clipped prompt and recent dialogue, nothing else. Request text is never logged.
- The control socket is `0600` in a `0700` directory. The router never edits `~/.codex/config.toml` or the shared Codex daemon.

Details: [architecture](docs/architecture.md#security-invariants).

## Docs

- [User guide](docs/user-guide.md): launching, footer, panel keys, pins, failure behaviour, hook
- [Configuration](docs/configuration.md): every field, presets, classifiers
- [Architecture](docs/architecture.md): components, data flow, invariants
- [Research](docs/research.md): what was measured and what is still unverified
