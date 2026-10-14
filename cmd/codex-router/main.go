// Command codex-router routes Codex CLI requests to a model and reasoning
// effort per step. See docs/architecture.md and docs/user-guide.md.
package main

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/alexei-led/codex-router/internal/brain"
	"github.com/alexei-led/codex-router/internal/config"
	"github.com/alexei-led/codex-router/internal/control"
	"github.com/alexei-led/codex-router/internal/home"
	"github.com/alexei-led/codex-router/internal/hook"
	"github.com/alexei-led/codex-router/internal/launcher"
	"github.com/alexei-led/codex-router/internal/link"
	"github.com/alexei-led/codex-router/internal/listen"
	"github.com/alexei-led/codex-router/internal/proxy"
	"github.com/alexei-led/codex-router/internal/registry"
	"github.com/alexei-led/codex-router/internal/route"
	"github.com/alexei-led/codex-router/internal/service"
	"github.com/alexei-led/codex-router/internal/setup"
	"github.com/alexei-led/codex-router/internal/tui"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

const envConfig = "CODEX_ROUTER_CONFIG"

const (
	exitOK    = 0
	exitError = 1
	exitUsage = 2
)

const usage = `codex-router runs Codex through a local proxy that picks a model and
reasoning effort for every step, and mirrors the choice into Codex's footer.

Usage:
  codex-router [--config PATH] [--] [codex args...]  run codex through the router
  codex-router tui [--session ID|LABEL] [--socket PATH]
                                                     show the router panel
  codex-router sessions [--json]                     list running sessions
  codex-router hook                                  Codex hook: print a line when the route switched
  codex-router [--config PATH] proxy [--port N]      run the proxy alone (debugging, codex exec)
  codex-router [--config PATH] edge [--home PATH] [--state-dir DIR]
                                                     serve a Codex home's provider port
  codex-router [--config PATH] brain [--home PATH] [--state-dir DIR]
                                                     route a Codex home's sessions
  codex-router [--config PATH] setup [--home PATH] [--state-dir DIR] [--dry-run]
               [--follow-symlink] [--undo [--purge]]
                                                     run a Codex home through an edge and a brain
  codex-router service ensure|status [--home PATH]   start or check a home's service units
  codex-router ctl status|pause|resume|pin [--home PATH] [--cwd DIR] [--tier TIER]
                                                     show or change one session's routing
  codex-router version                               print the version

Global flags:
  --config PATH  config file; else $CODEX_ROUTER_CONFIG; else
                 $XDG_CONFIG_HOME/codex-router/config.json (~/.config/...)
  --version      print the version
  -h, --help     show this help

Arguments after the router flags (or after --) go to codex unchanged. If the
router cannot start, it prints one line why and runs plain codex.
tui shows every running session of every Codex home, found in the session
registry; --socket shows only the endpoint at that control socket.
--session locks the panel to one session: an exact session or thread id,
else a unique prefix of one, else an exact label, else a label suffix that
starts at a " · " (for example "repo · branch" or a branch name).
sessions prints one line per session: id, home, label, mode, route; --json
prints the full session list with each session's endpoint_id.
proxy --port defaults to the config's proxy.port (0 = any free port) and
prints the codex -c flags that point at it.
edge holds the provider port of one Codex home (--home, else $CODEX_HOME,
else ~/.codex) and asks that home's brain for each step's route; with the
brain down it forwards requests unchanged. Its socket comes from launchd or
systemd; without one it binds the port codex-router setup stored and warns.
--state-dir is the base the service units pass, as $XDG_STATE_HOME (else
~/.local/state); the edge reads codex-router/homes/<home id>/ in it.
brain routes for one Codex home (same --home and --state-dir): it answers
that home's edge, follows the threads of the home's shared Codex daemon,
mirrors routes into their footers, and lists them in the session registry.
The classifier key comes from the env var named by the classifier's keyEnv.
When it is unset the classifier is off: requests keep their model unless a
route is pinned from the panel.
setup routes one Codex home (--home, else $CODEX_HOME, else ~/.codex)
through an edge and a brain, run by launchd (macOS) or systemd user units
(Linux). It prints the home, then the config.toml diff (token redacted):
model_provider = "router" and a router provider with a token header, in
marked blocks. It refuses a config.toml that sets model_provider, or that
is a symlink (--follow-symlink edits the target; the token then lands
there). It backs config.toml up, stores a free port and a new token in the
home's state dir, and installs the units with the home, the state dir
and the config path fixed. Running threads keep their provider until /new
or a Codex restart. --dry-run prints the changes and writes nothing.
--undo removes the blocks and the units; --purge also deletes the state.
service ensure starts the units of a set-up home that are down, without
waiting; for a home not set up it prints one hint line. service status
prints each unit's state.
ctl acts on one running session of a Codex home (--home, else $CODEX_HOME,
else ~/.codex), for the Codex plugin's skill: the session of
$CODEX_THREAD_ID, which Codex sets for its shell commands, else the one
whose cwd is the deepest equal to or containing --cwd (default: the current
dir). status prints the session's mode, tier and route; pause and resume
act on that session only; pin --tier T routes its next turn to tier T.

Exit codes:
  run      codex's exit code; 128+N when signal N ended codex;
           1 when neither routed nor plain codex could start
  tui      0 on quit, 1 on error or when no session matches --session,
           2 when several sessions match --session (they are listed)
  sessions 0, also when no session runs; 1 on error
  hook     0, also when the router is not running; 1 only when stdout fails
  proxy    0 after SIGINT or SIGTERM, 1 on error
  edge     0 after SIGINT or SIGTERM, 1 on error or when the home is not set up
  brain    0 after SIGINT or SIGTERM, 1 on error
  setup    0 on success, 1 on error or a refused edit
  service  0, also when the home is not set up; 1 on error
  ctl      0 on success, 1 on error or when no session matches,
           2 when several sessions match (they are listed)
  any      2 on a usage error
`

