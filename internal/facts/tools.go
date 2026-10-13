package facts

import (
	"encoding/json"
	"path"
	"regexp"
	"strings"

	"github.com/alexei-led/codex-router/internal/responses"
)

const (
	toolApplyPatch = "apply_patch"
	typeLocalShell = "local_shell_call"
)

var (
	callTypes   = map[string]bool{"function_call": true, "custom_tool_call": true, typeLocalShell: true}
	outputTypes = map[string]bool{"function_call_output": true, "custom_tool_call_output": true}
	// Function tools whose arguments carry a command: `shell` (argv),
	// `shell_command` (string), `exec_command` (unified exec, `cmd`).
	shellTools = map[string]bool{"shell": true, "shell_command": true, "exec_command": true}
	shells     = map[string]bool{"bash": true, "sh": true, "zsh": true}
)

// call is what a tool call item says about the work, with no text kept
// beyond the command and the patched paths.
type call struct {
	index   int
	id      string
	edit    bool     // apply_patch, as a tool or through a shell
	paths   []string // files an edit touches
	shell   bool     // a command that is not apply_patch
	command string   // the shell script; "" with ok false when unreadable
	ok      bool
}

// readOnly and checks need a readable command: an unreadable one counts as
// running something unknown.
func (c call) readOnly() bool { return c.ok && readsOnly(c.command) }
func (c call) checks() bool   { return c.ok && verifies(c.command) }

func isToolOutput(it responses.Item) bool { return outputTypes[it.Type] }

func callOf(index int, it responses.Item) (call, bool) {
	if !callTypes[it.Type] {
		return call{}, false
	}
	c := call{index: index, id: it.CallID}
	switch {
	case it.Type == typeLocalShell || shellTools[it.Name]:
		c.command, c.ok = commandOf(it.Arguments)
		c.shell = true
		if c.ok && applyPatchCommand.MatchString(c.command) {
			c.shell, c.edit, c.paths = false, true, patchPaths(c.command)
		}
	case it.Name == toolApplyPatch:
		c.edit, c.paths = true, patchPaths(patchText(it))
	}
	return c, true
}

func calls(items []responses.Item, from int) []call {
	var out []call
	for i := max(0, from); i < len(items); i++ {
		if c, ok := callOf(i, items[i]); ok {
			out = append(out, c)
		}
	}
	return out
}

// commandOf reads the command from shell tool arguments: `cmd`, `command` as
// a string or argv, or a local_shell_call `action.command` argv.
func commandOf(args string) (string, bool) {
	var a struct {
		Cmd     string          `json:"cmd"`
		Command json.RawMessage `json:"command"`
		Action  struct {
			Command json.RawMessage `json:"command"`
		} `json:"action"`
	}
	if json.Unmarshal([]byte(args), &a) != nil {
		return "", false
	}
	if a.Cmd != "" {
		return a.Cmd, true
	}
	raw := a.Command
	if len(raw) == 0 {
		raw = a.Action.Command
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s, true
	}
	var argv []string
	if json.Unmarshal(raw, &argv) == nil && len(argv) > 0 {
		return script(argv), true
	}
	return "", false
}

var plainArg = regexp.MustCompile(`^[\w./=:,@%+-]+$`)

// script turns argv into the shell text it runs: the script of `bash -lc`,
// else the words joined with anything non-plain single-quoted.
func script(argv []string) string {
	if len(argv) == 3 && shells[path.Base(argv[0])] && (argv[1] == "-c" || argv[1] == "-lc") {
		return argv[2]
	}
	words := make([]string, len(argv))
	for i, w := range argv {
		if plainArg.MatchString(w) {
			words[i] = w
		} else {
			words[i] = "'" + strings.ReplaceAll(w, "'", "") + "'"
		}
	}
	return strings.Join(words, " ")
}

var applyPatchCommand = regexp.MustCompile(`^\s*apply_patch\b`)

