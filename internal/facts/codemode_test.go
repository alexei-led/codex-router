package facts_test

import (
	"testing"

	"github.com/alexei-led/codex-router/internal/facts"
	"github.com/alexei-led/codex-router/internal/responses"
)

// Code-mode cells as Codex 0.162 sent them in a live session.
const (
	goTestCell = "const r = await tools.exec_command({cmd:\"go test ./...\",yield_time_ms:30000,max_output_tokens:4000});\n" +
		"text(r.output);\ntext(`exit_code=${r.exit_code}`);\n"
	printfTestCell = "const r = await tools.exec_command({cmd:\"printf '// x\\\\n' >> retry.go\\ngo test ./...\",yield_time_ms:30000,max_output_tokens:4000});\n" +
		"text(r.output);\ntext(`exit_code=${r.exit_code}`);\n"
	patchCell = "await tools.apply_patch(\"*** Begin Patch\\n*** Update File: retry.go\\n@@\\n-old\\n+new\\n*** End Patch\\n\");\ntext(\"patched\");\n"
)

func cell(id, js string) responses.Item {
	return responses.Item{Type: "custom_tool_call", Name: "exec", CallID: id, Arguments: js}
}

// cellOut is a cell result as Codex sends it, an array of text parts, read
// through the request parser.
func cellOut(id string, parts ...string) responses.Item {
	content := make([]map[string]string, len(parts))
	for i, p := range parts {
		content[i] = map[string]string{"type": "input_text", "text": p}
	}
	item := map[string]any{"type": "custom_tool_call_output", "call_id": id, "output": content}
	req, err := responses.Parse([]byte(jsonArgs(map[string]any{"model": "gpt-6-sol", "input": []any{item}})))
	if err != nil {
		panic(err)
	}
	return req.Input[0]
}

func scriptCompleted(wall string, body ...string) []string {
	return append([]string{"Script completed\nWall time " + wall + " seconds\nOutput:\n"}, body...)
}

func goTestFailed(id, wall, pkgTime string) responses.Item {
	return cellOut(id, scriptCompleted(wall,
		"--- FAIL: TestProbe (0.00s)\n    probe_test.go:3: probe\nFAIL\nFAIL\texample.com/retry\t"+pkgTime+"\nFAIL\n",
		"exit_code=1")...)
}

