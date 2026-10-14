package facts

import (
	"encoding/json"
	"hash/fnv"
	"regexp"
	"strconv"
	"strings"

	"github.com/alexei-led/codex-router/internal/responses"
)

// result is a tool output's exit status and the command output under the
// Codex header. Codex formats it three ways: JSON `{"output", "metadata":
// {"exit_code"}}` (shell), "Exit code: N" (shell_command, apply_patch), and
// "Process exited with code N" or "Process running with session ID N"
// (exec_command), each header ending at an "Output:" line. Code mode adds a
// fourth: a "Script ..." header for a JavaScript cell (see parseCell).
type result struct {
	exit    int
	known   bool
	running bool
	body    string
}

var (
	exitLine    = regexp.MustCompile(`^(?:Exit code:|Process exited with code) (-?\d+)$`)
	runningLine = regexp.MustCompile(`^Process running with session ID \d+`)
	cellHeader  = regexp.MustCompile(`^Script (?:completed|failed|terminated|error:|running with cell ID)`)
	printedExit = regexp.MustCompile(`(?i)^exit[ _-]?code\s*[=:]\s*(-?\d+)$`)
)

func parseResult(out string) result {
	if strings.HasPrefix(strings.TrimSpace(out), "{") {
		var j struct {
			Output   string `json:"output"`
			Metadata struct {
				ExitCode *int `json:"exit_code"`
			} `json:"metadata"`
		}
		if json.Unmarshal([]byte(out), &j) == nil && j.Metadata.ExitCode != nil {
			return result{exit: *j.Metadata.ExitCode, known: true, body: j.Output}
		}
	}
	if status, _, _ := strings.Cut(out, "\n"); cellHeader.MatchString(status) {
		return parseCell(out, status)
	}
	// Without the header the text is all command output: an "Exit code:"
	// line in it says nothing about this call.
	header, body, found := strings.Cut(out, "\nOutput:")
	if !found {
		return result{body: out}
	}
	r := result{body: strings.TrimPrefix(body, "\n")}
	for line := range strings.Lines(header) {
		line = strings.TrimSpace(line)
		if m := exitLine.FindStringSubmatch(line); m != nil {
			r.exit, _ = strconv.Atoi(m[1])
			r.known = true
		}
		r.running = r.running || runningLine.MatchString(line)
	}
	return r
}

// parseCell reads a code-mode cell result. Codex reports how the script
// ended, not what its commands returned, so a command's exit status is known
// only when the script printed it: the last `exit_code=N` or `exit code: N`
// line, or a nested tool's exit line, in the body. Unlike plain output, a
// cell body is what the script chose to print, and models print the status
// after the output. A script that threw before printing one failed. A body
// that is the nested result itself, `text(JSON.stringify(r))`, gives its
// `output` and `exit_code`; a printed line in that output still wins, since
// models mask the command's status (`cmd; echo "exit code: $?"; exit 0`). A
// result with a `session_id` and no `exit_code` is still running: it ends in
// a later `write_stdin` cell.
func parseCell(out, status string) result {
	_, body, found := strings.Cut(out, "\nOutput:")
	if !found {
		_, body, _ = strings.Cut(out, "\n")
	}
	r := result{body: strings.TrimPrefix(body, "\n"), running: strings.HasPrefix(status, "Script running")}
	var nested struct {
		Output    *string `json:"output"`
		ExitCode  *int    `json:"exit_code"`
		SessionID *int    `json:"session_id"`
	}
	if t := strings.TrimSpace(r.body); strings.HasPrefix(t, "{") && json.Unmarshal([]byte(t), &nested) == nil && nested.Output != nil {
		r.body = *nested.Output
		switch {
		case nested.ExitCode != nil:
			r.exit, r.known = *nested.ExitCode, true
		case nested.SessionID != nil:
			r.running = true
		}
	}
	for line := range strings.Lines(r.body) {
		line = strings.TrimSpace(line)
		m := printedExit.FindStringSubmatch(line)
		if m == nil {
			m = exitLine.FindStringSubmatch(line)
		}
		if m != nil {
			r.exit, _ = strconv.Atoi(m[1])
			r.known = true
		}
	}
	if !r.known && (strings.HasPrefix(status, "Script failed") || strings.HasPrefix(status, "Script error:")) {
		r.exit, r.known = 1, true
	}
	return r
}

