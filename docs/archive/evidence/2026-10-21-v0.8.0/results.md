# v0.8.0 live checks

Tested on macOS with Codex CLI 0.163.0 and the v0.8.0 router build in real agterm sessions. The launcher used private app-servers and an isolated state/runtime directory with a throwaway Git repository. The classifier was off, and each model change was pinned explicitly. No persistent home services were installed. SHA-256 checks confirmed that both existing Codex config files were unchanged.

Each session first answered a short prompt on its native model. Its next turn was pinned to the other model, requesting a fixed response without tools or delegation. Both routed replies completed and were fully priced.

| Native model        | Pinned route            | Routed estimate | Native estimate | Difference              | Bottom status line after routing |
| ------------------- | ----------------------- | --------------- | --------------- | ----------------------- | -------------------------------- |
| gpt-6-luna, medium  | high: gpt-6.1-sol, high | $0.0411700      | $0.0020585      | $0.0391115 extra, 1900% | GPT-6.1-Sol high                 |
| gpt-6.1-sol, medium | low: gpt-6-luna, high   | $0.0021327      | $0.0426540      | $0.0405213 saved, 95%   | GPT-6-Luna high                  |

The Usage panels showed separate savings and extra cost. The extra-cost panel showed `$0.04` extra and `$0.0021` as its native estimate. The savings panel showed `$0.04` saved and `$0.0021` as its routed estimate. Both panels reported one priced routed reply. Codex's status line changed model and effort in both directions, and the plugin printed the pinned route. The reported stale status line did not reproduce.

The first harness attempt used a restricted PATH and produced nonblocking hook exit-127 messages. Repeating both scenarios with the full tool PATH and the new router binary produced no hook failures. Existing home startup warnings remained visible. Captures below are from the repeat run.

- [Extra-cost Usage panel](extra-usage.txt)
- [Savings Usage panel](savings-usage.txt)
- [Extra-cost Codex footer](extra-codex-footer.txt)
- [Savings Codex footer](savings-codex-footer.txt)
- [Unrounded pricing totals](pricing.json)

Automated checks passed: formatting, vet, lint, race tests, vulnerability scan, release-version consistency, release-note validation and documentation links. The documentation renderer regenerated the sample SVG/PNG screens; the Usage, narrow and wide panel layouts were visually inspected. Sample image prices are illustrative, separate from these measured estimates.

Limits: this run checked accounting and launcher footer synchronization, not classifier quality, task ROI, persistent home services or Linux service behavior. Prices compare the same observed tokens; alternative token usage and cache behavior were not simulated.