func TestExtractFailureCodeMode(t *testing.T) {
	tests := []struct {
		name      string
		items     []responses.Item
		wantIndex int
	}{
		{"apply_patch cell between", []responses.Item{
			user("fix it"), cell("c1", goTestCell), goTestFailed("c1", "1.3", "0.160s"),
			cell("c2", patchCell), cellOut("c2", scriptCompleted("0.1", "patched")...),
			cell("c3", goTestCell), goTestFailed("c3", "0.7", "0.210s"),
		}, 6},
		{"writing command cell between", []responses.Item{
			user("fix it"), cell("c1", goTestCell), goTestFailed("c1", "1.3", "0.160s"),
			cell("c2", "const r = await tools.exec_command({cmd:\"printf '// x\\\\n' >> retry.go\"});\ntext(`exit_code=${r.exit_code}`);"),
			cellOut("c2", scriptCompleted("0.1", "exit_code=0")...),
			cell("c3", goTestCell), goTestFailed("c3", "0.7", "0.210s"),
		}, 6},
		{"no edit between", []responses.Item{
			user("fix it"), cell("c1", goTestCell), goTestFailed("c1", "1.3", "0.160s"),
			cell("c2", goTestCell), goTestFailed("c2", "0.7", "0.160s"),
		}, -1},
		{"failed writing cell is not an edit", []responses.Item{
			user("fix it"), cell("c1", goTestCell), goTestFailed("c1", "1.3", "0.160s"),
			cell("c2", printfTestCell), goTestFailed("c2", "0.7", "0.160s"),
			cell("c3", goTestCell), goTestFailed("c3", "0.7", "0.160s"),
		}, -1},
		{"read cell between", []responses.Item{
			user("fix it"), cell("c1", goTestCell), goTestFailed("c1", "1.3", "0.160s"),
			cell("c2", "text((await tools.exec_command({cmd:'cat retry.go'})).output);"), cellOut("c2", scriptCompleted("0.1", "package retry")...),
			cell("c3", goTestCell), goTestFailed("c3", "0.7", "0.160s"),
		}, -1},
		{"different printed exit codes", []responses.Item{
			user("fix it"), cell("c1", goTestCell), goTestFailed("c1", "1.3", "0.160s"),
			cell("c2", patchCell), cellOut("c2", scriptCompleted("0.1")...),
			cell("c3", goTestCell), cellOut("c3", scriptCompleted("0.7", "--- FAIL: TestProbe (0.00s)\n    probe_test.go:3: probe\nFAIL\nFAIL\texample.com/retry\t0.1s\nFAIL\n", "exit_code=2")...),
		}, -1},
		{"nested exit line", []responses.Item{
			user("fix it"), cell("c1", goTestCell), cellOut("c1", scriptCompleted("1", "FAIL x\nProcess exited with code 1")...),
			cell("c2", patchCell), cellOut("c2", scriptCompleted("0.1")...),
			cell("c3", goTestCell), cellOut("c3", scriptCompleted("1", "FAIL x\nProcess exited with code 1")...),
		}, 6},
		{"script failed", []responses.Item{
			user("fix it"), cell("c1", goTestCell), cellOut("c1", "Script failed\nWall time 0.2 seconds\nOutput:\n", "TypeError: x is undefined"),
			cell("c2", patchCell), cellOut("c2", scriptCompleted("0.1")...),
			cell("c3", goTestCell), cellOut("c3", "Script failed\nWall time 0.4 seconds\nOutput:\n", "TypeError: x is undefined"),
		}, 6},
		{"running cell is not a failure", []responses.Item{
			user("fix it"), cell("c1", goTestCell), cellOut("c1", "Script running with cell ID 3\nWall time 10.0 seconds\nOutput:\n"),
			cell("c2", patchCell), cellOut("c2", scriptCompleted("0.1")...),
			cell("c3", goTestCell), cellOut("c3", "Script running with cell ID 3\nWall time 10.0 seconds\nOutput:\n"),
		}, -1},
		{"no printed exit code", []responses.Item{
			user("fix it"), cell("c1", goTestCell), cellOut("c1", scriptCompleted("1", "FAIL x")...),
			cell("c2", patchCell), cellOut("c2", scriptCompleted("0.1")...),
			cell("c3", goTestCell), cellOut("c3", scriptCompleted("1", "FAIL x")...),
		}, -1},
		{"printed exit code without the cell header", []responses.Item{
			user("fix it"), exec("c1", "go test ./..."), output("c1", "FAIL x\nexit_code=1"),
			patch("c2", "x.go"),
			exec("c3", "go test ./..."), output("c3", "FAIL x\nexit_code=1"),
		}, -1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := facts.Extract(request(tt.items...), opts).Failure
			switch {
			case tt.wantIndex < 0 && got != nil:
				t.Errorf("Failure = %+v, want nil", got)
			case tt.wantIndex >= 0 && got == nil:
				t.Errorf("Failure = nil, want index %d", tt.wantIndex)
			case got != nil && got.Index != tt.wantIndex:
				t.Errorf("Failure = %+v, want index %d", got, tt.wantIndex)
			}
		})
	}
}

