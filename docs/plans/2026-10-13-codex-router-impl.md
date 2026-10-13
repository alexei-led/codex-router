# codex-router phase 2: implementation plan

Date 2026-10-13. Codex CLI 0.162.1. Go 1.27, module `github.com/alexei-led/codex-router`, branch
`feat/phase2-impl`. Source of truth: `docs/research.md` (facts, spikes 1-7, §3.8, §4.1, §6),
`docs/architecture-options.html` (decision A, proxy only, no shared core), `docs/ui-selected.html`.

## Goal

One binary `codex-router` that: starts a loopback Responses proxy, a private `codex app-server`
on a unix socket, and `codex --remote`; routes every main-thread step to a tier × activity route
inside the configured model's compatibility group; mirrors each decision into Codex's footer via
`thread/settings/update`; shows a `/router`-style TUI over a 0600 control socket. Auth headers
pass upstream only. Plain `codex`, `~/.codex/config.toml`, `~/.codex-work` and the shared daemon
are never touched.

## Decisions

| #   | Decision                                                                                                                                                                                                                                                                                                                                                                                                                    | Reason                                                                                                                                                                                                                                                                                                                   |
| --- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| D1  | Config: JSON at `$XDG_CONFIG_HOME/codex-router/config.json` (default `~/.config/...`), `encoding/json` with `DisallowUnknownFields`; missing file = defaults.                                                                                                                                                                                                                                                               | stdlib, one file, strict parse catches typos. TOML needs a dep.                                                                                                                                                                                                                                                          |
| D2  | Default routes (group comp_hash 3000, research §3.3): high `gpt-6.1-sol·high`, medium `gpt-6-sol·medium`, low `gpt-6-luna·medium`, micro `gpt-6-luna·low`; activity override `debug@medium → gpt-6-sol·high`. Baseline tier `medium`.                                                                                                                                                                                       | Matches ui-selected.html; all four models share comp_hash/Lite/tool_mode (spikes 3, 5). gpt-5.5 blocked (spike 4).                                                                                                                                                                                                       |
| D3  | Classifier adapters: `system-one` (Jev, default, 1500 ms; also Clef via endpoint) and `ollama` (local, no key). `openai-decisions` not ported.                                                                                                                                                                                                                                                                              | Two adapters cover hosted + local. Decisions API needs a separate OpenAI key the user does not use. Upgrade trigger: a user asks. Key comes from the env var named by `keyEnv`; the classifier request carries only prompt + clipped dialogue, never Codex headers (type-level: `classifier.Input` has no header field). |
| D4  | Compaction: keep the custom provider's default (`remote_compaction` unsupported → local compaction). No `capabilities` override.                                                                                                                                                                                                                                                                                            | One fewer moving part; no server-side checkpoint bound to a model (research §3.8 failure mode 1). Compaction then arrives as a normal-looking `/responses` POST on the configured model. Spike 1c proves its shape.                                                                                                      |
| D5  | Pass-through detection until spike 1c lands: a request is NOT a user step (forward unchanged, no decision, no classifier) when any of: `x-openai-subagent` header present; `x-codex-turn-metadata` missing/unparseable or without `turn_id`; last `user` text starts with a `passthrough` prefix (config list seeded with the Codex summarization prompt opener once 1c captures it).                                       | Conservative: subagents, memory, guardian and compaction all forward untouched. Upgrade trigger: 1c shows a stable discriminator (header or body field) → replace the prefix rule.                                                                                                                                       |
| D6  | Context-fit guard: `approxTokens = max(last response total tokens for the thread, body bytes/4)`; a route is eligible only if `approxTokens ≤ 0.8 × effective window(model)` from the catalog; unknown window = ineligible; no cheaper fit → forward original. **Also upward:** if the request's own model does not fit but the thread's baseline does, rewrite up to baseline and write settings back. Never clip history. | Research §3.3: Codex sizes the window from config (`model_context_window = 872000`), so after a settings write to a 272k model Codex itself would send an oversized thread. Docs tell the user to drop `model_context_window` from config.toml when routing.                                                             |
| D7  | Runtime dir `$XDG_RUNTIME_DIR/codex-router/<8-hex>/` else `$TMPDIR/codex-router/<8-hex>/` (0700), holding `app.sock`, `control.sock`, `proxy.port`. Proxy port: `proxy.port` config, default 0 = ephemeral, bound once per session and kept for the session (re-listen on the same port on supervised restart).                                                                                                             | macOS has no XDG_RUNTIME_DIR; `$TMPDIR` is per-user 0700. Fixed port would block two sessions. The app-server gets the port via `-c`, so it only needs to be stable per session.                                                                                                                                         |
| D8  | Control protocol: JSON lines over unix socket, mode 0600. Server sends a full `route.Snapshot` on connect and after every change (coalesced ≤ 20/s). Client sends `control.Command` (`pin`, `unpin`, `pause`, `resume`, `preset`, `snapshot`). TUI holds no state of its own: it renders the last snapshot.                                                                                                                 | Full-state snapshots are a few KB; no event replay or ordering bugs; the hook uses the same snapshot.                                                                                                                                                                                                                    |
| D9  | App-server transport: JSON-RPC 2.0 over WebSocket over the unix socket (research §3.6 [peer]) via `github.com/coder/websocket` (zero transitive deps). The `appserver.Dialer` interface isolates it; spike 1a proves the handshake and the fallback is a newline JSON-RPC `Dialer`.                                                                                                                                         | RFC 6455 client framing done right is ~400 lines; coder/websocket is one package. Justified extra dep.                                                                                                                                                                                                                   |
| D10 | Classifier timing: blocks the first step of a turn up to the classifier deadline (1500 ms); review every `reviewEveryRequests=4` requests in the background, applied at the next step. Retries of the same request (same thread+turn+fingerprint) reuse the pinned decision.                                                                                                                                                | Same as claude-model-router; upstream TTFB is ~1 s anyway (research §5).                                                                                                                                                                                                                                                 |
| D11 | Cost model: cache is per model and effort-neutral (spikes 3, 5); warmth = last response on that model within TTL 5 min − 30 s margin (configurable). Switching tax in **uncached tokens**; USD only when `prices[model]` is configured (default empty: the plan bills limits).                                                                                                                                              | Spikes measured tokens, not prices. Keeps UI honest ("list-price estimates").                                                                                                                                                                                                                                            |
| D12 | Manual pin: a `thread/settings/updated` whose `{model, effort}` differs from the last value the router wrote for that thread → `ModeManual` for the thread; proxy forwards unchanged until TUI **resume**.                                                                                                                                                                                                                  | ui-selected.html semantics; research §4.1.                                                                                                                                                                                                                                                                               |
| D13 | Launcher fallback: proxy probe (`GET /healthz` within 2 s) fails → run plain `codex` with the user's args and print one line why. App-server or `--remote` failure (spike 1a negative) → same fallback plus the hook line.                                                                                                                                                                                                  | Research §3.8: a dead proxy blocks the session; never start Codex against a dead listener.                                                                                                                                                                                                                               |
| D14 | Deps: `bubbletea` + `lipgloss` (TUI), `coder/websocket` (D9). Nothing else.                                                                                                                                                                                                                                                                                                                                                 | Task mandate.                                                                                                                                                                                                                                                                                                            |