// Run-to-run noise in failure output: positions and timings. Unknown runner
// noise is kept, so changed output conservatively counts as a new failure.
var noise = []struct {
	re   *regexp.Regexp
	with string
}{
	{regexp.MustCompile("\x1b\\[[0-?]*[ -/]*[@-~]"), ""},
	// JS stack frames and locations: `at f (src/x.mjs:12:5)`, `location: 'x.mjs:3'`.
	{regexp.MustCompile(`(?im)^([ \t]*at[ \t]+[^\n]+\.[a-z]+):\d+(?::\d+)?(\)?)[ \t]*$`), "$1:#$2"},
	{regexp.MustCompile(`(?im)^([ \t]*location:[ \t]*['"]?[^\n'"]+\.[a-z]+):\d+(?::\d+)?(['"]?)[ \t]*$`), "$1:#$2"},
	{regexp.MustCompile(`(?m)^([ \t]*(?:#[ \t]*)?duration_ms:?[ \t]+)\d+(?:\.\d+)?[ \t]*$`), "${1}#"},
	{regexp.MustCompile(`(?m)^([ \t]*✕[ \t]+.+) \(\d+(?:\.\d+)? ms\)$`), "$1 (# ms)"},
	// Go: `--- FAIL: TestX (0.01s)`, `FAIL\tpkg\t0.2s`, `x_test.go:42:`, goroutine frames.
	{regexp.MustCompile(`(?m)^([ \t]*--- (?:FAIL|PASS|SKIP): \S+) \(\d+(?:\.\d+)?s\)$`), "$1 (#s)"},
	{regexp.MustCompile(`(?m)^((?:FAIL|ok)[ \t]+\S+[ \t]+)\d+(?:\.\d+)?s$`), "${1}#s"},
	{regexp.MustCompile(`(?m)^([ \t]+[\w./-]+\.go):\d+:`), "$1:#:"},
	{regexp.MustCompile(`(?m)^([ \t]+[\w./-]+\.go):\d+(?: \+0x[0-9a-f]+)?$`), "$1:#"},
	// Python: `File "x.py", line 12, in f` and pytest's `1 failed in 0.12s`.
	{regexp.MustCompile(`(?m)^([ \t]*File "[^"]+", line )\d+`), "${1}#"},
	{regexp.MustCompile(`\bin \d+(?:\.\d+)?s\b`), "in #s"},
}

// signatureOf is FNV-1a over the exit status and the normalized output: a
// bounded key that still tells two diagnostics apart.
func signatureOf(r result) string {
	text := strings.ReplaceAll(strings.ReplaceAll(r.body, "\r\n", "\n"), "\r", "\n")
	for _, n := range noise {
		text = n.re.ReplaceAllString(text, n.with)
	}
	h := fnv.New64a()
	h.Write([]byte("exit " + strconv.Itoa(r.exit) + "\n" + strings.TrimSpace(text)))
	return strconv.FormatUint(h.Sum64(), 16)
}

type toolError struct {
	index     int
	signature string
}

// repeatedFailure finds two failed tool outputs with the same signature, both
// in the trailing FailureWindow items, with an edit attempt between them: an
// apply_patch or a writing command that did not fail itself. The latest pair
// wins.
func repeatedFailure(items []responses.Item) *Failure {
	last := len(items) - 1
	var errs []toolError
	failed := map[string]bool{}
	for i, it := range items {
		if !isToolOutput(it) || last-i > FailureWindow {
			continue
		}
		if r := parseResult(it.Output); r.known && r.exit != 0 {
			errs = append(errs, toolError{index: i, signature: signatureOf(r)})
			failed[it.CallID] = true
		}
	}
	var edits []int
	for _, c := range calls(items, 0) {
		writes := c.shell && !c.readOnly() && !c.checks()
		if (c.edit || writes) && !failed[c.id] {
			edits = append(edits, c.index)
		}
	}
	for j := len(errs) - 1; j > 0; j-- {
		for i := j - 1; i >= 0; i-- {
			if errs[i].signature == errs[j].signature && editBetween(edits, errs[i].index, errs[j].index) {
				return &Failure{Signature: errs[j].signature, Index: errs[j].index}
			}
		}
	}
	return nil
}

func editBetween(edits []int, from, to int) bool {
	for _, k := range edits {
		if k > from && k < to {
			return true
		}
	}
	return false
}