func main() {
	os.Exit(run(context.Background(), os.Args[1:], os.Environ(), os.Stdin, os.Stdout, os.Stderr))
}

type cli struct {
	env    []string
	stdin  io.Reader
	stdout io.Writer
	stderr io.Writer
}

func (c cli) getenv(key string) string {
	for _, kv := range slices.Backward(c.env) {
		if k, v, ok := strings.Cut(kv, "="); ok && k == key {
			return v
		}
	}
	return ""
}

func (c cli) fail(err error) int {
	_, _ = fmt.Fprintln(c.stderr, "codex-router:", err)
	return exitError
}

func (c cli) usageError(err error) int {
	_, _ = fmt.Fprintf(c.stderr, "codex-router: %v\nRun 'codex-router --help' for usage.\n", err)
	return exitUsage
}

func run(ctx context.Context, args, env []string, stdin io.Reader, stdout, stderr io.Writer) int {
	c := cli{env: env, stdin: stdin, stdout: stdout, stderr: stderr}
	g, err := parseGlobals(args)
	switch {
	case err != nil:
		return c.usageError(err)
	case g.help:
		_, _ = fmt.Fprint(stdout, usage)
		return exitOK
	case g.version:
		_, _ = fmt.Fprintln(stdout, version)
		return exitOK
	}
	if !g.codexOnly && len(g.rest) > 0 {
		sub, rest := g.rest[0], g.rest[1:]
		switch sub {
		case "version":
			_, _ = fmt.Fprintln(stdout, version)
			return exitOK
		case "hook":
			if err := hook.Run(stdin, stdout, c.getenv); err != nil {
				return c.fail(err)
			}
			return exitOK
		case "tui":
			return c.tui(ctx, rest)
		case "sessions":
			return c.sessions(rest)
		case "proxy":
			return c.proxy(ctx, g.config, rest)
		case "edge":
			return c.edge(ctx, g.config, rest)
		case "brain":
			return c.brain(ctx, g.config, rest)
		case "setup":
			return c.setup(ctx, g.config, rest)
		case "service":
			return c.service(ctx, rest)
		case "ctl":
			return c.ctl(rest)
		}
	}
	return c.launch(g.config, g.rest)
}

type globals struct {
	config    string
	help      bool
	version   bool
	codexOnly bool // a -- ended the router flags
	rest      []string
}