## Package map

```
cmd/codex-router        subcommands: (default) launch · tui · hook · proxy · version
internal/launcher       runtime dir, proxy start+probe, app-server child, codex --remote exec, fallback, wiring (Sink fan-out)
internal/proxy          HTTP server 127.0.0.1: /v1/responses, /v1/models, /healthz; upstream client; SSE pass-through; header hygiene
internal/route          Engine: thread/turn state, decisions, modes, stats; orchestrates facts+classifier+policy+cost+catalog
internal/appserver      JSON-RPC client to the private app-server (initialize experimentalApi, thread/settings/update, notifications)
internal/control        0600 unix socket; Snapshot push, Command handling; Client for tui/hook
internal/tui            Bubble Tea app: tabs Now/Routes/Policy/Classifier/Usage; Render(snapshot) for docs images
internal/hook           `codex-router hook`: hook stdin → control snapshot → systemMessage on switch
internal/classifier     Advice contract, adapters (systemone, ollama), Client (deadline, 2 attempts, breaker 3/60 s)
internal/policy         pure Decide: votes, hysteresis, escalation, verification gate, FitTier
internal/cost           cache ledger per thread×model, switch tax in tokens, optional USD
internal/facts          Responses input items → Facts (prompt, dialogue, failure signature, observed activity, verified)
internal/responses      request body parse/rewrite (model, reasoning.effort), turn metadata, Kind, usage from SSE
internal/catalog        /models body → Catalog; compatibility Group; effective window
internal/config         types, defaults, presets, Load/Path
```

Dependency direction (arrows = imports; no cycles, no package imports a higher row):

```
cmd → launcher → {proxy, route, appserver, control, tui, hook, config}
tui, hook → control → route (types only) → {facts, classifier, policy, cost, catalog, responses, config}
proxy → {responses, catalog}         policy → {classifier, config}     facts → {responses, config}
classifier, catalog, responses → config          appserver → config      cost → (stdlib)
```

`proxy` never imports `route`: it declares `Decider`/`Observer` and `launcher` passes the engine.
`classifier.Input` carries prompt + dialogue only, so auth headers cannot reach an adapter by
construction. Logging: `log/slog`, facts and IDs only; no header values, no body text.

## Cross-package interfaces

The skeleton in `internal/*` is the contract; task owners add logic and unexported types but do
not change exported signatures without updating this section. Key signatures:

