# Hook registration via `-c` (Codex 0.162.1)

Spike result. Date 2026-10-13. Status: syntax verified, live firing NOT verified (trust gate).

## Verified

- `codex features list`: `hooks` is `stable true`. No feature flag needed.
- `-c` accepts hooks. Syntax (TOML inline, one `-c` per event):

  ```
  -c 'hooks.UserPromptSubmit=[{hooks=[{type="command",command="/abs/hook.sh"}]}]'
  -c 'hooks.Stop=[{hooks=[{type="command",command="/abs/hook.sh"}]}]'
  ```

  Optional handler fields (app-server schema `ConfiguredHookHandler`): `timeoutSec`,
  `statusMessage`, `async`, `commandWindows`, `additionalContextLimit`. Optional group
  field: `matcher`.

- `codex app-server` with these `-c` flags and the `hooks/list` request (needs
  `capabilities.experimentalApi`) returns two entries:
  - key `/<session-flags>/config.toml:user_prompt_submit:0:0`, `source: sessionFlags`
  - key `/<session-flags>/config.toml:stop:0:0`, `source: sessionFlags`
  - both `enabled: true`, `trustStatus: "untrusted"`, each with a `currentHash` (`sha256:...`).
- Hook events in schema (`HookEventName`): preToolUse, permissionRequest, postToolUse,
  preCompact, postCompact, sessionStart, sessionEnd, userPromptSubmit, subagentStart,
  subagentStop, stop, interrupt. Trust values: managed, untrusted, trusted, modified.

## Blocker: trust

`-c` hooks arrive as `untrusted`. In `codex exec` the script never ran
(no output file; `exec` printed only the user's own trusted hooks).

Tried, did not work:

- `-c 'hooks.state."/<session-flags>/config.toml:stop:0:0".trusted_hash="sha256:<currentHash>"'`.
  Codex warns ``hooks.state."/<session-flags>/...` is ignored``. Trust state is only read
  from persisted user config (`~/.codex/config.toml` `[hooks.state."<key>"]`).

Not tried:

- `codex exec --dangerously-bypass-hook-trust` (also accepted by top-level `codex`).
  Documented as "Run enabled hooks without requiring persisted hook trust for this
  invocation. DANGEROUS". The auto-mode classifier denied running it in this spike. The user
  must run it (script below). `codex app-server` has no such flag in `--help`.
- Writing `hooks.state.<key>.trusted_hash` into a persisted config (needs a user or
  `CODEX_HOME` config edit; excluded by the task rules).

Consequence for the router: purely `-c` registration is not enough on its own. Options:
`--dangerously-bypass-hook-trust` for `exec` runs, or a one-time persisted trust entry
(`hooks.state."<key>".trusted_hash`) for the hook command, for example through app-server
`config/batchWrite` if it allows that key (unverified). Trust hash changes when the hook
command changes (`modified` status), so keep the command string stable.

## Hook stdin keys

Not observed live (hooks did not fire). From source (`codex-rs/hooks/src/schema.rs:279-290`,
see `docs/research.md` §3.7): `session_id`, `turn_id`, `model`, `hook_event_name` among the
inputs. `systemMessage` acceptance by `exec` is also unverified; research.md says it renders
as `Hook · router: ...` in the TUI.

## Script for the user to run (live check)

```sh
D=$(mktemp -d); git -C "$D" init -q
cat > "$D/hook.sh" <<'E'
#!/bin/sh
python3 -I -c 'import sys,json;d=json.load(sys.stdin);print(d.get("hook_event_name"),sorted(d.keys()),file=open(sys.argv[1],"a"))' "$(dirname "$0")/keys.txt"
echo '{"systemMessage":"router: test"}'
E
chmod +x "$D/hook.sh"
cd "$D" && codex --no-daemon exec --dangerously-bypass-hook-trust --ephemeral -s read-only \
  -m gpt-6-luna -c model_reasoning_effort=low \
  -c "hooks.UserPromptSubmit=[{hooks=[{type=\"command\",command=\"$D/hook.sh\"}]}]" \
  -c "hooks.Stop=[{hooks=[{type=\"command\",command=\"$D/hook.sh\"}]}]" "say ok"
cat "$D/keys.txt"
```

Check: `keys.txt` has one line per event; exec output shows the `router: test` message.
