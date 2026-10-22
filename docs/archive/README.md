# Historical evidence

These records describe development experiments and live checks. Use the [user guide](../user-guide.md) and [configuration reference](../configuration.md) for current behavior.

| Record | What it checked |
| --- | --- |
| [Codex research](research.md) | Extension points, footer sync, cache behavior and failure risks. |
| [Single-session live run](evidence/2026-10-14/live-test-report.md) | Routing and manual control in Codex 0.162.1 on macOS. |
| [Five wrapper sessions](evidence/2026-10-14-multi-session/stage1/report.md) | Session discovery, isolation and controls across two homes. |
| [Service probes](evidence/2026-10-14-multi-session/gate/report.md) | Codex daemon behavior and socket activation assumptions. |
| [Five plain Codex sessions](evidence/2026-10-14-multi-session/stage2/report.md) | Setup, service restarts, failure recovery and undo. |
| [Model evaluation](evidence/2026-10-15-openai-defaults/report.md) | Bounded tasks used to choose default routes. |
| [Routing modes live check](evidence/2026-10-21-routing-modes/report.md) | Mode switches, pins, resume, project files, the sandbox, a live Jev run and one task per mode. |

Raw captures and scripts remain with the reports to support their findings. They include test prompts, local paths and session identifiers. They are not examples to paste into a normal Codex home.

Earlier completed plans were removed from the working tree; Git history retains them. Later ones are kept in [plans](plans/). Linux services have unit coverage, but these records do not contain a Linux live run.