// patchText is the patch of an apply_patch call: the `input` argument of the
// function tool, or the freeform input of the custom tool.
func patchText(it responses.Item) string {
	var a struct {
		Input string `json:"input"`
	}
	if json.Unmarshal([]byte(it.Arguments), &a) == nil && a.Input != "" {
		return a.Input
	}
	if it.Arguments != "" {
		return it.Arguments
	}
	return it.Text
}

var patchHeaders = []string{"*** Add File: ", "*** Update File: ", "*** Delete File: ", "*** Move to: "}

func patchPaths(patch string) []string {
	var paths []string
	for line := range strings.Lines(patch) {
		line = strings.TrimSpace(line)
		for _, h := range patchHeaders {
			if p, ok := strings.CutPrefix(line, h); ok && p != "" {
				paths = append(paths, p)
			}
		}
	}
	return paths
}

var docPath = regexp.MustCompile(`(?i)\.(?:md|mdx|rst|txt)$|(?:^|[\\/])docs[\\/]`)

// observe buckets the tool calls from index `from` on: code, docs, ops, read
// or talk. First match wins; only names, commands and paths are read.
func observe(items []responses.Item, from int) Observed {
	cs := calls(items, from)
	edited, docs := false, true
	for _, c := range cs {
		if c.edit {
			edited = true
			docs = docs && len(c.paths) > 0 && allDocs(c.paths)
		}
	}
	if edited {
		if docs {
			return ObservedDocs
		}
		return ObservedCode
	}
	for _, c := range cs {
		if c.shell && !c.readOnly() {
			return ObservedOps
		}
	}
	if len(cs) > 0 {
		return ObservedRead
	}
	return ObservedTalk
}

func allDocs(paths []string) bool {
	for _, p := range paths {
		if !docPath.MatchString(p) {
			return false
		}
	}
	return true
}

var (
	quoted       = regexp.MustCompile(`'[^']*'|"[^"]*"`)
	fdRedirect   = regexp.MustCompile(`\d?>\s*&\d|\d?>\s*/dev/null`)
	substitution = regexp.MustCompile("`|\\$\\(")
	separators   = regexp.MustCompile(`&&|\|\||[;|&\n]`)
	hidesStatus  = regexp.MustCompile(`\|\||[;|&\n]`)
	// find can run or delete what it finds.
	writingFind  = regexp.MustCompile(`\s-(?:exec|execdir|ok|okdir|delete)\b`)
	readCommands = map[string]bool{
		"cat": true, "ls": true, "grep": true, "rg": true, "find": true, "head": true, "tail": true, "cd": true,
		"nl": true, "wc": true, "pwd": true,
	}
	readGit = map[string]bool{"status": true, "log": true, "diff": true, "show": true}
)

// bare blanks quoted text and drops fd redirections, so a `;` or `>` inside a
// pattern is not taken for shell syntax.
func bare(command string) string {
	return fdRedirect.ReplaceAllString(quoted.ReplaceAllString(command, `""`), "")
}

// readsOnly says every command of a pipeline or list only reads. Command
// substitution runs inside double quotes, so it is looked for in the whole
// command. Anything unrecognised counts as running something.
func readsOnly(command string) bool {
	if substitution.MatchString(command) {
		return false
	}
	b := bare(command)
	if strings.ContainsAny(b, "<>") || writingFind.MatchString(b) {
		return false
	}
	for _, part := range separators.Split(b, -1) {
		if words := strings.Fields(part); len(words) > 0 && !readsWords(words) {
			return false
		}
	}
	return true
}

func readsWords(words []string) bool {
	switch words[0] {
	case "git":
		return len(words) > 1 && readGit[words[1]]
	case "sed":
		// `sed -n '1,80p' f` is how Codex reads files; any flag with an i may edit in place.
		printOnly := false
		for _, w := range words[1:] {
			if strings.HasPrefix(w, "-") && strings.Contains(w, "i") {
				return false
			}
			printOnly = printOnly || w == "-n"
		}
		return printOnly
	}
	return readCommands[words[0]]
}