```go
// config
type Tier string; type Activity string; type Effort string; type Preset string
type Route struct{ Model string; Effort Effort }
type Config struct{ Baseline Tier; Routes map[Tier]Route; Activities map[Activity]map[Tier]Route; Prices map[string]Price;
    Policy Policy; Preset Preset; Classifier string; Classifiers map[string]Classifier; Context Context; Cache Cache;
    Proxy Proxy; Passthrough []string; CodexBin string }
func Default() Config; func Path() (string, error); func Load(path string) (Config, error); func (c Config) Validate() error
func (c Config) Effective() Policy            // Policy with the Preset applied
func (c Config) RouteFor(t Tier, a Activity) Route

// catalog
type Group struct{ CompHash string; Lite bool; ToolMode string }
type Catalog struct{ Models map[string]Model; ETag string; FetchedAt time.Time }
type Source interface{ Current() Catalog }
func Parse(body []byte) (Catalog, error); func LoadCache(path string) (Catalog, error)   // $CODEX_HOME/models_cache.json, read-only
func (c Catalog) GroupOf(slug string) (Group, bool); func (c Catalog) Compatible(a, b string) bool
func (c Catalog) Window(slug string) (int, bool)   // context_window × effective_context_window_percent / 100
type Store struct{...}; func (s *Store) Current() Catalog; func (s *Store) Set(Catalog)

// responses
type Kind string (KindUserStep, KindPassthrough); type Meta struct{ SessionID, ThreadID, TurnID, AgentName, Subagent, WindowID string }
type Request struct{ Model string; Effort config.Effort; Input []Item; Bytes int; ... }
type Rewrite struct{ Model string; Effort config.Effort }; type Usage struct{ InputTokens, CachedInputTokens, OutputTokens, ReasoningTokens, TotalTokens int }
func ParseMeta(h http.Header) Meta; func Parse(body []byte) (*Request, error)
func (r *Request) Apply(rw Rewrite) ([]byte, error)      // re-encodes only model + reasoning.effort; every other byte unchanged
func (r *Request) Fingerprint(m Meta) string             // sha256(turn_id, len(input), last item type+call_id, model)
func Classify(r *Request, m Meta, passthrough []string) Kind
func UsageFromSSE(event []byte) (Usage, string, bool)     // response.completed → usage, served model

// facts
type Facts struct{ Prompt string; Dialogue []Turn; Continuation bool; Failure *Failure; Observed Observed; Verified bool; ApproxTokens, Items int }
func Extract(req *responses.Request, opts Options) Facts

// classifier
type Asker interface{ Ask(ctx context.Context, in Input) (Advice, error); Health() Health }
type Adapter interface{ Build(model string, in Input) ([]byte, error); Parse(body []byte) (Advice, error) }
func New(cfg config.Classifier, key string, adapter Adapter, doer Doer, now func() time.Time) *Client   // implements Asker
func SystemOne() Adapter; func Ollama() Adapter

// policy
func Decide(in Input) Result                      // pure; state in, state out
func FitTier(tier config.Tier, tokens int, window func(config.Tier) (int, bool), fill float64) (config.Tier, bool)

// cost
type Ledger struct{...}; func NewLedger(c Cache) *Ledger
func (l *Ledger) Observe(thread, model string, u responses.Usage, at time.Time)
func (l *Ledger) Warm(thread, model string, now time.Time) bool
func (l *Ledger) SwitchTax(thread, candidate string, tokens int, now time.Time) int   // uncached tokens the next request writes
func USD(p config.Price, u responses.Usage) float64

// route
type Engine struct{...}; func New(d Deps) *Engine
func (e *Engine) Decide(ctx context.Context, req *responses.Request, m responses.Meta) (responses.Rewrite, bool)  // proxy.Decider
func (e *Engine) Observe(m responses.Meta, served string, u responses.Usage, status int)                         // proxy.Observer
func (e *Engine) Apply(c Command) error      // pin/unpin/pause/resume/preset; control.Handler
func (e *Engine) SettingsUpdated(threadID string, r config.Route)   // from appserver; echo vs manual pin
func (e *Engine) Snapshot() Snapshot

// proxy
type Decider interface{ Decide(ctx, *responses.Request, responses.Meta) (responses.Rewrite, bool) }
type Observer interface{ Observe(responses.Meta, string, responses.Usage, int) }
func New(o Options) *Server; func (s *Server) Listen() (int, error); func (s *Server) Serve(ctx context.Context) error

// appserver
type API interface{ Initialize(ctx) error; UpdateSettings(ctx, threadID string, s Settings) error; Updates() <-chan SettingsUpdated; Close() error }
type Dialer interface{ Dial(ctx, socketPath string) (Conn, error) }
func Dial(ctx, socketPath string, d Dialer) (API, error); func WebSocket() Dialer

// control
type Handler interface{ Apply(Command) error; Snapshot() route.Snapshot }
func Listen(path string, h Handler) (*Server, error); func (s *Server) Publish(route.Snapshot)
func Dial(path string) (*Client, error); func (c *Client) Updates() <-chan route.Snapshot; func (c *Client) Send(Command) error

// tui / hook / launcher
func tui.Run(ctx, Options) error; func tui.Render(s route.Snapshot, tab Tab, width int) string
func hook.Run(stdin io.Reader, stdout io.Writer, socket string) error
func launcher.Run(ctx, Options) error
```

## Data flow per request

