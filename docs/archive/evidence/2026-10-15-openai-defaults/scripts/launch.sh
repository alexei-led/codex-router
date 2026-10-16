#!/bin/zsh -l
# Usage: launch.sh A|B|F [PROMPT]
#   A = config.v2 from sol·medium, B = config.v2 from luna·low,
#   F = final built-in defaults (new binary, user config) from sol·medium
cd ~/Workspace/codex-router-wt-routing-lab
mode=$1; shift
case "$mode" in
  B) exec codex-router --config $LAB/config.v2.json -- --yolo -c model=gpt-6-luna -c model_reasoning_effort=low "$@" ;;
  F) exec $LAB/codex-router-final -- --yolo -c model=gpt-6.1-sol -c model_reasoning_effort=medium "$@" ;;
  *) exec codex-router --config $LAB/config.v2.json -- --yolo -c model=gpt-6.1-sol -c model_reasoning_effort=medium "$@" ;;
esac