// A command whose success shows that a check passed: a checker run, or a
// launcher whose subcommand names a check (`npm test`, `go test`, `make lint`,
// `uv run pytest`, `python -m pytest`, `node --test`), alone or in an `&&`
// list, so the command's status is the check's. A pipeline, `;`, `||` or
// background job can hide a failed check, so none of them counts. Neither do
// package names, paths and file names: `npm install -D vitest`.
var (
	checkers = map[string]bool{
		"pytest": true, "jest": true, "vitest": true, "mocha": true, "tsc": true, "eslint": true, "biome": true,
		"ruff": true, "mypy": true, "pyright": true, "golangci-lint": true,
	}
	launchers = map[string]bool{
		"npm": true, "pnpm": true, "yarn": true, "bun": true, "npx": true, "bunx": true, "uv": true, "uvx": true,
		"poetry": true, "cargo": true, "go": true, "make": true, "just": true, "gradle": true, "gradlew": true,
		"mvn": true, "dotnet": true, "swift": true, "deno": true, "node": true, "python": true, "python3": true,
		"tox": true,
	}
	runVerbs  = map[string]bool{"run": true, "exec": true, "x": true, "dlx": true}
	checkWord = regexp.MustCompile(
		`\b(?:test|tests|pytest|jest|vitest|mocha|tsc|typecheck|lint|eslint|biome|ruff|mypy|pyright|clippy|vet|build|check|validate)\b`)
	// A version or help request runs no check.
	infoArg   = regexp.MustCompile(`^(?:--version|-V|--help|-h|help|version)$`)
	envAssign = regexp.MustCompile(`^\w+=`)
	testFlag  = regexp.MustCompile(`^--test\b`)
	extension = regexp.MustCompile(`\.\w+$`)
)

func verifies(command string) bool {
	steps := strings.Split(bare(command), "&&")
	for _, s := range steps {
		if hidesStatus.MatchString(s) {
			return false
		}
	}
	for _, s := range steps {
		if runsCheck(strings.Fields(s)) {
			return true
		}
	}
	return false
}

// runsCheck reads one simple command. An option before the subcommand may
// take a value (`npm --prefix test install`), so the subcommand must come
// first; `python -m` is the one option form read.
func runsCheck(words []string) bool {
	for len(words) > 1 && envAssign.MatchString(words[0]) {
		words = words[1:]
	}
	if len(words) == 0 {
		return false
	}
	name, rest := path.Base(words[0]), words[1:]
	for _, w := range rest {
		if infoArg.MatchString(w) {
			return false
		}
	}
	if checkers[name] {
		return true
	}
	if !launchers[name] {
		return false
	}
	for _, w := range rest {
		if testFlag.MatchString(w) {
			return true
		}
	}
	args := rest
	if (name == "python" || name == "python3") && len(args) > 0 && args[0] == "-m" {
		args = args[1:]
	}
	if len(args) > 0 && runVerbs[args[0]] {
		args = args[1:]
	}
	if len(args) == 0 {
		return false
	}
	sub := args[0]
	return !strings.HasPrefix(sub, "-") && !strings.Contains(sub, "/") && !extension.MatchString(sub) &&
		checkWord.MatchString(sub)
}

// verifiedSince says the last verification command at or after index `from`
// passed: exit 0 in its output. False when none ran, it failed, it has no
// output yet, or it was still running when the output came back (unified exec
// yielded): that output reports a launch, not a check.
func verifiedSince(items []responses.Item, from int) bool {
	last := call{index: -1}
	for _, c := range calls(items, from) {
		if c.shell && c.checks() {
			last = c
		}
	}
	if last.index < 0 {
		return false
	}
	for _, it := range items[last.index+1:] {
		if isToolOutput(it) && it.CallID == last.id {
			r := parseResult(it.Output)
			return r.known && r.exit == 0 && !r.running
		}
	}
	return false
}