// parseGlobals reads router flags up to the first other argument, which
// starts a subcommand or the codex args. The flag package would reject
// codex's own flags, so this is by hand.
func parseGlobals(args []string) (globals, error) {
	var g globals
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--":
			g.codexOnly, g.rest = true, args[i+1:]
			return g, nil
		case a == "--config" || a == "-config":
			if i+1 >= len(args) {
				return g, errors.New("--config needs a path")
			}
			i++
			g.config = args[i]
		case strings.HasPrefix(a, "--config="):
			g.config = strings.TrimPrefix(a, "--config=")
		case a == "-h" || a == "--help":
			g.help = true
		case a == "--version":
			g.version = true
		default:
			g.rest = args[i:]
			return g, nil
		}
	}
	return g, nil
}

// configPath is the config file: --config, else $CODEX_ROUTER_CONFIG, else
// config.Path.
func (c cli) configPath(flagPath string) (string, error) {
	if path := cmp.Or(flagPath, c.getenv(envConfig)); path != "" {
		return path, nil
	}
	return config.Path()
}

func (c cli) loadConfig(flagPath string) (config.Config, error) {
	path, err := c.configPath(flagPath)
	if err != nil {
		return config.Config{}, err
	}
	return config.Load(path)
}

func (c cli) launch(configPath string, codexArgs []string) int {
	o := launcher.Options{CodexArgs: codexArgs, Env: c.env, Stderr: c.stderr}
	cfg, err := c.loadConfig(configPath)
	if err != nil {
		if err := launcher.Fallback(o, err); err != nil {
			return c.fail(err)
		}
		return exitOK
	}
	o.Config = cfg
	code, err := launcher.Run(context.Background(), o)
	if err != nil {
		return c.fail(err)
	}
	return code
}

// runPanel shows the panel; tests replace it, since the panel needs a
// terminal.
var runPanel = tui.Run

func (c cli) tui(ctx context.Context, args []string) int {
	fs := flag.NewFlagSet("tui", flag.ContinueOnError)
	fs.SetOutput(c.stderr)
	socket := fs.String("socket", "", "control socket `path` (default: every running session in the registry)")
	session := fs.String("session", "", "lock the panel to one session: its `id` or label (see codex-router sessions)")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if fs.NArg() > 0 {
		return c.usageError(fmt.Errorf("tui: unexpected argument %q", fs.Arg(0)))
	}
	o := tui.Options{Socket: *socket}
	if *socket == "" {
		root, err := registry.Root(c.getenv)
		if err != nil {
			return c.fail(err)
		}
		o.Registry = root
	}
	if *session != "" {
		all, err := findSessions(o.Socket, o.Registry)
		if err != nil {
			return c.fail(err)
		}
		switch matches := resolveSession(all, *session); len(matches) {
		case 0:
			return c.fail(fmt.Errorf("tui: no running session matches %q; see codex-router sessions", *session))
		case 1:
			o.Session = matches[0].ID
		default:
			_, _ = fmt.Fprintf(c.stderr, "codex-router: tui: %q matches %d sessions:\n", *session, len(matches))
			for _, s := range matches {
				_, _ = fmt.Fprintf(c.stderr, "  %s  %s\n", s.ID, s.Label)
			}
			return exitUsage
		}
	}
	ctx, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := runPanel(ctx, o); err != nil {
		return c.fail(err)
	}
	return exitOK
}