```
Codex app-server ─POST /v1/responses─▶ proxy
  1 proxy: path allow-list; copy body (cap 32 MB); responses.ParseMeta(headers); responses.Parse(body)
    parse error → forward original (fail open), log "parse-error thread=… turn=…"
  2 proxy → engine.Decide(ctx, req, meta) with ctx deadline = classifier timeout + 200 ms
    engine: Kind != UserStep → (∅,false)            mode paused/manual for thread → (∅,false)
            key = (thread, turn, fingerprint); hit → pinned rewrite
            catalog.GroupOf(req.Model) unknown → (∅,false)
            first step of turn (turn_id changed): facts.Extract → classifier.Ask (blocking, deadline) → advice
            later steps: facts.Extract (cheap) → observed move / pending review answer
            policy.Decide(votes, hysteresis, escalation on facts.Failure, verification gate, tax = ledger.SwitchTax)
            tier×activity → cfg.RouteFor → catalog.Compatible(req.Model, route.Model) else original
            FitTier via catalog.Window; upward fix if req.Model itself does not fit
            pin Decision under key; Sink.OnDecision (→ appserver.UpdateSettings, control.Publish)
    any panic/error → recover, log reason, (∅,false)
  3 proxy: rewritten body or original; upstream POST https://chatgpt.com/backend-api/codex/responses
    headers copied verbatim incl. Authorization, ChatGPT-Account-ID (never read, never logged); no redirects (CheckRedirect → error)
  4 stream: status + headers minus Openai-Model/X-Openai-Model → client; body io.Copy with flush per SSE chunk;
    tee: parse `response.completed` → engine.Observe(meta, served, usage, status) (cache ledger, stats, strip)
    client gone or upstream error after first byte → close both sides, no re-send, log "stream-aborted stage=…"
    upstream dial/5xx before first byte → return status as-is (Codex retries; same fingerprint → same decision)
Codex ─GET /v1/models?…─▶ proxy ─▶ upstream verbatim (incl. If-None-Match); 200 body → catalog.Store.Set; passthrough bytes + ETag
```

## Test strategy

- Fakes (boundaries only): `httptest.Server` upstream that scripts SSE (`response.created`, N
  `output_item`, `response.completed` with usage; modes: 200, 400, 500, hang, close-after-first-item,
  echo-model-header); fake app-server (`net.Listen("unix")` + the same `Dialer` interface, scripted
  `initialize`/`thread/settings/update` replies and injected `thread/settings/updated`); fake
  classifier (`Asker` returning scripted advice/errors); fake clock `func() time.Time`.
- Unit, table-driven: config validation; catalog groups/window; responses parse/rewrite byte
  identity (golden: input bytes minus `model`/`effort` == output bytes minus the same); Kind
  classification; facts (failure signature stability, observed activity, verifies()); policy decide
  matrix (reasons × votes × holds × taxes); FitTier; cost warm/tax; classifier adapters (fixture
  bodies from claude-model-router `test/fixtures`, ported); control codec; hook output.
- Proxy fault tests (in-process, `httptest`): upstream dead → 502 to client, no retry; upstream
  closes after first item → client sees partial stream + EOF, exactly one upstream POST; client
  disconnect mid-stream → upstream request cancelled; `Openai-Model` stripped; redirect refused;
  path outside allow-list → 404; non-loopback bind refused; Authorization never appears in logs
  (log sink assertion on every test).
- Route engine tests: same fingerprint twice → one classifier call, same rewrite; passthrough
  kinds never call classifier; out-of-group model → no rewrite; manual pin after foreign
  settings/updated; pause; pin next turn consumed once; classifier timeout → original; review
  every 4 requests applied at next step; escalation after repeated failure signature.
- E2E (`internal/launcher` test): real proxy + fake upstream + fake app-server + engine; drive 5
  requests of a 3-tool turn through `http.Client`; assert footer writes and control snapshots.
- `make all` (fmt-check, lint, test -race, build) is the gate for every task.

## Stage 1: spikes (user runs them with `! bash scripts/spikes/<name>.sh`)

Rules: `-c` overrides only; `--no-daemon`, `--ephemeral`, `-s read-only`; throwaway `mktemp -d`
runtime; scripts print header **names** only and redact text; never `~/.codex/config.toml`.
Tooling: `scripts/spikes/shape-proxy.py` (Python stdlib `http.server` + `urllib`, ~120 lines:
forwards to the backend, logs header names, item types, metadata keys, usage; optional
`--rewrite model=…,effort=…`, `--fault none|before-bind|before-upstream|after-first-item`,
`--record shapes.jsonl`), `scripts/spikes/appserver-client.py` (run with
`uv run --with websockets`; tries WS handshake on the unix socket, falls back to newline JSON-RPC).

### 1a: two clients on one unix app-server, TUI via `--remote`, footer mid-turn, parity

Proves [peer]: WS-over-unix transport; `experimentalApi` gate; no owner check on
`thread/settings/update`; and the three §4.1 "not verified" items.
Script: `1a-two-clients.sh` starts `codex app-server --listen unix://$RT/app.sock` (no provider
override; default model), prints the exact `codex --remote unix://$RT/app.sock` command for the
user's second pane, then runs `appserver-client.py` which: initializes with `experimentalApi`,
logs notification method names, waits for `turn/started`, 3 s later sends
`thread/settings/update {model: gpt-6-luna, effort: low}`, logs `thread/settings/updated`, then
waits for the user to run `/model gpt-6-sol` in the TUI and logs that notification too.
User checklist printed by the script: footer repaints mid-turn (model-with-reasoning +
context-window-size), `/model`, `/status`, approvals prompt, shell tool, image paste, Esc
interrupt, `/compact` all work under `--remote`.
Evidence: `$RT/1a.log` (method names, model/effort values, timings) + the user's yes/no per item.
If it fails: transport not WS → `appserver.Dialer` JSON-lines impl (same contract). No repaint
mid-turn but repaint at next turn → keep settings write, add hook line + OSC 2 title fallback
(`tui.terminal_title = []` via `-c`; its effect is the first assertion of that fallback). `--remote`
missing features → launcher runs `codex -p`-less embedded mode: proxy only, no footer, hook line
(research §4.1 fallbacks).

