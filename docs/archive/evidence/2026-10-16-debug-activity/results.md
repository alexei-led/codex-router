# Debug activity, 2026-10-16

Live sessions showed Jev labelling debugging prompts `code` or `ops`, so the `debug` route at
`medium` rarely fired. Cause: the contract names an activity by what the turn produces, and its
mixed-turn rule ("fix it and commit" is code) made any failure-plus-fix prompt `code`.

Change (`internal/classifier/contract.go`):

- `debug` covers a found cause "usually with its fix", and names errors, panics and build failures.
- `code` is not for a failure whose cause is unknown "even when the fix is asked for too".
- One judge line: "the test fails, fix it" is debug; "change X to Y so the test passes" is code.

Eval: `labeval_test.go.txt`. Copy it to `internal/launcher/labeval_test.go` and run
`go test -tags labeval -run TestLabEvalActivity -v ./internal/launcher/`. It sends 20 labelled
prompts to the configured classifier (Jev 1.13.0).

| Prompts                                                   | Before (3 runs) | After (2 runs) |
| --------------------------------------------------------- | --------------- | -------------- |
| 8 debug: a failure, cause not given, most ask for the fix | 4/8, 4/8, 3/8   | 8/8, 8/8       |
| 12 others: code, ops, explore, review, plan, docs         | 12/12 each      | 12/12 each     |

Every miss before was a failure-plus-fix prompt, scored `code` with debug at 0.07–0.48. After,
the eight debug prompts score 0.67–1.00 and the others keep their labels at 0.98–1.00. "Run the
tests and commit if they pass" stays `ops`. Only Jev was measured; `clef`, `openai` and `ollama`
read the same contract.
