# Stage 2 gate, 2026-10-14

**Result: G1-G5 all pass.** Codex 0.162.1, test home A only (`~/.cache/codex-router-live/home-a`).
Stage 2 went ahead. Socket activation on macOS stays on purego, with no cgo fallback.

- The real `~/.codex/config.toml` and `~/.codex-work/config.toml` sha256 were unchanged before and
  after (`c409e1eb…1486`).
- Every process ran with a temp `XDG_STATE_HOME`. The user's daemons were not touched.
- No codex-router bugs were found, so there are no fix commits.

Spikes: `scripts/spikes/stage2-homes.sh`, `scripts/spikes/stage2/` (build tag `live`),
`scripts/spikes/activation/` (own module with purego), `scripts/spikes/plugin/`.

## Results

| Gate | Question                                                                       | Result | What happened                                                                                                                                                                                                                                          | Evidence                                                                                                              |
| ---- | ------------------------------------------------------------------------------ | ------ | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ | --------------------------------------------------------------------------------------------------------------------- |
| G1   | Can a client of the home's daemon see every thread and repaint its footer?     | pass   | Two plain `codex` TUIs shared one daemon. `thread/loaded/list` + `thread/read` gave both threads with their cwd. `thread/resume {excludeTurns}` returned model and cwd. `thread/settings/update` to `gpt-6-luna · low` repainted both footers at once. | `g1-loaded-resume.txt`, `g1-settings-update.txt`, `g1-tui{1,2}-after-update.txt`                                      |
| G2   | Does a provider added to `config.toml` apply without a daemon restart?         | pass   | The provider was written while the daemon ran. `/new` + a prompt went through the proxy and the daemon pid stayed the same. New threads use `provider=router`; threads started earlier keep `provider=openai`.                                         | `g2-loaded-after-provider.txt`, `g2-tui1-new-thread.txt`, `g2-g4-proxy.txt`                                           |
| G3   | Does a static `http_headers` token reach the proxy?                            | pass   | The edge logged `token=ok` for both `/v1/responses` requests. curl without a token or with a wrong one got 401.                                                                                                                                        | `g3-g4-edge.txt`, `g3-tui2-token-turn.txt`                                                                            |
| G4   | Does launchd keep the port while the edge is killed?                           | pass   | Edge built with `CGO_ENABLED=0`. After `kill -9` the next request was served by a new edge in 18, 35 and 18 ms. A real turn killed mid-stream: Codex retried about 0.9 s later and the turn completed.                                                 | `g4-stall.txt`, `g3-g4-edge.txt`, `g4-tui2-turn-across-kill.txt`                                                      |
| G5   | Can a plugin SessionStart hook start a detached service and find the registry? | pass   | The hook ran after the user trusted it in `/hooks`. It had `CODEX_HOME`, `PLUGIN_ROOT` and `PLUGIN_DATA`. A detached child survived with ppid 1. `codex-router sessions --json` from the hook listed the session through the registry.                 | `g5-hook-daemon-run{1,2}.txt`, `g5-hook-launcher.txt`, `g5-detached-children.txt`, `g5-hooks-review-before-trust.txt` |

## Findings used by later tasks

- **The daemon's env is the env of the TUI that started it.** A plugin hook gets that env plus
  `PLUGIN_ROOT` and `PLUGIN_DATA`, and runs under the user's `$SHELL -lc`. `codex-router` is not
  on its PATH. So setup records the binary and state dir in `<home>/codex-router.env`, and the
  plugin's wrapper reads it.
- **Plugin hooks need one-time trust** (`t` in `/hooks`). Codex stores the trust hash in the
  home's `config.toml`. Setup does not write it; the docs tell the user to trust the hook.
- **SessionStart timing:** on `/new` it fires when the thread starts. For the TUI's first thread
  it fires at the first prompt.
- **A provider change applies to new threads only.** Running threads keep their provider until
  `/new` or a restart.
- **`thread/resume` returns an empty effort** when the thread uses the model's default effort.
  The brain treats that as "model default", not as a missing route.
- `thread/settings/updated` carries the full thread settings, including developer instructions.
  The brain does not log the payload.
- After `/new`, the previous thread stays in `thread/loaded/list`, so the brain's 10 min idle rule
  must not expect the daemon to unload it quickly.

## Not verified

- Whether Codex re-rendered the partial reply after the G4 retry (only the screen tail was
  captured).
- Crash loops faster than launchd's `ThrottleInterval` (1 s in the spike plist).
- A detached child that keeps the hook's stdout open (the Codex source says the hook then waits
  until its timeout).

## Cleanup

agterm workspace `router-gate` deleted; launchd spike agent booted out; proxy, test daemon and its
`pid-update-loop` stopped; plugin and marketplace removed; `home-a/config.toml` reset to empty.
Both test homes stay logged in.