### 1b: proxy fault injection

Proves [peer]: `unbounded_connection_retries` behaviour against a dead listener; Codex retry on
5xx/connection reset; recovery after restart on the same port.
Script: `1b-faults.sh` runs `codex --no-daemon exec --ephemeral -s read-only -c model_provider=router
-c 'model_providers.router={...base_url="http://127.0.0.1:47911/v1"...}'` with a 3-tool prompt, four
times: (1) proxy never bound: measure how long `codex exec` hangs, whether it exits, exit code;
(2) `--fault before-upstream` (502 before upstream): count retries and spacing, then restart the
proxy in `none` mode after 10 s and see whether the turn completes; (3) `--fault
after-first-item` (close after first `output_item`): does Codex retry the step, with the same
body (compare fingerprints in the record), does the turn complete; (4) kill -9 the proxy mid-turn,
restart on the same port after 5 s.
Evidence: `$RT/1b-<case>.log` (proxy timestamps + request fingerprints), codex stderr, wall time.
If it fails: (1) hangs forever → launcher must also watchdog the proxy and, on 3 failed probes,
restart the listener in-process and print a warning; (3) Codex re-sends after partial output →
engine treats a repeat fingerprint after partial output as the same decision (already the design)
and proxy must never buffer; nothing else changes.

### 1c: request shapes (compaction, memory, guardian, subagent, tool error) and post-compaction switch

Proves: D5 discriminators; [peer] "remote v2 posts compaction metadata to /responses";
[unverified] "long thread crossing auto-compaction then switching models"; the
`function_call_output` error shape for the failure signature.
Script: `1c-shapes.sh` with `shape-proxy.py --record`: (1) short task (baseline shapes);
(2) long thread with `-c model_context_window=40000 -c model_auto_compact_token_limit=30000`
(forces compaction early), local compaction, proxy rewrites to `gpt-6-luna/low` **after** the
first request whose item count drops (post-compaction) → expect 200; (3) same with
`-c 'model_providers.router.capabilities.remote_compaction="v2"'` to record the remote shape;
(4) a prompt that runs a failing command (`false; exit 1` style) to record the error item;
(5) a prompt that spawns a subagent (`spawn_agent` tool) if available, to record `x-openai-subagent`.
Record per request: header names, turn-metadata keys and whether `turn_id` is present, item type
sequence, first 60 chars of the last user text, body top-level keys, status, usage.
Evidence: `$RT/shapes.jsonl` and a one-screen summary printed by the script.
If it fails: no discriminator for compaction → keep D5 prefix rule and add
`model_auto_compact_token_limit` guidance; post-compaction switch 400 → router disables routing
for a thread for the rest of the session after any item-count drop (`compacted` flag) until the
next turn; subagent header absent → subagent requests look like user steps and route normally
(acceptable: same group).

## Stage 1 outcome (2026-10-13)

All three spikes ran; results in `docs/research.md` §9. Deltas applied to the plan:
D5 becomes "route only `request_kind == "turn"` without `x-openai-subagent`"; the app-server
client subscribes with `thread/resume {threadId, excludeTurns: true}` before settings writes;
the launcher starts the app-server in the user's cwd; local compaction stays (D4). Task T3b
(responses classification) and the `Resume` addition to `appserver` run in wave 3 with T13.

## Stage 2: implementation waves

Rules: one task = one owner per listed path; no two tasks in one wave touch the same file;
`go.mod`/`go.sum` edited only by T6 (wave 1) and T11 (wave 2). Every task: `make all` green,
tests listed below exist and pass, no `TODO` in place of listed work, `slog` lines never include
header values or text. Models: opus = core, sonnet = UI/tests/docs/scripts, haiku = checklists.

### Wave 0 (done by this plan): skeleton in `internal/*` compiles; `scripts/spikes/` scripts (T0, sonnet)

T0 **spike scripts** · owns `scripts/spikes/*` · deps none · sonnet · AC: three scripts + two helpers as
in Stage 1; `shellcheck` clean; `--help` prints the run order; secrets never printed (grep test
in `scripts/spikes/selftest.sh` against a fake request with an Authorization header).

### Wave 1 (leaf packages; 8 tasks)

