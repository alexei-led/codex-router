package facts_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/alexei-led/codex-router/internal/config"
	"github.com/alexei-led/codex-router/internal/facts"
	"github.com/alexei-led/codex-router/internal/responses"
)

var opts = facts.Options{RecentTurns: 8, MaxTextChars: 400}

func message(role, text string) responses.Item {
	return responses.Item{Type: "message", Role: role, Text: text}
}

func user(text string) responses.Item      { return message("user", text) }
func assistant(text string) responses.Item { return message("assistant", text) }

func jsonArgs(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(b)
}

func exec(id, cmd string) responses.Item {
	return responses.Item{Type: "function_call", Name: "exec_command", CallID: id, Arguments: jsonArgs(map[string]string{"cmd": cmd})}
}

func shell(id string, argv ...string) responses.Item {
	return responses.Item{Type: "function_call", Name: "shell", CallID: id, Arguments: jsonArgs(map[string][]string{"command": argv})}
}

func shellCommand(id, cmd string) responses.Item {
	return responses.Item{Type: "function_call", Name: "shell_command", CallID: id, Arguments: jsonArgs(map[string]string{"command": cmd})}
}

func localShell(id string, argv ...string) responses.Item {
	action := map[string]any{"action": map[string]any{"type": "exec", "command": argv}}
	return responses.Item{Type: "local_shell_call", CallID: id, Arguments: jsonArgs(action)}
}

func tool(id, name string) responses.Item {
	return responses.Item{Type: "function_call", Name: name, CallID: id, Arguments: "{}"}
}

func patchText(paths ...string) string {
	var b strings.Builder
	b.WriteString("*** Begin Patch\n")
	for _, p := range paths {
		fmt.Fprintf(&b, "*** Update File: %s\n@@\n-old\n+new\n", p)
	}
	b.WriteString("*** End Patch\n")
	return b.String()
}

func patch(id string, paths ...string) responses.Item {
	return responses.Item{Type: "custom_tool_call", Name: "apply_patch", CallID: id, Arguments: patchText(paths...)}
}

func execOut(code int, body, wall string) string {
	return fmt.Sprintf("Chunk ID: 9f2a\nWall time: %s seconds\nProcess exited with code %d\nOriginal token count: 12\nOutput:\n%s", wall, code, body)
}

func output(id, text string) responses.Item {
	return responses.Item{Type: "function_call_output", CallID: id, Output: text}
}

func done(id string, code int, body string) responses.Item {
	return output(id, execOut(code, body, "0.4"))
}

func request(items ...responses.Item) *responses.Request {
	return &responses.Request{Model: "gpt-6-sol", Input: items}
}

