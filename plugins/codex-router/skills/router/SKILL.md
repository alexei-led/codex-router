---
name: router
description: Show or change how codex-router routes this Codex session. Use when the user asks which model or effort the router picked and why, or asks to pause or resume routing, or to pin the next turn to a tier (micro, low, medium, high).
---

# codex-router control for this session

codex-router picks a model and reasoning effort for every step of this session. These commands act on this session only: the one whose thread is `CODEX_THREAD_ID`, else the one whose working directory is the deepest match for the current directory, in this Codex home.

Run every command through the plugin's wrapper. It finds the codex-router binary and the home's state even when they are not on `PATH`. The wrapper is `scripts/codex-router.sh` in the plugin root, two directories above this skill's directory:

```sh
sh <plugin root>/scripts/codex-router.sh ctl status
```

## Commands

| Ask                               | Command                                   |
| --------------------------------- | ----------------------------------------- |
| what the router does now          | `ctl status`                              |
| stop routing this session         | `ctl pause`                               |
| route this session again          | `ctl resume`                              |
| run the next turn on a fixed tier | `ctl pin --tier micro\|low\|medium\|high` |

`ctl status` prints the session, its label, mode (`on`, `paused`, `manual`), tier, route, reply count and attention flag, if any. While paused, requests keep the model and effort in Codex's footer. A pin applies once, to the next turn, then routing goes back to normal. `ctl resume` also ends manual mode (a `/model` pick).

Change routing only when the user asks for it. `status` is read-only.

## Results

- exit 0: done; show the output to the user.
- exit 1: no running router lists this session, or the command failed. Say so and quote the error. For a home set up with `codex-router setup`, `codex-router service status` shows whether its services run. Without setup, sessions are routed only when Codex was started as `codex-router`.
- exit 2: several sessions match, or a usage error. The candidates are listed; ask the user which one, and suggest `codex-router tui --session ID` to control it from the panel.
- exit 127: the codex-router binary was not found; the user must install it.