| id  | package(s) owned                                           | deps                | model  | acceptance                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                       |
| --- | ---------------------------------------------------------- | ------------------- | ------ | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| T1  | `internal/config`                                          | –                   | sonnet | `Default()` equals D2; `Load` rejects unknown keys, bad tiers/efforts/empty routes with field path in error; `Effective()` applies presets thrifty/balanced/quality; `RouteFor` inherits base route per field; `Path()` honours `XDG_CONFIG_HOME`. Tests: table over 12 bad configs; golden default JSON round-trip.                                                                                                                                                                                                                                                                                                                                                                                             |
| T2  | `internal/catalog`                                         | –                   | sonnet | `Parse` of a fixture made from `models_cache.json` field names (slug, context_window, max_context_window, effective_context_window_percent, comp_hash, use_responses_lite, tool_mode (missing → "none"), supported_reasoning_levels[].effort, visibility); `GroupOf`, `Compatible` (gpt-6.1-sol↔gpt-6-luna true, ↔gpt-5.5 false), `Window` (500000×95%); `Store` is goroutine-safe (race test); `LoadCache` tolerates a missing file.                                                                                                                                                                                                                                                                            |
| T3  | `internal/responses`                                       | –                   | opus   | `Parse` keeps unknown fields; `Apply` changes only `model` and `reasoning.effort` (golden byte test on the 99 KB-shaped fixture; `reasoning` absent → added with effort only); `ParseMeta` from `x-codex-turn-metadata` JSON; `Classify` per D5 (table: subagent header, missing turn_id, prefix match, user step); `Fingerprint` stable across retries and different across steps; `UsageFromSSE` parses `response.completed` and ignores other events; items decode `message`(input_text/output_text), `function_call`, `function_call_output`, `custom_tool_call(_output)`, `local_shell_call`, `reasoning` with unknown types kept opaque.                                                                   |
| T4  | `internal/classifier`                                      | –                   | opus   | Port contract text (criteria, instructions) and adapters `systemone`, `ollama` from `lib/classifier-contract.mjs`, `classifier-apis.mjs` (fixtures ported); `Client`: total deadline, 2 attempts on transient status with Retry-After within budget, 401/403 → `ErrAuth`, 3 failures → paused 60 s (fake clock), busy slot, `MAX_ANSWER_CHARS` 16 KiB, Ollama activity second request only with ≥300 ms left; `Health()` snapshot; request body never contains anything but `Input` fields (test asserts no "authorization"/"chatgpt" substrings in outbound body/headers except the classifier's own bearer).                                                                                                   |
| T5  | `internal/policy` + `internal/cost`                        | –                   | opus   | Port `policy.mjs` `decide` with taxes in tokens: reasons `escalation, hold, no-advice, uncertain, same-tier, upgrade, jump, upgrade-pending, downgrade, downgrade-held, downgrade-pending`; `massAbove/massAtOrBelow`; `fresh` single-vote; `FitTier` fallback order; table of ≥25 cases. `cost`: warm within TTL−margin, cold after; `SwitchTax` = tokens − warm prefix on candidate (0 when warm); `USD` zero when unpriced.                                                                                                                                                                                                                                                                                   |
| T6  | `internal/appserver` + `go.mod`/`go.sum` (coder/websocket) | –                   | opus   | `Dial` + `Initialize` sends `initialize` with `capabilities.experimentalApi=true` and reads the result; `UpdateSettings` sends `thread/settings/update {threadId, model, effort}` and returns RPC errors as Go errors; `Updates()` yields `thread/settings/updated`; ids are per-request; request/notification multiplexing under a fake unix WS server (`net.Listen("unix")` + `websocket.Accept`); context cancel closes cleanly; a JSON-lines `Dialer` behind the same tests (fallback for 1a).                                                                                                                                                                                                               |
| T7  | `internal/control`                                         | –                   | sonnet | `Listen` creates the socket with 0600 (test stats the file), rejects an existing live socket, removes a stale one; snapshot on connect; `Publish` coalesces; `Send` round-trips `Command`, bad op → error message; multiple clients; client reconnect on EOF.                                                                                                                                                                                                                                                                                                                                                                                                                                                    |
| T8  | `internal/facts`                                           | T3 types (skeleton) | opus   | Port `facts.mjs`: prompt = last user text, dialogue last N clipped to `maxTextChars` with head+tail clip; `Continuation` when last item is a tool output; failure signature: FNV-1a over normalized `function_call_output` text (ANSI stripped, line endings, stack positions, durations), repeated with an `apply_patch`/write between (window 40 items); `Observed` from tool names (`apply_patch` path → code/docs; `shell`/`exec_command`/`local_shell_call` non-read → ops; else read/talk); `Verified` via ported `verifies()` (go test, npm test, make lint, uv run pytest, …) and exit status in output; `ApproxTokens = max(len/4, …)`. Tables for clip, signature normalization, readsOnly, runsCheck. |

### Wave 2 (composition; 5 tasks)

| id  | owned                                                    | deps              | model  | acceptance                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                             |
| --- | -------------------------------------------------------- | ----------------- | ------ | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| T9  | `internal/proxy`                                         | T2, T3            | opus   | Binds loopback only (non-loopback addr → error); allow-list `/v1/responses` POST, `/v1/models` GET, `/healthz`; upstream fixed to `proxy.Upstream` unless `Options.Upstream` (tests only); `CheckRedirect` refuses; streams SSE with flush per chunk; strips `Openai-Model`/`X-Openai-Model`; tee `response.completed` → `Observer`; catalog update on `/models` 200; all fault tests in Test strategy; body cap 32 MB → 413; decision ctx deadline; `Decider` panic recovered; log assertions (no auth values ever).                                                                                                                                                                                                                  |
| T10 | `internal/route`                                         | T1,T2,T3,T4,T5,T8 | opus   | Engine per Data flow: state keyed `(thread, turn, fingerprint)` with LRU 256 threads; pin-for-retry; first-step classify blocking with deadline; background review every 4 requests applied next step; observed move; escalation on `facts.Failure`; verification gate + `minDwellRequests` for downgrades on the same task; group check and FitTier incl. upward fix (D6); passthrough kinds untouched; modes: on/paused/manual per D12, pin next turn consumed once; `Snapshot` with Stats (replies, switches, strip ≤48 cells merging pairs, by tier/activity, last switch, cache reuse %, uncached switch tokens, USD when priced); `Sink` called outside locks; fail-open on any error (test: fake classifier panics → original). |
| T11 | `internal/tui` + `go.mod`/`go.sum` (bubbletea, lipgloss) | T7, T10 types     | sonnet | Tabs 1-5 per ui-selected.html; Now = next/this turn, activity line, why + support, pin buttons with %, SESSION strip/counts/last switch/saved, TODAY (when present), WORTH A LOOK, Codex footer line, context + cache rows; keys `1-5 p space q`, preset cycling on Policy; `Render(snapshot, tab, width)` pure (golden tests at 80 and 120 cols); reconnect loop on socket loss with "disconnected" header; no colors leak into golden plain mode.                                                                                                                                                                                                                                                                                    |
| T12 | `internal/hook`                                          | T7, T10 types     | sonnet | Reads hook JSON (`session_id`, `turn_id`, `hook_event_name`); socket from `CODEX_ROUTER_CONTROL`; prints `{"systemMessage":"router: <tier> · <activity> → <model> · <effort> · <why>"}` only when the turn's route differs from the previous turn's, else `{}`; never `additionalContext`; no socket → `{}` exit 0; ≤ 200 ms.                                                                                                                                                                                                                                                                                                                                                                                                          |
| T13 | `internal/launcher`                                      | T6,T7,T9,T10      | opus   | Runtime dir per D7 (0700, cleaned on exit); proxy `Listen` then `/healthz` probe ≤ 2 s; writes `proxy.port`; child `codex app-server --listen unix://… -c model_provider=router -c 'model_providers.router={…requires_openai_auth=true}'` with `CODEX_ROUTER_CONTROL` env; waits for the socket; `appserver.Dial` + `Initialize`; wires Sink → `UpdateSettings` (dedupe equal writes) + `control.Publish`, `Updates()` → `engine.SettingsUpdated`; execs `codex --remote unix://… <user args>` and forwards signals; on any startup failure → plain `codex <user args>` + one stderr line; E2E test with fakes (no real codex: `CodexBin` = a test script).                                                                            |

### Wave 3 (integration + verification; 4 tasks)

| id  | owned                                                                              | deps        | model  | acceptance                                                                                                                                                                                                                                                                                                                                                                                                                             |
| --- | ---------------------------------------------------------------------------------- | ----------- | ------ | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| T14 | `cmd/codex-router`                                                                 | T11,T12,T13 | opus   | Subcommands: default → `launcher.Run` (pass-through args to codex), `tui [--socket]` (default: newest runtime dir), `hook`, `proxy [--port] [--config]` (standalone for 1b-style debugging, decisions on, no app-server), `version`; `--config` flag + env `CODEX_ROUTER_CONFIG`; classifier key from `keyEnv`; exit codes documented in `--help`; smoke test runs `version` and `proxy --port 0` + healthz.                           |
| T15 | `internal/launcher/e2e_test.go`, `internal/proxy/testdata/`                        | T14         | sonnet | Scenario test: fake upstream + fake app-server + real engine/proxy/control: 3-tool turn (5 requests) → exactly 1 classifier call, decisions pinned per step, 1 settings write per route change, control snapshot stats = 5 replies; manual `/model` injection → manual mode, next request unchanged; pause → unchanged; classifier down → unchanged, health paused after 3; upstream close after first item → no second upstream POST. |
| T16 | `docs/user-guide.md`, `docs/architecture.md`, `README.md`, `docs/configuration.md` | T14         | sonnet | Install, launch, TUI keys, config reference (every field, default, D6 `model_context_window` note), security model (what the proxy sees, what leaves the machine), failure behaviour, hook setup, spikes' results folded into `docs/research.md` §7.                                                                                                                                                                                   |
| T17 | `docs/plans/2026-10-13-live-checklist.md`                                          | T15         | haiku  | Stage 3 checklist rendered as a form with evidence columns, from this plan.                                                                                                                                                                                                                                                                                                                                                            |

## Stage 3: live test checklist (user runs; evidence captured in `docs/evidence/<date>/`)

| check                | steps                                                                                    | pass                                                                                           | evidence                                                                     |
| -------------------- | ---------------------------------------------------------------------------------------- | ---------------------------------------------------------------------------------------------- | ---------------------------------------------------------------------------- |
| Launch               | `codex-router` in a repo; `codex-router tui` in a split                                  | TUI shows "footer synced", Codex footer shows configured model                                 | `tui.txt` (snapshot dump via `codex-router tui --dump`), terminal screenshot |
| Footer follows route | prompt "fix the flaky retry test…" (debug)                                               | footer changes to `gpt-6-sol high` within the turn; TUI Now "This turn" matches                | screenshot before/after, `$RT/router.log` decision lines (ids + facts only)  |
| Per-step rewrite     | same turn                                                                                | router log shows 1 classifier call, N steps with pinned decision; upstream 200 on each         | `router.log`                                                                 |
| Manual `/model`      | run `/model gpt-6-luna`                                                                  | TUI shows `◐ manual`; next prompt forwarded unchanged (log `mode=manual`)                      | `router.log`, screenshot                                                     |
| Resume               | TUI **resume**                                                                           | routing resumes next turn                                                                      | `router.log`                                                                 |
| Pin next             | TUI `p` → high                                                                           | next turn on high route, then prior behaviour                                                  | `router.log`                                                                 |
| Pause                | TUI space                                                                                | every request unchanged, footer back to configured model                                       | `router.log`                                                                 |
| Hook line            | with hook configured, a switch prints `↳ Hook · router: …` and no line on a non-switch   | screenshot                                                                                     |
| Kill proxy           | `kill -9` launcher's proxy? n/a (in-process) → instead kill `codex-router` during a turn | Codex TUI shows reconnect/err, user restarts `codex-router`; thread resumes via `codex resume` | notes                                                                        |
| Escalation           | make a test fail twice with an edit between                                              | route goes up with reason `escalation`, holds 2 turns                                          | `router.log`                                                                 |
| Context guard        | long session (>220k tokens)                                                              | no downgrade below the fit; `fit` reason visible in TUI Why                                    | `router.log`                                                                 |
| Compaction           | let auto-compaction run                                                                  | request passed through (kind=passthrough), no decision, thread continues                       | `router.log`                                                                 |
| Classifier down      | unset key / block host                                                                   | every request forwarded, TUI Classifier tab shows paused                                       | `router.log`, screenshot                                                     |

## Stage 4: docs + screenshot pipeline

- `scripts/render-docs/main.go` (`go run ./scripts/render-docs`): builds fixture `route.Snapshot`s
  (sample session matching ui-selected.html), calls `tui.Render(snapshot, tab, 100)` and writes
  `docs/images/router-tui-<tab>.ansi`; then `freeze --language ansi` (charm `freeze`, dev tool,
  not a module dep; `make docs-images` checks it is installed) produces `docs/images/*.svg`
  (+PNG). Golden `.ansi` files are committed so docs diffs show in review without freeze.
- `docs/user-guide.md` embeds the SVGs with alt text as claude-model-router does; `make
docs-images` is part of `make all`'s docs target but not of `lint`.

## Stage 5: review

1. `revmux` or `/code-review high` on the whole branch; security pass on `proxy`, `launcher`,
   `classifier` (auth never leaves the upstream path; socket modes; redirects; log lines).
2. Architecture check: `go list -deps` per package matches the dependency direction above
   (a test in `internal/arch_test.go` asserts it, so it stays true).
3. Fold spike + live evidence into `docs/research.md` §7 and flip the [peer]/[unverified] marks.

## Proof map for [peer]/[unverified] items

| research item                                                                    | proven by                                     |
| -------------------------------------------------------------------------------- | --------------------------------------------- |
| WS over unix app-server transport (§3.6 [peer])                                  | spike 1a handshake; T6 fallback Dialer if not |
| `experimentalApi` gate, no owner check on `thread/settings/update` (§3.6 [peer]) | spike 1a                                      |
| footer repaint mid-turn, two clients, `--remote` parity (§4.1)                   | spike 1a + Stage 3                            |
| unbounded reconnects against a dead listener (§3.8 [peer])                       | spike 1b                                      |
| remote v2 compaction shape (§3.8 [peer]), post-compaction switch (§7)            | spike 1c                                      |
| `openai-model` header from backend (§7)                                          | irrelevant: T9 strips unconditionally         |
| `tui.terminal_title = []` (§7)                                                   | only if 1a fallback is needed                 |
| hook `systemMessage` rendering (§4 [peer])                                       | Stage 3 "Hook line"                           |
| subagent threads (§7)                                                            | 1c records; D5 passes them through            |

## Definition of done

- `make all` green on `main` after merge; `go test -race` coverage ≥ 80 % on `internal/{proxy,route,policy,responses,facts}`.
- Spikes 1a-1c run with evidence files; plan deltas applied and recorded in `docs/research.md`.
- Stage 3 checklist complete with evidence; any failed row has an issue in `docs/backlog/`.
- Docs (T16) and images (Stage 4) committed; `codex-router --help` matches docs.
- Security invariants hold by test: loopback only, allow-list, no redirects, auth never logged or
  sent to classifier/control/TUI, control socket 0600, no body text in logs.
- No edits to `~/.codex/config.toml`, `~/.codex-work`, or the shared daemon by any code path
  (grep test: no writes under `$CODEX_HOME` except reading `models_cache.json`).
