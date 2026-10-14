package facts

import (
	"encoding/json"
	"path"
	"regexp"
	"strconv"
	"strings"

	"github.com/alexei-led/codex-router/internal/responses"
)

const (
	toolApplyPatch = "apply_patch"
	toolCodeMode   = "exec"
	typeCustomTool = "custom_tool_call"
	typeLocalShell = "local_shell_call"
)

var (
	callTypes   = map[string]bool{"function_call": true, typeCustomTool: true, typeLocalShell: true}
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
	case it.Name == toolCodeMode && it.Type == typeCustomTool:
		c = cellCall(c, it.Arguments)
	}
	return c, true
}

var (
	nestedCall = regexp.MustCompile(`\btools\.(exec_command|shell_command|shell|apply_patch)\s*\(`)
	commandKey = regexp.MustCompile(`\b(?:cmd|command)\s*:\s*`)
)

// cellCall reads the nested tool calls of a code-mode `exec` cell, a
// JavaScript program, as one call: an edit if any nested call edits, with its
// commands joined by newlines. So the cell reads only if every command reads,
// and it is a check only with a single check command, as its printed exit
// status may belong to any of them. Only `tools.NAME(` calls are seen, not
// `tools["NAME"]` or aliases; a command that is not a string literal (a
// variable, an interpolation, a `shell` argv) makes the cell unreadable.
func cellCall(c call, js string) call {
	locs := nestedCall.FindAllStringSubmatchIndex(js, -1)
	var commands []string
	c.ok = true
	for i, loc := range locs {
		end := len(js)
		if i+1 < len(locs) {
			end = locs[i+1][0]
		}
		args := js[loc[1]:end]
		if js[loc[2]:loc[3]] == toolApplyPatch {
			c.edit = true
			if q := strings.IndexAny(args, "\"'`"); q >= 0 {
				p, _ := jsString(args[q:])
				c.paths = append(c.paths, patchPaths(p)...)
			}
			continue
		}
		command, ok := "", false
		if k := commandKey.FindStringIndex(args); k != nil {
			command, ok = jsString(args[k[1]:])
		}
		switch {
		case !ok:
			c.shell, c.ok = true, false
		case applyPatchCommand.MatchString(command):
			c.edit, c.paths = true, append(c.paths, patchPaths(command)...)
		default:
			c.shell = true
			commands = append(commands, command)
		}
	}
	c.command = strings.Join(commands, "\n")
	return c
}

var jsEscapes = map[byte]string{'n': "\n", 't': "\t", 'r': "\r", 'b': "\b", 'f': "\f", 'v': "\v", '0': "\x00", '\n': ""}

// jsString decodes the JavaScript string literal s starts with: "double",
// 'single', or a `template` without substitutions.
func jsString(s string) (string, bool) {
	if s == "" || !strings.ContainsRune("\"'`", rune(s[0])) {
		return "", false
	}
	quote := s[0]
	var b strings.Builder
	for i := 1; i < len(s); i++ {
		switch {
		case s[i] == quote:
			return b.String(), true
		case quote == '`' && strings.HasPrefix(s[i:], "${"):
			return "", false
		case s[i] != '\\':
			b.WriteByte(s[i])
			continue
		}
		if i++; i == len(s) {
			return "", false
		}
		if e, ok := jsEscapes[s[i]]; ok {
			b.WriteString(e)
			continue
		}
		hex := ""
		switch s[i] {
		case 'x':
			hex = s[i+1 : min(i+3, len(s))]
		case 'u':
			hex = s[i+1 : min(i+5, len(s))]
			if strings.HasPrefix(hex, "{") {
				hex, _, _ = strings.Cut(s[i+2:], "}")
				i += 2 // the braces
			}
		default:
			b.WriteByte(s[i])
			continue
		}
		r, err := strconv.ParseUint(hex, 16, 32)
		if err != nil {
			return "", false
		}
		b.WriteRune(rune(r))
		i += len(hex)
	}
	return "", false
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
