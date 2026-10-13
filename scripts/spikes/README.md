# Stage 1 spikes

Throwaway scripts that prove the open items in `docs/plans/2026-10-13-codex-router-impl.md`
(Stage 1). The agent sandbox cannot run `codex`; run each script yourself from the repo root:

```sh
! bash scripts/spikes/selftest.sh        # 1. offline tooling check, no codex, no network
! bash scripts/spikes/1a-two-clients.sh  # 2. interactive; needs a TTY, uv, codex
! bash scripts/spikes/1b-faults.sh       # 3. about 10 minutes worst case
! bash scripts/spikes/1c-shapes.sh       # 4. five codex runs
```

Each script prints a short summary and ends with `evidence: <dir>` (a `mktemp -d` directory,
mode 0700). Every script accepts `-h`/`--help`.

## Rules the scripts follow

- Only `-c` overrides: `codex --no-daemon exec --ephemeral -s read-only` in a throwaway git repo
  inside the evidence dir. `~/.codex/config.toml`, `~/.codex-work` and the shared daemon are not
  touched. Codex still writes its own state under `~/.codex`; that is why the sandbox cannot run it.
- Provider: `-c model_provider=router -c 'model_providers.router={name="router", base_url="http://127.0.0.1:PORT/v1", wire_api="responses", requires_openai_auth=true}'`.
- Default model `gpt-6-luna`, effort `low` (override with `SPIKE_MODEL`, `SPIKE_EFFORT`).
- Every codex run has a wall-clock limit (perl process-group watchdog; macOS has no `timeout`).
- Header values are never printed or recorded, only names. Request text is cut to the first
  60 characters of the last user text (and of the last tool output).

## Files

| File                                | Role                                                                                                                                                                                                               |
| ----------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| `shape-proxy.py`                    | Recording Responses proxy (stdlib). `--rewrite model=..,effort=..`, `--rewrite-after-drop`, `--fault none\|before-bind\|before-upstream\|after-first-item`, `--fault-count N`, `--record FILE`, `--summarize FILE` |
| `appserver-client.py`               | Second app-server client (`uv run --with websockets`): WebSocket over the unix socket, newline JSON-RPC fallback                                                                                                   |
| `1a-two-clients.sh`                 | Two clients on one unix app-server, `codex --remote` TUI, footer mid-turn, parity checklist                                                                                                                        |
| `1b-faults.sh`                      | Four proxy fault cases on port 47911                                                                                                                                                                               |
| `1c-shapes.sh`                      | Five request-shape cases, post-compaction switch verdict                                                                                                                                                           |
| `selftest.sh`, `selftest-driver.py` | Offline checks: `bash -n`, shellcheck, fake upstream, redaction, secret grep                                                                                                                                       |
| `lib.sh`                            | Shared helpers (not run directly)                                                                                                                                                                                  |

## 1a: two clients, footer mid-turn, parity

Starts `codex app-server --listen unix://$RT/app.sock` (default model, no provider override) and
the second client. With `agtermctl` on PATH and `AGTERM_SESSION_ID` set, it opens a split in your
session, types `codex --remote unix://$RT/app.sock`, waits for you to answer any trust prompt, then
types a prompt that runs a ~20 s four-step shell task. The client sends
`thread/settings/update {model: gpt-6-luna, effort: low}` 3 s after `turn/started`. The script
saves the split screen before (`1a-before.txt`) and about 3 s after (`1a-after.txt`) the update.
Without agterm it prints the commands for a second terminal (manual mode).

Then it asks you to run `/model gpt-6-sol` in the TUI, and asks y/n/s for the parity checklist
(footer mid-turn, `/model`, `/status`, approvals, shell tool, image paste, Esc interrupt,
`/compact`) plus "footer repainted at the next turn". Answers go to `1a-checklist.txt`.

Evidence: `1a.log` (transport, method names, model/effort values, timings), `appserver.log`,
`client.out`, `1a-checklist.txt`, the two screen captures. Child processes and the split are
cleaned up on exit.

Client protocol notes (unverified until this spike runs): it lists loaded threads with
`thread/loaded/list`, joins with `thread/resume`, sends no `jsonrpc` field, and does not answer
server-initiated requests (approvals) so the TUI keeps ownership of them.

## 1b: proxy faults

`CASES='1 2 3 4'`, `PORT=47911`, `LIMIT=150`, `NO_PROXY_LIMIT=90`.

1. Proxy never bound: wall time and exit code of `codex exec`.
2. `--fault before-upstream` (502); the proxy restarts in normal mode after 10 s.
3. `--fault after-first-item --fault-count 1`: connection closed after the first output item, once.
4. `kill -9` the proxy after the first recorded request, restart on the same port after 5 s.

Per case: exit code, wall time, one line per request (time, status, item count, `sha256` prefix of
the body) and `distinct_bodies`. Same hash on a retry means Codex re-sent the same body.

## 1c: request shapes

`CASES='1 2 3 4 5'`, `LIMIT=240`. Context window 60000, compaction limit 45000; the repo holds
eight 32 KB files that the long prompt reads one per tool call.

1. baseline: three tool calls.
2. local compaction on `gpt-6-sol`; the proxy switches to `gpt-6-luna/low` after the first request
   whose item count drops. The script prints item counts and the status of switched requests.
3. remote compaction (`capabilities.remote_compaction="v2"`).
4. failing command: the `function_call_output` error shape.
5. subagent: `x-openai-subagent` header, if the `spawn_agent` tool exists.

`shapes.jsonl` has, per request: header names, turn-metadata keys, `turn_id_present`, item type
sequence, body keys, compaction hints (header, body key or item type containing "compact"), first
60 characters of the last user text, status, usage. Print it again with
`python3 scripts/spikes/shape-proxy.py --summarize <dir>/shapes.jsonl`.

## Tooling limits

- The WebSocket path of `appserver-client.py` is not covered by `selftest.sh` (no `websockets`
  offline); the newline JSON-RPC fallback is.
- `shape-proxy.py` buffers nothing but SSE blocks (split on a blank line); it assumes `\n\n`.