func (c cli) sessions(args []string) int {
	fs := flag.NewFlagSet("sessions", flag.ContinueOnError)
	fs.SetOutput(c.stderr)
	asJSON := fs.Bool("json", false, "print the session list as JSON")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if fs.NArg() > 0 {
		return c.usageError(fmt.Errorf("sessions: unexpected argument %q", fs.Arg(0)))
	}
	root, err := registry.Root(c.getenv)
	if err != nil {
		return c.fail(err)
	}
	all, err := findSessions("", root)
	if err != nil {
		return c.fail(err)
	}
	if *asJSON {
		enc := json.NewEncoder(c.stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(all); err != nil {
			return c.fail(err)
		}
		return exitOK
	}
	w := tabwriter.NewWriter(c.stdout, 0, 0, 2, ' ', 0)
	for _, s := range all {
		_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", s.ID, s.homeName, s.Label, s.Mode, routeText(s.Route))
	}
	if err := w.Flush(); err != nil {
		return c.fail(err)
	}
	return exitOK
}

// labelSep separates the parts of a session label (home.Label).
const labelSep = " · "

// listTimeout bounds the wait for an endpoint's first sessions list.
const listTimeout = 2 * time.Second

// listedSession is one running session with the endpoint serving it, as
// sessions --json prints it.
type listedSession struct {
	route.SessionInfo
	Endpoint string `json:"endpoint_id"`
	homeName string
	base     string // the label before Disambiguate, shared by colliding sessions
	socket   string // the endpoint's control socket
}

// findSessions lists the sessions of the endpoint at socket, or, when socket
// is "", of every endpoint in the registry at root.
func findSessions(socket, root string) ([]listedSession, error) {
	if socket != "" {
		return collectSessions([]registry.Endpoint{{Socket: socket}}, func(registry.Endpoint) {}), nil
	}
	eps, _, err := registry.List(root)
	if err != nil {
		return nil, err
	}
	return collectSessions(eps, func(ep registry.Endpoint) {
		if !registry.Alive(ep) {
			_ = registry.Remove(root, ep.ID)
		}
	}), nil
}

// collectSessions asks every endpoint for its sessions, in parallel, and
// returns them in panel order (home name, then most recently active) with
// the panel's labels. An endpoint that does not answer is skipped, and
// pruned when it is dead. A session two endpoints list is taken from the
// more recently active report.
func collectSessions(eps []registry.Endpoint, prune func(registry.Endpoint)) []listedSession {
	var mu sync.Mutex
	byID := map[string]listedSession{}
	var wg sync.WaitGroup
	for _, ep := range eps {
		wg.Go(func() {
			h, items, err := endpointSessions(ep)
			if err != nil {
				prune(ep)
				return
			}
			mu.Lock()
			defer mu.Unlock()
			for _, it := range items {
				if old, ok := byID[it.ID]; ok && !it.LastActive.After(old.LastActive) {
					continue
				}
				byID[it.ID] = listedSession{SessionInfo: it, Endpoint: h.EndpointID, homeName: h.Home.Name, socket: ep.Socket}
			}
		})
	}
	wg.Wait()
	out := slices.AppendSeq(make([]listedSession, 0, len(byID)), maps.Values(byID))
	slices.SortFunc(out, func(a, b listedSession) int {
		return cmp.Or(
			cmp.Compare(a.homeName, b.homeName), cmp.Compare(a.Home, b.Home),
			b.LastActive.Compare(a.LastActive), cmp.Compare(a.ID, b.ID))
	})
	labels, ids := make([]string, len(out)), make([]string, len(out))
	for i, s := range out {
		labels[i], ids[i] = s.Label, s.ID
	}
	for i, l := range home.Disambiguate(labels, ids) {
		out[i].base, out[i].Label = out[i].Label, l
	}
	return out
}

// endpointSessions dials ep and returns its hello and first sessions list.
// A hello naming another endpoint or home than the entry is an error: the
// entry is stale (M3). An entry without a home (--socket) is not checked.
func endpointSessions(ep registry.Endpoint) (control.Hello, []route.SessionInfo, error) {
	c, err := control.Dial(ep.Socket)
	if err != nil {
		return control.Hello{}, nil, err
	}
	defer func() { _ = c.Close() }()
	h := c.Hello()
	if ep.Home.ID != "" && (h.EndpointID != ep.ID || h.Home.ID != ep.Home.ID) {
		return h, nil, errors.New("stale registry entry")
	}
	select {
	case items, ok := <-c.Sessions():
		if !ok {
			return h, nil, control.ErrDisconnected
		}
		return h, items, nil
	case <-time.After(listTimeout):
		return h, nil, errors.New("no sessions list")
	}
}

// resolveSession returns the sessions query names under the first rule that
// matches any: an exact session or thread id; a session or thread id
// prefix; an exact label; a label suffix starting at a separator. Label
// rules try the shown label and the one before disambiguation, so a label
// that several sessions share names all of them. One match resolves;
// several are ambiguous.
func resolveSession(sessions []listedSession, query string) []listedSession {
	rules := []func(listedSession) bool{
		func(s listedSession) bool { return s.ID == query || s.ThreadID == query },
		func(s listedSession) bool {
			return strings.HasPrefix(s.ID, query) || strings.HasPrefix(s.ThreadID, query)
		},
		func(s listedSession) bool { return s.Label == query || s.base == query },
		func(s listedSession) bool {
			return strings.HasSuffix(s.Label, labelSep+query) || strings.HasSuffix(s.base, labelSep+query)
		},
	}
	for _, match := range rules {
		var got []listedSession
		for _, s := range sessions {
			if match(s) {
				got = append(got, s)
			}
		}
		if len(got) > 0 {
			return got
		}
	}
	return nil
}

// routeText formats a route as the panel does.
func routeText(r config.Route) string {
	switch {
	case r.Model == "":
		return "-"
	case r.Effort == "":
		return r.Model
	}
	return r.Model + " · " + string(r.Effort)
}

// envThread is the thread id Codex sets in the env of its shell commands.
const envThread = "CODEX_THREAD_ID"

// ctlOps maps ctl's subcommands to commands; status sends none.
var ctlOps = map[string]route.Op{"status": "", "pause": route.OpPause, "resume": route.OpResume, "pin": route.OpPin}

// ctl shows or changes the routing of one session of a Codex home, for the
// plugin's skill, which runs it from a Codex shell command: the session of
// $CODEX_THREAD_ID when a router of the home lists it, else the one whose
// cwd is the deepest equal to or containing --cwd.
func (c cli) ctl(args []string) int {
	var sub string
	if len(args) > 0 {
		sub = args[0]
	}
	op, known := ctlOps[sub]
	if !known {
		return c.usageError(errors.New("ctl: want status, pause, resume or pin"))
	}
	fs := flag.NewFlagSet("ctl "+sub, flag.ContinueOnError)
	cwd := fs.String("cwd", "", "the session's working `dir` (default: the current dir)")
	tier := new(string)
	if op == route.OpPin {
		tier = fs.String("tier", "", "`tier` of the next turn: micro, low, medium or high")
	}
	c, h, code, ok := c.homeFlags(fs, args[1:], false)
	if !ok {
		return code
	}
	if op == route.OpPin && !slices.Contains(config.Tiers, config.Tier(*tier)) {
		return c.usageError(fmt.Errorf("ctl pin: --tier %q is not micro, low, medium or high", *tier))
	}
	dir := *cwd
	if dir == "" {
		wd, err := os.Getwd()
		if err != nil {
			return c.fail(err)
		}
		dir = wd
	}
	dir, err := filepath.Abs(dir)
	if err != nil {
		return c.fail(err)
	}
	root, err := registry.Root(c.getenv)
	if err != nil {
		return c.fail(err)
	}
	all, err := findSessions("", root)
	if err != nil {
		return c.fail(err)
	}
	all = slices.DeleteFunc(all, func(s listedSession) bool { return s.Home != h.ID })
	matches := shellSessions(all, c.getenv(envThread), dir)
	switch len(matches) {
	case 0:
		return c.fail(fmt.Errorf("ctl: no running session of Codex home %s works in %s; see codex-router sessions", h.Name, dir))
	case 1:
	default:
		_, _ = fmt.Fprintf(c.stderr, "codex-router: ctl: %d sessions of Codex home %s work in %s:\n", len(matches), h.Name, dir)
		for _, s := range matches {
			_, _ = fmt.Fprintf(c.stderr, "  %s  %s\n", s.ID, s.Label)
		}
		return exitUsage
	}
	s := matches[0]
	if op == "" {
		return c.printSession(s)
	}
	cmd := route.Command{Op: op, Session: s.ID, Scope: route.ScopeSession, Tier: config.Tier(*tier)}
	if err := sendCommand(s, cmd); err != nil {
		return c.fail(fmt.Errorf("ctl %s: %w", sub, err))
	}
	switch op {
	case route.OpPause:
		_, _ = fmt.Fprintf(c.stdout, "paused routing of %s: requests keep the model in Codex's footer\n", s.Label)
	case route.OpResume:
		_, _ = fmt.Fprintf(c.stdout, "resumed routing of %s\n", s.Label)
	default:
		_, _ = fmt.Fprintf(c.stdout, "pinned the next turn of %s to %s\n", s.Label, *tier)
	}
	return exitOK
}

// shellSessions returns the session of thread when one is listed, else the
// sessions whose cwd is the deepest one equal to or containing dir. Paths
// are compared with symlinks resolved: a shell may see /tmp where Codex
// reports /private/tmp.
func shellSessions(sessions []listedSession, thread, dir string) []listedSession {
	if thread != "" {
		if i := slices.IndexFunc(sessions, func(s listedSession) bool { return s.ThreadID == thread }); i >= 0 {
			return sessions[i : i+1]
		}
	}
	dir = canonicalPath(dir)
	var best []listedSession
	depth := -1
	for _, s := range sessions {
		cwd := canonicalPath(s.Cwd)
		if !filepath.IsAbs(cwd) {
			continue
		}
		rel, err := filepath.Rel(cwd, dir)
		if err != nil || rel == ".." || strings.HasPrefix(rel, "../") {
			continue
		}
		switch {
		case len(cwd) > depth:
			best, depth = []listedSession{s}, len(cwd)
		case len(cwd) == depth:
			best = append(best, s)
		}
	}
	return best
}

// canonicalPath resolves symlinks in p, or cleans p when it does not exist.
func canonicalPath(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return filepath.Clean(p)
}

// printSession prints s for ctl status, one field per line.
func (c cli) printSession(s listedSession) int {
	w := tabwriter.NewWriter(c.stdout, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintf(w, "session\t%s\nlabel\t%s\nmode\t%s\ntier\t%s\nroute\t%s\nreplies\t%d\n",
		s.ID, s.Label, s.Mode, cmp.Or(string(s.Tier), "-"), routeText(s.Route), s.Replies)
	if s.Attention != "" {
		_, _ = fmt.Fprintf(w, "attention\t%s\n", s.Attention)
	}
	if err := w.Flush(); err != nil {
		return c.fail(err)
	}
	return exitOK
}

// sendCommand sends cmd to the endpoint that listed s.
func sendCommand(s listedSession, cmd route.Command) error {
	cl, err := control.Dial(s.socket)
	if err != nil {
		return err
	}
	defer func() { _ = cl.Close() }()
	if cl.Hello().EndpointID != s.Endpoint {
		return errors.New("the session's router changed; try again")
	}
	return cl.Send(cmd)
}

func (c cli) proxy(ctx context.Context, configPath string, args []string) int {
	fs := flag.NewFlagSet("proxy", flag.ContinueOnError)
	fs.SetOutput(c.stderr)
	port := fs.Int("port", -1, "loopback `port` (default: config proxy.port; 0 = any free port)")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if fs.NArg() > 0 {
		return c.usageError(fmt.Errorf("proxy: unexpected argument %q", fs.Arg(0)))
	}
	cfg, err := c.loadConfig(configPath)
	if err != nil {
		return c.fail(err)
	}
	if *port >= 0 {
		cfg.Proxy.Port = *port
	}
	ctx, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	o := launcher.Options{Config: cfg, Env: c.env, Stderr: c.stderr}
	err = launcher.Serve(ctx, o, func(p int) {
		_, _ = fmt.Fprintf(c.stdout, "proxy listening on 127.0.0.1:%d; point codex at it with:\n", p)
		for _, v := range launcher.ProviderOverrides(p) {
			_, _ = fmt.Fprintf(c.stdout, "  -c '%s'\n", v)
		}
		_, _ = fmt.Fprintln(c.stdout, "this proxy checks no token: any local user can reach it; use it for debugging only")
	})
	if err != nil {
		return c.fail(err)
	}
	return exitOK
}

// edgeUpstream is the edge's backend; empty is proxy.Upstream. Tests point
// it at a fake.
var edgeUpstream string

// homeFlags adds --home, and with withState --state-dir, to fs, then parses
// args. A set flag overrides CODEX_HOME or XDG_STATE_HOME in the returned
// cli's env. On failure it returns the exit code and false.
func (c cli) homeFlags(fs *flag.FlagSet, args []string, withState bool) (cli, home.Home, int, bool) {
	name := fs.Name()
	fs.SetOutput(c.stderr)
	homePath := fs.String("home", "", "Codex home `path` (default: $CODEX_HOME, else ~/.codex)")
	stateDir := new(string)
	if withState {
		stateDir = fs.String("state-dir", "", "state base `dir`, as $XDG_STATE_HOME (default: $XDG_STATE_HOME, else ~/.local/state)")
	}
	if err := fs.Parse(args); err != nil {
		return c, home.Home{}, exitUsage, false
	}
	if fs.NArg() > 0 {
		return c, home.Home{}, c.usageError(fmt.Errorf("%s: unexpected argument %q", name, fs.Arg(0))), false
	}
	for _, f := range []struct{ name, value, env string }{
		{"--home", *homePath, "CODEX_HOME"},
		{"--state-dir", *stateDir, "XDG_STATE_HOME"},
	} {
		if f.value == "" {
			continue
		}
		if !filepath.IsAbs(f.value) {
			return c, home.Home{}, c.usageError(fmt.Errorf("%s: %s %q is not an absolute path", name, f.name, f.value)), false
		}
		c.env = append(slices.Clip(c.env), f.env+"="+f.value)
	}
	h, err := home.Resolve(c.getenv)
	if err != nil {
		return c, home.Home{}, c.fail(err), false
	}
	return c, h, exitOK, true
}

func (c cli) edge(ctx context.Context, configPath string, args []string) int {
	c, h, code, ok := c.homeFlags(flag.NewFlagSet("edge", flag.ContinueOnError), args, true)
	if !ok {
		return code
	}
	dir, token, err := edgeState(c.getenv, h)
	if err != nil {
		return c.fail(err)
	}
	log := slog.New(slog.NewJSONHandler(c.stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
	cfg, err := c.loadConfig(configPath)
	if err != nil {
		// Codex has no other way to its backend: forward with defaults
		// rather than leave the port dead.
		log.Warn("config-unreadable", "err", err)
		cfg = config.Default()
	}
	ln, err := listen.Activated(service.EdgeSocket)
	var addr string
	switch {
	case errors.Is(err, listen.ErrNotActivated):
		port, perr := readPort(dir)
		if perr != nil {
			return c.fail(perr)
		}
		addr = net.JoinHostPort(proxy.LoopbackHost, strconv.Itoa(port))
		log.Warn("edge-self-bind", "port", port, "why", err.Error(),
			"note", "the port is free while the edge is down; run codex-router setup to install the service units")
	case err != nil:
		return c.fail(err)
	}
	lc := link.NewClient(link.ClientOptions{
		Path: brain.LinkPath(dir), HomeID: h.ID, Timeout: cfg.DecideTimeout(), Logger: log,
	})
	defer func() { _ = lc.Close() }()
	srv := proxy.New(proxy.Options{
		Addr:          addr,
		Listener:      ln,
		Token:         token,
		Upstream:      edgeUpstream,
		Decider:       lc,
		Observer:      lc,
		Logger:        log,
		DecideTimeout: cfg.DecideTimeout(),
		Health: func(ctx context.Context) string {
			if lc.Up(ctx) {
				return "brain: up"
			}
			return "brain: down"
		},
	})
	port, err := srv.Listen()
	if err != nil {
		return c.fail(err)
	}
	log.Info("edge-ready", "home", h.ID, "port", port, "activated", ln != nil)
	ctx, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := srv.Serve(ctx); err != nil {
		return c.fail(err)
	}
	return exitOK
}

func (c cli) brain(ctx context.Context, configPath string, args []string) int {
	c, h, code, ok := c.homeFlags(flag.NewFlagSet("brain", flag.ContinueOnError), args, true)
	if !ok {
		return code
	}
	cfg, err := c.loadConfig(configPath)
	if err != nil {
		return c.fail(err)
	}
	log := slog.New(slog.NewJSONHandler(c.stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
	deps, _ := launcher.RouterDeps(launcher.Options{Config: cfg, Env: c.env, Stderr: c.stderr}, log)
	ctx, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := brain.Run(ctx, brain.Options{Home: h, Deps: deps, Getenv: c.getenv}); err != nil {
		return c.fail(err)
	}
	return exitOK
}

// edgeState returns h's stage 2 state dir and the token in it. The dir and
// its parents up to codex-router/ must be private to this user: whoever can
// write there can swap the token.
func edgeState(getenv func(string) string, h home.Home) (dir, token string, err error) {
	app, err := registry.StateDir(getenv)
	if err != nil {
		return "", "", err
	}
	dir = brain.HomeDir(app, h.ID)
	for _, d := range []string{app, filepath.Dir(dir), dir} {
		if err := registry.CheckPrivate(d); err != nil {
			return "", "", fmt.Errorf("edge: home %s is not set up (run codex-router setup --home %s): %w", h.Name, h.Path, err)
		}
	}
	b, err := os.ReadFile(filepath.Join(dir, setup.TokenFile))
	if err != nil {
		return "", "", fmt.Errorf("edge: token: %w", err)
	}
	if token = strings.TrimSpace(string(b)); token == "" {
		return "", "", fmt.Errorf("edge: token file %s is empty", filepath.Join(dir, setup.TokenFile))
	}
	return dir, token, nil
}

// readPort reads the provider port setup stored in dir.
func readPort(dir string) (int, error) {
	path := filepath.Join(dir, setup.PortFile)
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, fmt.Errorf("edge: port: %w", err)
	}
	p, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil || p < 1 || p > 65535 {
		return 0, fmt.Errorf("edge: %s does not hold a port", path)
	}
	return p, nil
}

// executable is the binary the service units run; tests replace it.
var executable = os.Executable

// serviceRunner runs launchctl and systemctl; tests replace it.
var serviceRunner service.Runner = service.Exec

// ensureTimeout bounds service ensure, which a Codex hook runs at session
// start.
const ensureTimeout = 2 * time.Second

func (c cli) services() (*service.Manager, error) {
	return service.New(service.Options{
		GOOS: runtime.GOOS, Home: c.getenv("HOME"), ConfigHome: c.getenv("XDG_CONFIG_HOME"),
		UID: os.Getuid(), Run: serviceRunner,
	})
}

func (c cli) setup(ctx context.Context, configPath string, args []string) int {
	fs := flag.NewFlagSet("setup", flag.ContinueOnError)
	dryRun := fs.Bool("dry-run", false, "print the changes and write nothing")
	undo := fs.Bool("undo", false, "remove the config.toml blocks and the service units")
	purge := fs.Bool("purge", false, "with --undo, also delete the home's state dir")
	follow := fs.Bool("follow-symlink", false, "edit the target of a symlinked config.toml; the token lands there")
	c, h, code, ok := c.homeFlags(fs, args, true)
	if !ok {
		return code
	}
	if *purge && !*undo {
		return c.usageError(errors.New("setup: --purge needs --undo"))
	}
	app, err := registry.StateDir(c.getenv)
	if err != nil {
		return c.fail(err)
	}
	cfgPath, err := c.configPath(configPath)
	if err == nil {
		cfgPath, err = filepath.Abs(cfgPath)
	}
	if err != nil {
		return c.fail(err)
	}
	bin, err := executable()
	if err != nil {
		return c.fail(err)
	}
	mgr, err := c.services()
	if err != nil {
		return c.fail(err)
	}
	err = setup.Run(ctx, setup.Options{
		Home: h, StateDir: filepath.Dir(app), Config: cfgPath, Bin: bin,
		DryRun: *dryRun, Undo: *undo, Purge: *purge, FollowSymlink: *follow,
		Service: mgr, Out: c.stdout,
	})
	if err != nil {
		return c.fail(err)
	}
	return exitOK
}

func (c cli) service(ctx context.Context, args []string) int {
	if len(args) == 0 || (args[0] != "ensure" && args[0] != "status") {
		return c.usageError(errors.New("service: want ensure or status"))
	}
	sub := args[0]
	c, h, code, ok := c.homeFlags(flag.NewFlagSet("service "+sub, flag.ContinueOnError), args[1:], false)
	if !ok {
		return code
	}
	mgr, err := c.services()
	if err != nil {
		return c.fail(err)
	}
	if sub == "ensure" {
		ctx, cancel := context.WithTimeout(ctx, ensureTimeout)
		defer cancel()
		got, err := mgr.Ensure(ctx, h.ID)
		if err != nil {
			return c.fail(err)
		}
		if got == service.NotSetUp {
			_, _ = fmt.Fprintf(c.stdout, "codex-router: Codex home %s has no router service; run: codex-router setup --home %s\n", h.Name, h.Path)
		}
		return exitOK
	}
	_, _ = fmt.Fprintf(c.stdout, "Codex home %s: %s (id %s)\n", h.Name, h.Path, h.ID)
	w := tabwriter.NewWriter(c.stdout, 0, 0, 2, ' ', 0)
	for _, u := range mgr.Status(ctx, h.ID) {
		_, _ = fmt.Fprintf(w, "%s\t%s\t%s\n", u.Role, u.Name, u.State)
	}
	if err := w.Flush(); err != nil {
		return c.fail(err)
	}
	return exitOK
}
