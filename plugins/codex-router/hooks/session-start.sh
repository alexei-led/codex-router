#!/bin/sh
# SessionStart hook: start this Codex home's router services when they are
# set up and down (codex-router service ensure). The child is detached with
# stdio redirected, so Codex neither waits for it nor reads its output, and
# the hook returns at once. The hook's stdin is not read.
nohup sh "$(dirname "$0")/../scripts/codex-router.sh" service ensure </dev/null >/dev/null 2>&1 &
