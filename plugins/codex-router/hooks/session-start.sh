#!/bin/sh
# SessionStart hook: keep the binary at the plugin's version, then start this
# home's router services when they are set up and down.
#
# The binary and the plugin are separate installs, so a plugin add or upgrade
# leaves an older binary behind. When the versions differ, this hook runs
# `codex-router self-update --to v<plugin version>`: the release download is
# checksum-verified, its own `version` output must match, and only then does it
# replace the file. CODEX_ROUTER_NO_SELF_UPDATE=1 skips the replacement and
# keeps the check.
#
# The child is detached with stdio redirected, so Codex neither waits for it
# nor reads its output, and the hook returns at once. The hook's stdin is not
# read. A binary gained this way serves the next Codex start: this session's
# launcher already ran.
here=$(dirname "$0")
script="$here/../scripts/codex-router.sh"

want=$(sed -n 's/^ *"version": *"\([^"]*\)".*/\1/p' "$here/../.codex-plugin/plugin.json" 2>/dev/null)
have=$(sh "$script" version 2>/dev/null | head -1)
if [ -n "$want" ] && [ "v$want" != "$have" ]; then
	nohup sh "$script" self-update --to "v$want" </dev/null >/dev/null 2>&1 &
fi

nohup sh "$script" service ensure </dev/null >/dev/null 2>&1 &