func TestExtractVerifiedCodeMode(t *testing.T) {
	passed := func(id string) responses.Item {
		return cellOut(id, scriptCompleted("1.3", "ok  \texample.com/retry\t0.2s\n", "exit_code=0")...)
	}
	tests := []struct {
		name  string
		items []responses.Item
		from  int
		want  bool
	}{
		{"printed exit 0 after the last edit", []responses.Item{
			cell("c1", patchCell), cellOut("c1", scriptCompleted("0.1")...), cell("c2", goTestCell), passed("c2"),
		}, 1, true},
		{"check before the last edit", []responses.Item{
			cell("c1", goTestCell), passed("c1"), cell("c2", patchCell), cellOut("c2", scriptCompleted("0.1")...),
		}, 3, false},
		{"printed exit 1", []responses.Item{cell("c1", goTestCell), goTestFailed("c1", "1.3", "0.160s")}, 0, false},
		{"no printed exit code", []responses.Item{cell("c1", goTestCell), cellOut("c1", scriptCompleted("1.3", "ok")...)}, 0, false},
		{"still running", []responses.Item{cell("c1", goTestCell), cellOut("c1", "Script running with cell ID 3\nWall time 10.0 seconds\nOutput:\n")}, 0, false},
		{"script failed", []responses.Item{cell("c1", goTestCell), cellOut("c1", "Script failed\nWall time 0.2 seconds\nOutput:\n", "boom")}, 0, false},
		{"printed exit code with spaces", []responses.Item{cell("c1", goTestCell), cellOut("c1", scriptCompleted("1", "Exit_Code: 0")...)}, 0, true},
		{"printed exit code in words", []responses.Item{cell("c1", goTestCell), cellOut("c1", scriptCompleted("1", "exit code: 0")...)}, 0, true},
		{"single-quoted command", []responses.Item{cell("c1", "await tools.exec_command({cmd: 'make lint'})"), passed("c1")}, 0, true},
		{"template command", []responses.Item{cell("c1", "await tools.exec_command({cmd: `go vet ./...`})"), passed("c1")}, 0, true},
		{"shell_command", []responses.Item{cell("c1", `await tools.shell_command({command: "npm test"})`), passed("c1")}, 0, true},
		{"interpolated command", []responses.Item{cell("c1", "await tools.exec_command({cmd: `go test ${pkg}`})"), passed("c1")}, 0, false},
		{"command in a variable", []responses.Item{cell("c1", "await tools.exec_command({cmd})"), passed("c1")}, 0, false},
		{"two commands in one cell", []responses.Item{
			cell("c1", `await tools.exec_command({cmd: "go test ./..."}); await tools.exec_command({cmd: "go vet ./..."})`), passed("c1"),
		}, 0, false},
		{"not a check", []responses.Item{cell("c1", `await tools.exec_command({cmd: "go run ."})`), passed("c1")}, 0, false},
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

func TestExtractObservedCodeMode(t *testing.T) {
	tests := []struct {
		name string
		js   string
		want facts.Observed
	}{
		{"no nested tools", `text(1 + 1)`, facts.ObservedRead},
		{"read", `await tools.exec_command({cmd: "cat retry.go"})`, facts.ObservedRead},
		{"escaped quotes", `await tools.exec_command({cmd: "rg -n \"a;b > c\" retry.go"})`, facts.ObservedRead},
		{"escaped single quotes", `await tools.exec_command({cmd: 'grep -n \'a|b\' x.go'})`, facts.ObservedRead},
		{"unicode escapes", `await tools.exec_command({cmd: "rg -n \u0022a;b\u{22} x.go"})`, facts.ObservedRead},
		{"hex escapes", `await tools.exec_command({cmd: "grep -n \x27a|b\x27 x.go"})`, facts.ObservedRead},
		{"two reads", `await tools.exec_command({cmd: "ls"}); await tools.exec_command({cmd: "git status"})`, facts.ObservedRead},
		{"read then write", `await tools.exec_command({cmd: "ls"}); await tools.exec_command({cmd: "touch x"})`, facts.ObservedOps},
		{"escaped newline joins commands", `await tools.exec_command({cmd: "ls\nrm x"})`, facts.ObservedOps},
		{"check", goTestCell, facts.ObservedOps},
		{"unreadable command", "await tools.exec_command({cmd: `cat ${f}`})", facts.ObservedOps},
		{"unterminated literal", `await tools.exec_command({cmd: "cat x`, facts.ObservedOps},
		{"patch code", patchCell, facts.ObservedCode},
		{"patch docs in a template", "await tools.apply_patch(`*** Begin Patch\n*** Add File: docs/a.md\n+x\n*** End Patch`)", facts.ObservedDocs},
		{"patch through a heredoc", `await tools.exec_command({cmd: "apply_patch <<'EOF'\n*** Begin Patch\n*** Update File: README.md\n@@\n-a\n+b\n*** End Patch\nEOF"})`, facts.ObservedDocs},
		{"patch beats a check", patchCell + goTestCell, facts.ObservedCode},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := facts.Extract(request(user("go"), cell("c1", tt.js)), opts).Observed; got != tt.want {
				t.Errorf("Observed = %q, want %q", got, tt.want)
			}
		})
	}
}