func TestExtractPrompt(t *testing.T) {
	tests := []struct {
		name  string
		items []responses.Item
		want  string
	}{
		{"last user message", []responses.Item{user("old"), assistant("a"), user("fix the retry test")}, "fix the retry test"},
		{"kept through tool steps", []responses.Item{user("fix it"), exec("c1", "ls"), done("c1", 0, "a.go")}, "fix it"},
		{"environment context skipped", []responses.Item{user("fix it"), user("<environment_context>\n  <cwd>/x</cwd>\n</environment_context>")}, "fix it"},
		{"AGENTS.md skipped", []responses.Item{user("fix it"), user("# AGENTS.md instructions for /x\n\n<INSTRUCTIONS>...")}, "fix it"},
		{"aborted turn skipped", []responses.Item{user("fix it"), user("<turn_aborted>\ninterrupted\n</turn_aborted>")}, "fix it"},
		{"developer text ignored", []responses.Item{message("developer", "be brief")}, ""},
		{"trimmed", []responses.Item{user("  hello \n")}, "hello"},
		{"none", nil, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := facts.Extract(request(tt.items...), opts).Prompt; got != tt.want {
				t.Errorf("Prompt = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestExtractClipsHeadAndTail(t *testing.T) {
	tests := []struct {
		text  string
		limit int
		want  string
	}{
		{"short", 10, "short"},
		{"0123456789", 10, "0123456789"},
		{"0123456789abcdefghij", 11, "012 […] hij"},
		{"0123456789abcdefghij", 12, "0123 […] hij"},
		{"привет, как дела?", 8, "пр […] ?"},
		{"0123456789", 5, "01234"},
		{"0123456789", 0, ""},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("%s/%d", tt.text, tt.limit), func(t *testing.T) {
			f := facts.Extract(request(user(tt.text)), facts.Options{MaxTextChars: tt.limit})
			if f.Prompt != tt.want {
				t.Errorf("Prompt = %q, want %q", f.Prompt, tt.want)
			}
		})
	}
}

func TestExtractDialogue(t *testing.T) {
	items := []responses.Item{
		user("one"), assistant("two"), message("developer", "dev"),
		user("<environment_context></environment_context>"),
		user("three"), assistant("four"), assistant("  "),
		user("the prompt"), exec("c1", "ls"), done("c1", 0, "x"),
		assistant("a long commentary message"),
	}
	tests := []struct {
		name string
		opts facts.Options
		want []facts.Turn
	}{
		{"last turns without the prompt", facts.Options{RecentTurns: 3, MaxTextChars: 100}, []facts.Turn{
			{Role: "user", Text: "three"}, {Role: "assistant", Text: "four"}, {Role: "assistant", Text: "a long commentary message"},
		}},
		{"clipped", facts.Options{RecentTurns: 1, MaxTextChars: 12}, []facts.Turn{{Role: "assistant", Text: "a lo […] age"}}},
		{"all when fewer", facts.Options{RecentTurns: 10, MaxTextChars: 100}, []facts.Turn{
			{Role: "user", Text: "one"}, {Role: "assistant", Text: "two"}, {Role: "user", Text: "three"},
			{Role: "assistant", Text: "four"}, {Role: "assistant", Text: "a long commentary message"},
		}},
		{"none", facts.Options{RecentTurns: 0, MaxTextChars: 100}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := facts.Extract(request(items...), tt.opts).Dialogue
			if fmt.Sprint(got) != fmt.Sprint(tt.want) {
				t.Errorf("Dialogue = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestExtractContinuation(t *testing.T) {
	tests := []struct {
		name  string
		items []responses.Item
		want  bool
	}{
		{"function output last", []responses.Item{user("go"), exec("c1", "ls"), done("c1", 0, "")}, true},
		{"custom tool output last", []responses.Item{user("go"), patch("c1", "a.go"), {Type: "custom_tool_call_output", CallID: "c1", Output: "Success."}}, true},
		{"user message last", []responses.Item{user("go"), exec("c1", "ls"), done("c1", 0, ""), user("next")}, false},
		{"call without output", []responses.Item{user("go"), exec("c1", "ls")}, false},
		{"empty", nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := facts.Extract(request(tt.items...), opts).Continuation; got != tt.want {
				t.Errorf("Continuation = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestExtractObserved(t *testing.T) {
	heredoc := "apply_patch <<'EOF'\n" + patchText("internal/x.go") + "EOF\n"
	tests := []struct {
		name  string
		calls []responses.Item
		want  facts.Observed
	}{
		{"no calls", nil, facts.ObservedTalk},
		{"plan update", []responses.Item{tool("c1", "update_plan")}, facts.ObservedRead},
		{"image view", []responses.Item{tool("c1", "view_image")}, facts.ObservedRead},
		{"exec_command read", []responses.Item{exec("c1", "rg -n foo internal | head -20")}, facts.ObservedRead},
		{"git reads via bash -lc", []responses.Item{shell("c1", "bash", "-lc", "git status && git diff")}, facts.ObservedRead},
		{"local shell ls", []responses.Item{localShell("c1", "ls", "-la")}, facts.ObservedRead},
		{"argv with pattern", []responses.Item{shell("c1", "rg", "-n", "a|b > c", "internal")}, facts.ObservedRead},
		{"exec_command run", []responses.Item{exec("c1", "go test ./...")}, facts.ObservedOps},
		{"shell_command build", []responses.Item{shellCommand("c1", "make build")}, facts.ObservedOps},
		{"local shell write", []responses.Item{localShell("c1", "touch", "x")}, facts.ObservedOps},
		{"unreadable args", []responses.Item{{Type: "function_call", Name: "exec_command", CallID: "c1", Arguments: "not json"}}, facts.ObservedOps},
		{"patch code", []responses.Item{patch("c1", "internal/x.go")}, facts.ObservedCode},
		{"patch docs", []responses.Item{patch("c1", "README.md", "docs/guide.html")}, facts.ObservedDocs},
		{"patch docs and code", []responses.Item{patch("c1", "README.md"), patch("c2", "main.go")}, facts.ObservedCode},
		{"patch without paths", []responses.Item{{Type: "custom_tool_call", Name: "apply_patch", CallID: "c1", Arguments: "garbage"}}, facts.ObservedCode},
		{"patch function tool", []responses.Item{{Type: "function_call", Name: "apply_patch", CallID: "c1", Arguments: jsonArgs(map[string]string{"input": patchText("notes.txt")})}}, facts.ObservedDocs},
		{"patch freeform text field", []responses.Item{{Type: "custom_tool_call", Name: "apply_patch", CallID: "c1", Text: patchText("a.md")}}, facts.ObservedDocs},
		{"patch through exec heredoc", []responses.Item{exec("c1", heredoc)}, facts.ObservedCode},
		{"patch through shell argv", []responses.Item{shell("c1", "apply_patch", "*** Begin Patch\n*** Add File: docs/a.md\n+x\n*** End Patch")}, facts.ObservedDocs},
		{"edit beats ops", []responses.Item{exec("c1", "go test ./..."), patch("c2", "x.go")}, facts.ObservedCode},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			items := append([]responses.Item{user("go")}, tt.calls...)
			if got := facts.Extract(request(items...), opts).Observed; got != tt.want {
				t.Errorf("Observed = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestExtractObservedReadOnlyCommand(t *testing.T) {
	tests := []struct {
		cmd      string
		readOnly bool
	}{
		{"ls", true},
		{"cat x.go", true},
		{"rg -n foo internal | head -20", true},
		{"sed -n '1,120p' internal/facts/facts.go", true},
		{"nl -ba x.go && wc -l x.go", true},
		{"cd internal && ls", true},
		{"git log --oneline -5; git show HEAD", true},
		{"find . -name '*.go'", true},
		{`grep -n 'a;b>c' x.go 2>/dev/null`, true},
		{"cat x.go 2>&1 | tail -5", true},
		{"pwd", true},
		{"sed -i 's/a/b/' x.go", false},
		{"sed -ni 's/a/b/p' x.go", false},
		{"sed 's/a/b/' x.go", false},
		{"cat a > b", false},
		{"cat a >> b", false},
		{"cat < a", false},
		{`echo "$(rm -rf x)"`, false},
		{"ls `rm x`", false},
		{"find . -name '*.tmp' -delete", false},
		{"find . -exec rm {} ;", false},
		{"git commit -m 'x'", false},
		{"git -C dir status", false},
		{"git", false},
		{"ls && rm x", false},
		{"ls; touch y", false},
		{"cat x | sh", false},
		{"echo hi", false},
		{"go test ./...", false},
	}
	for _, tt := range tests {
		t.Run(tt.cmd, func(t *testing.T) {
			want := facts.ObservedOps
			if tt.readOnly {
				want = facts.ObservedRead
			}
			if got := facts.Extract(request(user("go"), exec("c1", tt.cmd)), opts).Observed; got != want {
				t.Errorf("Observed = %q, want %q", got, want)
			}
		})
	}
}

func TestExtractObservedCountsOnlyTheCurrentTurn(t *testing.T) {
	items := []responses.Item{
		user("first"), patch("c1", "x.go"), done("c1", 0, ""),
		user("second"), exec("c2", "go test ./..."), done("c2", 0, ""), exec("c3", "ls"), done("c3", 0, ""),
	}
	tests := []struct {
		name  string
		items []responses.Item
		from  int
		want  facts.Observed
	}{
		{"calls after the prompt", items, 0, facts.ObservedOps},
		{"earlier turn", items[:3], 0, facts.ObservedCode},
		{"from an index", items, 6, facts.ObservedRead},
		{"from past the end", items, len(items), facts.ObservedTalk},
		{"new prompt without calls", append(items[:3:3], user("third")), 0, facts.ObservedTalk},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := facts.Extract(request(tt.items...), facts.Options{ObservedFrom: tt.from}).Observed
			if got != tt.want {
				t.Errorf("Observed = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestExtractVerifiedByCommand(t *testing.T) {
	tests := []struct {
		cmd  string
		want bool
	}{
		{"go test ./...", true},
		{"go test -race ./... 2>&1", true},
		{"go vet ./...", true},
		{"make lint", true},
		{"npm test", true},
		{"npm run lint", true},
		{"npm run test:unit", true},
		{"uv run pytest -q", true},
		{"python -m pytest tests", true},
		{"node --test", true},
		{"cargo clippy", true},
		{"cd app && go test ./...", true},
		{"FOO=1 pytest", true},
		{"./node_modules/.bin/vitest run", true},
		{"golangci-lint run ./...", true},
		{"go build ./...", true},
		{"make build", true},
		{"cargo check", true},
		{"npm run validate", true},
		{"go test ./... | tail -5", false},
		{"go test ./...; echo done", false},
		{"go test ./... || true", false},
		{"go test ./... &", false},
		{"npm install -D vitest", false},
		{"git diff test/x.mjs", false},
		{"go test -h", false},
		{"pytest --version", false},
		{"npm --prefix test install", false},
		{"go run ./cmd/x", false},
		{"node test.js", false},
		{"make", false},
		{"ls tests", false},
	}
	for _, tt := range tests {
		t.Run(tt.cmd, func(t *testing.T) {
			f := facts.Extract(request(user("go"), exec("c1", tt.cmd), done("c1", 0, "ok")), opts)
			if f.Verified != tt.want {
				t.Errorf("Verified = %v, want %v", f.Verified, tt.want)
			}
		})
	}
}

func TestExtractVerifiedByResult(t *testing.T) {
	const check = "go test ./..."
	tests := []struct {
		name  string
		items []responses.Item
		from  int
		want  bool
	}{
		{"exec exit 0", []responses.Item{exec("c1", check), done("c1", 0, "ok")}, 0, true},
		{"exec exit 1", []responses.Item{exec("c1", check), done("c1", 1, "FAIL")}, 0, false},
		{"still running", []responses.Item{exec("c1", check), output("c1", "Chunk ID: 1\nWall time: 10 seconds\nProcess running with session ID 7\nOriginal token count: 0\nOutput:\n")}, 0, false},
		{"shell JSON exit 0", []responses.Item{shell("c1", "bash", "-lc", check), output("c1", `{"output":"ok","metadata":{"exit_code":0,"duration_seconds":1.2}}`)}, 0, true},
		{"shell JSON exit 2", []responses.Item{shell("c1", "bash", "-lc", check), output("c1", `{"output":"x","metadata":{"exit_code":2}}`)}, 0, false},
		{"exit code header", []responses.Item{shellCommand("c1", check), output("c1", "Exit code: 0\nWall time: 3.1 seconds\nOutput:\nok")}, 0, true},
		{"empty output section", []responses.Item{shellCommand("c1", check), output("c1", "Exit code: 0\nWall time: 3.1 seconds\nOutput:")}, 0, true},
		{"no exit status", []responses.Item{exec("c1", check), output("c1", "ok")}, 0, false},
		{"exit line without header", []responses.Item{exec("c1", check), output("c1", "ok\nExit code: 0")}, 0, false},
		{"no output yet", []responses.Item{exec("c1", check)}, 0, false},
		{"last check failed", []responses.Item{exec("c1", check), done("c1", 0, ""), exec("c2", check), done("c2", 1, "")}, 0, false},
		{"failed then passed", []responses.Item{exec("c1", check), done("c1", 1, ""), patch("c2", "x.go"), exec("c3", check), done("c3", 0, "")}, 0, true},
		{"later non-check ignored", []responses.Item{exec("c1", check), done("c1", 0, ""), exec("c2", "go run ."), done("c2", 1, "")}, 0, true},
		{"check before the index", []responses.Item{exec("c1", check), done("c1", 0, ""), patch("c2", "x.go")}, 3, false},
		{"none", []responses.Item{exec("c1", "ls"), done("c1", 0, "")}, 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			items := append([]responses.Item{user("go")}, tt.items...)
			got := facts.Extract(request(items...), facts.Options{VerifiedFrom: tt.from}).Verified
			if got != tt.want {
				t.Errorf("Verified = %v, want %v", got, tt.want)
			}
		})
	}
}

func repeated(first, second string) []responses.Item {
	return []responses.Item{
		user("fix the test"),
		exec("c1", "go test ./..."), output("c1", first),
		patch("c2", "x.go"), output("c2", "Exit code: 0\nWall time: 0 seconds\nOutput:\nSuccess."),
		exec("c3", "go test ./..."), output("c3", second),
	}
}

func TestExtractFailureSignatureNormalization(t *testing.T) {
	tests := []struct {
		name          string
		first, second string
		same          bool
	}{
		{"identical", execOut(1, "FAIL x", "1"), execOut(1, "FAIL x", "1"), true},
		{"codex header timing", execOut(1, "FAIL x", "0.31"), execOut(1, "FAIL x", "2.7"), true},
		{"header format", execOut(1, "FAIL x", "1"), "Exit code: 1\nWall time: 1 seconds\nOutput:\nFAIL x", true},
		{"ansi colors", execOut(1, "\x1b[31mFAIL\x1b[0m x", "1"), execOut(1, "FAIL x", "1"), true},
		{"line endings", execOut(1, "a\r\nb\rc", "1"), execOut(1, "a\nb\nc", "1"), true},
		{"js stack positions", execOut(1, "Error: boom\n    at run (src/a.mjs:12:5)", "1"), execOut(1, "Error: boom\n    at run (src/a.mjs:40:9)", "1"), true},
		{"js locations", execOut(1, "location: 'test/a.test.mjs:3:1'", "1"), execOut(1, "location: 'test/a.test.mjs:9:2'", "1"), true},
		{"duration_ms", execOut(1, "not ok 1\n  duration_ms: 12.5", "1"), execOut(1, "not ok 1\n  duration_ms: 80", "1"), true},
		{"jest timing", execOut(1, "  ✕ adds (3 ms)", "1"), execOut(1, "  ✕ adds (41 ms)", "1"), true},
		{"go test timing", execOut(1, "--- FAIL: TestX (0.01s)\nFAIL\tgithub.com/a/b\t0.2s", "1"), execOut(1, "--- FAIL: TestX (0.30s)\nFAIL\tgithub.com/a/b\t1.5s", "1"), true},
		{"go test position", execOut(1, "    x_test.go:42: want 1, got 2", "1"), execOut(1, "    x_test.go:57: want 1, got 2", "1"), true},
		{"go stack frame", execOut(2, "panic: boom\n\t/src/a/x.go:12 +0x1d", "1"), execOut(2, "panic: boom\n\t/src/a/x.go:19 +0x2f", "1"), true},
		{"python traceback", execOut(1, `  File "a.py", line 12, in f`, "1"), execOut(1, `  File "a.py", line 30, in f`, "1"), true},
		{"pytest timing", execOut(1, "=== 1 failed in 0.12s ===", "1"), execOut(1, "=== 1 failed in 3.40s ===", "1"), true},
		{"shell JSON", `{"output":"FAIL x","metadata":{"exit_code":1,"duration_seconds":0.2}}`, `{"output":"FAIL x","metadata":{"exit_code":1,"duration_seconds":9}}`, true},
		{"different message", execOut(1, "want 1, got 2", "1"), execOut(1, "want 1, got 3", "1"), false},
		{"different exit code", execOut(1, "FAIL x", "1"), execOut(2, "FAIL x", "1"), false},
		{"go test message changed", execOut(1, "    x_test.go:42: want 1, got 2", "1"), execOut(1, "    x_test.go:42: want 1, got 5", "1"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := facts.Extract(request(repeated(tt.first, tt.second)...), opts)
			if got := f.Failure != nil; got != tt.same {
				t.Errorf("repeated failure = %v, want %v", got, tt.same)
			}
		})
	}
}

func TestExtractFailure(t *testing.T) {
	fail := func(id, body string) responses.Item { return done(id, 1, body) }
	filler := make([]responses.Item, facts.FailureWindow)
	for i := range filler {
		filler[i] = assistant("working")
	}
	tests := []struct {
		name      string
		items     []responses.Item
		wantIndex int
	}{
		{"same failure after a patch", repeated(execOut(1, "FAIL", "1"), execOut(1, "FAIL", "1")), 6},
		{"same failure without an edit", []responses.Item{
			user("go"), exec("c1", "go test ./..."), fail("c1", "FAIL"), exec("c2", "go test ./..."), fail("c2", "FAIL"),
		}, -1},
		{"exit lines without header", []responses.Item{
			user("go"), exec("c1", "go test ./..."), output("c1", "ok\nExit code: 1"),
			patch("c2", "x.go"),
			exec("c3", "go test ./..."), output("c3", "ok\nExit code: 1"),
		}, -1},
		{"failing reruns are not edits", []responses.Item{
			user("go"), exec("c1", "go run ."), fail("c1", "boom"), exec("c2", "go run ."), fail("c2", "boom"),
		}, -1},
		{"writing command between", []responses.Item{
			user("go"), exec("c1", "go test ./..."), fail("c1", "FAIL"),
			exec("c2", "sed -i 's/a/b/' x.go"), done("c2", 0, ""),
			exec("c3", "go test ./..."), fail("c3", "FAIL"),
		}, 6},
		{"read between", []responses.Item{
			user("go"), exec("c1", "go test ./..."), fail("c1", "FAIL"),
			exec("c2", "cat x.go"), done("c2", 0, ""),
			exec("c3", "go test ./..."), fail("c3", "FAIL"),
		}, -1},
		{"different failures", repeated(execOut(1, "FAIL a", "1"), execOut(1, "FAIL b", "1")), -1},
		{"passing runs", repeated(execOut(0, "ok", "1"), execOut(0, "ok", "1")), -1},
		{"first failure outside the window", append(append([]responses.Item{
			user("go"), exec("c1", "go test ./..."), fail("c1", "FAIL"), patch("c2", "x.go"),
		}, filler...), exec("c3", "go test ./..."), fail("c3", "FAIL")), -1},
		{"latest pair wins", append(repeated(execOut(1, "FAIL a", "1"), execOut(1, "FAIL a", "1")),
			patch("c4", "y.go"), exec("c5", "go test ./..."), fail("c5", "FAIL b"),
			patch("c6", "y.go"), exec("c7", "go test ./..."), fail("c7", "FAIL b"),
		), 12},
		{"pair before the last prompt", append(repeated(execOut(1, "FAIL", "1"), execOut(1, "FAIL", "1")),
			user("now the docs"),
		), -1},
		{"a later passing check", append(repeated(execOut(1, "FAIL", "1"), execOut(1, "FAIL", "1")),
			patch("c4", "x.go"), exec("c5", "go test ./..."), done("c5", 0, "ok"),
		), -1},
		{"a later check still failing", append(repeated(execOut(1, "FAIL", "1"), execOut(1, "FAIL", "1")),
			patch("c4", "x.go"), exec("c5", "go test ./..."), fail("c5", "FAIL c"),
		), 6},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := facts.Extract(request(tt.items...), opts).Failure
			switch {
			case tt.wantIndex < 0 && got != nil:
				t.Errorf("Failure = %+v, want nil", got)
			case tt.wantIndex >= 0 && got == nil:
				t.Errorf("Failure = nil, want index %d", tt.wantIndex)
			case got != nil && (got.Index != tt.wantIndex || got.Signature == ""):
				t.Errorf("Failure = %+v, want index %d and a signature", got, tt.wantIndex)
			}
		})
	}
}

func TestExtractFailureSignatureIsStableAndDistinct(t *testing.T) {
	sig := func(body string) string {
		f := facts.Extract(request(repeated(execOut(1, body, "1"), execOut(1, body, "2"))...), opts)
		if f.Failure == nil {
			t.Fatalf("no failure for %q", body)
		}
		return f.Failure.Signature
	}
	first, again := sig("FAIL a"), sig("FAIL a")
	if first != again {
		t.Error("same output gave different signatures")
	}
	if first == sig("FAIL b") {
		t.Error("different outputs gave one signature")
	}
}

func TestExtractSize(t *testing.T) {
	tests := []struct {
		name       string
		req        *responses.Request
		wantTokens int
		wantItems  int
	}{
		{"body bytes", &responses.Request{Bytes: 4000, Input: []responses.Item{user("hi")}}, 1000, 1},
		{"rounds down", &responses.Request{Bytes: 4003, Input: []responses.Item{user("hi")}}, 1000, 1},
		{"item text when bytes unknown", &responses.Request{Input: []responses.Item{user("0123456789"), output("c1", "ab")}}, 3, 2},
		{"empty", &responses.Request{}, 0, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := facts.Extract(tt.req, opts)
			if f.ApproxTokens != tt.wantTokens || f.Items != tt.wantItems {
				t.Errorf("ApproxTokens, Items = %d, %d; want %d, %d", f.ApproxTokens, f.Items, tt.wantTokens, tt.wantItems)
			}
		})
	}
}

func TestExtractNilRequest(t *testing.T) {
	f := facts.Extract(nil, opts)
	if f.Observed != facts.ObservedTalk || f.Prompt != "" || f.Failure != nil || f.Items != 0 {
		t.Errorf("Extract(nil) = %+v", f)
	}
}

func TestAgrees(t *testing.T) {
	tests := []struct {
		predicted config.Activity
		observed  facts.Observed
		want      bool
	}{
		{config.ActivityCode, facts.ObservedCode, true},
		{config.ActivityCode, facts.ObservedRead, false},
		{config.ActivityDebug, facts.ObservedOps, true},
		{config.ActivityDebug, facts.ObservedTalk, false},
		{config.ActivityExplore, facts.ObservedTalk, true},
		{config.ActivityExplore, facts.ObservedCode, false},
		{config.ActivityPlan, facts.ObservedDocs, true},
		{config.ActivityReview, facts.ObservedOps, false},
		{config.ActivityOps, facts.ObservedRead, true},
		{config.ActivityDocs, facts.ObservedDocs, true},
		{config.ActivityDocs, facts.ObservedCode, false},
		{config.ActivityUncertain, facts.ObservedTalk, false},
		{"", facts.ObservedTalk, false},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("%s/%s", tt.predicted, tt.observed), func(t *testing.T) {
			if got := facts.Agrees(tt.predicted, tt.observed); got != tt.want {
				t.Errorf("Agrees = %v, want %v", got, tt.want)
			}
		})
	}
}
