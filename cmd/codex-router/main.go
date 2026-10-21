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
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/alexei-led/codex-router/internal/brain"
	"github.com/alexei-led/codex-router/internal/catalog"
	"github.com/alexei-led/codex-router/internal/classifier"
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
	"github.com/alexei-led/codex-router/internal/sessions"
	"github.com/alexei-led/codex-router/internal/setup"
	"github.com/alexei-led/codex-router/internal/statedir"
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
  codex-router tui [--session ID|LABEL] [--socket PATH] [--no-mouse]
                                                     show the router panel
  codex-router sessions [--json]                     list running sessions
  codex-router [--config PATH] doctor [--home PATH] [--state-dir DIR] [--json]
                                                     read-only operational diagnostics
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
  codex-router ctl mode cost|balanced|quality|default [--home PATH] [--cwd DIR]
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
--no-mouse leaves the mouse to the terminal, so text selection works; the
panel otherwise takes clicks on tabs, sessions, pins and modes.
sessions prints one line per session: id, home, label, mode, route; --json
prints the full session list with each session's endpoint_id.
doctor checks configuration, private state paths, registry endpoints,
service status, caches, classifier key availability, logs and runtime dirs.
It never starts services or reads classifier credential files. --json prints
machine-readable checks; warnings do not affect its exit status.
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
The classifier key comes from the env var named by the classifier's keyEnv,
else from the classifier's envFile (a private dotenv file; units do not see
the shell's env). Without a key the classifier is off: requests keep their
model unless a route is pinned from the panel.
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
$CODEX_THREAD_ID, which Codex sets for its shell commands. If that id is not
listed for the home, ctl fails instead of falling back to --cwd. Otherwise,
outside Codex it uses the session whose cwd is deepest equal to or containing
--cwd (default: the current dir). status prints the session's mode, tier and
route; pause and resume act on that session only; pin --tier T routes its next
turn to tier T.

Exit codes:
  run      codex's exit code; 128+N when signal N ended codex;
           1 when neither routed nor plain codex could start
  tui      0 on quit, 1 on error or when no session matches --session,
           2 when several sessions match --session (they are listed)
  sessions 0, also when no session runs; 1 on error
  doctor   0 when no errors are found; 1 when at least one error is found
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
		case "doctor":
			return c.doctor(ctx, g.config, rest)
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

// launchExec replaces the process with plain codex when the router cannot
// start; nil execs for real, tests replace it.
var launchExec func(path string, argv, env []string) error

func (c cli) launch(configPath string, codexArgs []string) int {
	o := launcher.Options{CodexArgs: codexArgs, Env: c.env, Stderr: c.stderr, Exec: launchExec, Version: version}
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
	noMouse := fs.Bool("no-mouse", false, "leave the mouse to the terminal (text selection works)")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if fs.NArg() > 0 {
		return c.usageError(fmt.Errorf("tui: unexpected argument %q", fs.Arg(0)))
	}
	o := tui.Options{Socket: *socket, NoMouse: *noMouse}
	if *socket == "" {
		root, err := registry.Root(c.getenv)
		if err != nil {
			return c.fail(err)
		}
		o.Registry = root
	}
	if *session != "" {
		all, err := sessions.List(o.Socket, o.Registry)
		if err != nil {
			return c.fail(err)
		}
		switch matches := sessions.Resolve(all, *session); len(matches) {
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
	all, err := sessions.List("", root)
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
		_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", s.ID, s.HomeName, s.Label, s.Mode, sessions.RouteText(s.Route))
	}
	if err := w.Flush(); err != nil {
		return c.fail(err)
	}
	return exitOK
}

// envThread is the thread id Codex sets in the env of its shell commands.
const envThread = "CODEX_THREAD_ID"

// blocked names a router a sandbox hides from ctl; tests replace it.
var blocked = sessions.Blocked

// unreachable is ctl's error for a router a sandbox hides. Inside Codex it
// comes before the CODEX_THREAD_ID check: a hidden router lists no threads.
func unreachable(home string, err error) error {
	return fmt.Errorf("ctl: cannot reach the router of Codex home %s: %w; a sandbox (such as Codex's) blocks local sockets, so run this command outside it", home, err)
}

// ctlOps maps ctl's subcommands to commands; status sends none.
var ctlOps = map[string]route.Op{"status": "", "pause": route.OpPause, "resume": route.OpResume, "pin": route.OpPin, "mode": route.OpMode}

// ctl shows or changes the routing of one session of a Codex home, for the
// plugin's skill, which runs it from a Codex shell command. An explicit
// $CODEX_THREAD_ID must be listed for the home; otherwise ctl refuses to fall
// back to --cwd. Without a thread id, it selects by the deepest matching cwd.
func (c cli) ctl(args []string) int {
	var sub string
	if len(args) > 0 {
		sub = args[0]
	}
	op, known := ctlOps[sub]
	if !known {
		return c.usageError(errors.New("ctl: want status, pause, resume, pin or mode"))
	}
	rest := args[1:]
	var mode config.RoutingMode
	if op == route.OpMode {
		if len(rest) > 0 {
			mode, rest = config.RoutingMode(rest[0]), rest[1:]
		}
		if mode != route.ModeDefault && !slices.Contains(config.RoutingModes, mode) {
			return c.usageError(fmt.Errorf("ctl mode: %q is not cost, balanced, quality or default", mode))
		}
	}
	fs := flag.NewFlagSet("ctl "+sub, flag.ContinueOnError)
	cwd := fs.String("cwd", "", "the session's working `dir` (default: the current dir)")
	tier := new(string)
	if op == route.OpPin {
		tier = fs.String("tier", "", "`tier` of the next turn: micro, low, medium or high")
	}
	c, h, code, ok := c.homeFlags(fs, rest, false)
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
	all, err := sessions.List("", root)
	if err != nil {
		return c.fail(err)
	}
	all = slices.DeleteFunc(all, func(s sessions.Session) bool { return s.Home != h.ID })
	thread := c.getenv(envThread)
	var matches []sessions.Session
	if thread != "" {
		i := slices.IndexFunc(all, func(s sessions.Session) bool { return s.ThreadID == thread })
		if i < 0 {
			if err := blocked(root, h.ID); err != nil {
				return c.fail(unreachable(h.Name, err))
			}
			return c.fail(fmt.Errorf("ctl: CODEX_THREAD_ID %q is not listed for Codex home %s; refusing cwd fallback", thread, h.Name))
		}
		matches = all[i : i+1]
	} else {
		matches = sessions.ForShell(all, "", dir)
	}
	switch len(matches) {
	case 0:
		if err := blocked(root, h.ID); err != nil {
			return c.fail(unreachable(h.Name, err))
		}
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
	cmd := route.Command{Op: op, Session: s.ID, Scope: route.ScopeSession, Tier: config.Tier(*tier), Mode: mode}
	if err := sessions.Send(s, cmd); err != nil {
		return c.fail(fmt.Errorf("ctl %s: %w", sub, err))
	}
	switch op {
	case route.OpPause:
		_, _ = fmt.Fprintf(c.stdout, "paused routing of %s: requests keep the model in Codex's footer\n", s.Label)
	case route.OpResume:
		_, _ = fmt.Fprintf(c.stdout, "resumed routing of %s\n", s.Label)
	case route.OpMode:
		if mode == route.ModeDefault {
			_, _ = fmt.Fprintf(c.stdout, "cleared the mode of %s: the project file, the config or balanced decides\n", s.Label)
			break
		}
		_, _ = fmt.Fprintf(c.stdout, "set %s to %s mode: the route follows once the switch gates pass; a pin forces one now\n", s.Label, mode)
	default:
		_, _ = fmt.Fprintf(c.stdout, "pinned the next turn of %s to %s\n", s.Label, *tier)
	}
	return exitOK
}

// printSession prints s for ctl status, one field per line.
func (c cli) printSession(s sessions.Session) int {
	w := tabwriter.NewWriter(c.stdout, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintf(w, "session\t%s\nlabel\t%s\nmode\t%s\ntier\t%s\nroute\t%s\nreplies\t%d\n",
		s.ID, s.Label, s.Mode, cmp.Or(string(s.Tier), "-"), sessions.RouteText(s.Route), s.Replies)
	if s.RoutingMode != "" {
		_, _ = fmt.Fprintf(w, "routing\t%s (%s)\n", s.RoutingMode, s.RoutingModeSource)
	}
	if s.Attention != "" {
		_, _ = fmt.Fprintf(w, "attention\t%s\n", s.Attention)
	}
	if err := w.Flush(); err != nil {
		return c.fail(err)
	}
	return exitOK
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
		port, perr := statedir.ReadPort(dir)
		if perr != nil {
			return c.fail(fmt.Errorf("edge: %w", perr))
		}
		addr = net.JoinHostPort(proxy.LoopbackHost, strconv.Itoa(port))
		log.Warn("edge-self-bind", "port", port, "why", err.Error(),
			"note", "the port is free while the edge is down; run codex-router setup to install the service units")
	case err != nil:
		return c.fail(err)
	}
	lc := link.NewClient(link.ClientOptions{
		Path: func() string { return link.Path(dir) }, HomeID: h.ID, Timeout: cfg.DecideTimeout(), Logger: log,
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
	store := &catalog.Store{}
	if cache, err := catalog.LoadCache(filepath.Join(h.Path, catalog.CacheFile)); err != nil {
		log.Warn("catalog-cache-unreadable", "err", err)
	} else {
		store.Set(cache)
		log.Info("catalog-seeded", "models", len(cache.Models))
	}
	asker, why := classifier.Configured(cfg.Classifier, cfg.Classifiers, c.getenv)
	if asker == nil {
		log.Warn("classifier-disabled", "reason", why)
	}
	deps := route.Deps{
		Config: cfg, Catalog: store, Classifier: asker, ClassifierOffReason: why, Logger: log, Home: h,
	}
	models := catalog.NewCacheWatch(filepath.Join(h.Path, catalog.CacheFile), store, nil)
	ctx, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := brain.Run(ctx, brain.Options{Home: h, Version: version, Deps: deps, Getenv: c.getenv, Models: models}); err != nil {
		return c.fail(err)
	}
	return exitOK
}

// edgeState returns h's stage 2 state dir and the token in it. The dir and
// its parents up to codex-router/ must be private to this user: whoever can
// write there can swap the token.
func edgeState(getenv func(string) string, h home.Home) (dir, token string, err error) {
	app, err := statedir.Dir(getenv)
	if err != nil {
		return "", "", err
	}
	dir = statedir.HomeDir(app, h.ID)
	if err := statedir.CheckHome(app, dir); err != nil {
		return "", "", fmt.Errorf("edge: home %s is not set up (run codex-router setup --home %s): %w", h.Name, h.Path, err)
	}
	b, err := os.ReadFile(filepath.Join(dir, statedir.TokenFile))
	if err != nil {
		return "", "", fmt.Errorf("edge: token: %w", err)
	}
	if token = strings.TrimSpace(string(b)); token == "" {
		return "", "", fmt.Errorf("edge: token file %s is empty", filepath.Join(dir, statedir.TokenFile))
	}
	return dir, token, nil
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
	given, err := home.Given(c.getenv)
	if err != nil {
		return c.fail(err)
	}
	app, err := statedir.Dir(c.getenv)
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
		Home: h, HomePath: given, StateDir: filepath.Dir(app), Config: cfgPath, Bin: bin,
		DryRun: *dryRun, Undo: *undo, Purge: *purge, FollowSymlink: *follow,
		Service: mgr, Out: c.stdout,
	})
	if err != nil {
		return c.fail(err)
	}
	return exitOK
}

type doctorCheck struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Detail string `json:"detail"`
}

type doctorReport struct {
	Version  string        `json:"version"`
	Errors   int           `json:"errors"`
	Warnings int           `json:"warnings"`
	Checks   []doctorCheck `json:"checks"`
}

func (d *doctorReport) add(status, name, detail string) {
	d.Checks = append(d.Checks, doctorCheck{Name: name, Status: status, Detail: detail})
	switch status {
	case "error":
		d.Errors++
	case "warning":
		d.Warnings++
	}
}

// doctor runs read-only diagnostics. It does not invoke setup, service ensure,
// the classifier, or any network health endpoint.
func (c cli) doctor(ctx context.Context, configFlag string, args []string) int {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "print machine-readable checks as JSON")
	c, h, code, ok := c.homeFlags(fs, args, true)
	if !ok {
		return code
	}
	report := doctorReport{Version: safeDoctorVersion(version)}

	configPath, priority, err := c.doctorConfigPath(configFlag)
	var cfg config.Config
	configOK := false
	if err != nil {
		report.add("error", "config", "config path could not be resolved")
	} else if cfg, err = config.Load(configPath); err != nil {
		report.add("error", "config", fmt.Sprintf("invalid config at %s (priority: %s); check JSON syntax, known fields and values", configPath, priority))
	} else {
		configOK = true
		if _, err := os.Stat(configPath); errors.Is(err, os.ErrNotExist) {
			report.add("ok", "config", fmt.Sprintf("using defaults; no file at %s (priority: %s)", configPath, priority))
		} else {
			report.add("ok", "config", fmt.Sprintf("loaded %s (priority: %s)", configPath, priority))
		}
	}

	app, err := statedir.Dir(c.getenv)
	if err != nil {
		report.add("error", "state", "state directory path could not be resolved")
		if emitErr := emitDoctor(c.stdout, report, *asJSON); emitErr != nil {
			return c.fail(emitErr)
		}
		return exitError
	}
	statePrivate := checkPrivatePath(&report, "state", app)
	appInfo, appStatErr := os.Lstat(app)
	stateTreeSafe := errors.Is(appStatErr, os.ErrNotExist) ||
		(appStatErr == nil && appInfo.IsDir() && appInfo.Mode()&os.ModeSymlink == 0 && statePrivate)

	// This bind-only probe cannot activate launchd or systemd socket units.
	// Run it before service status checks, and never connect to the port.
	portProbe := probeDoctorPort(app, h.ID)
	root := filepath.Join(app, "registry")
	var eps []registry.Endpoint
	registryOK := false
	if stateTreeSafe {
		eps, registryOK = doctorRegistry(&report, root)
	} else {
		report.add("warning", "registry", "not inspected because the state directory is not private")
	}
	liveEndpoints := make(map[string]struct{}, len(eps))
	homeIDs := map[string]struct{}{h.ID: {}}
	for _, ep := range eps {
		if doctorHomeID(ep.Home.ID) {
			homeIDs[ep.Home.ID] = struct{}{}
		} else {
			report.add("error", "registry", fmt.Sprintf("endpoint %q has an invalid home id", ep.ID))
		}
		client, err := control.Dial(ep.Socket)
		if err != nil {
			report.add("error", "endpoint "+strconv.Quote(ep.ID), "control socket did not answer the hello")
			doctorVersionCheck(&report, ep, nil)
			continue
		}
		hello := client.Hello()
		_ = client.Close()
		if hello.EndpointID != ep.ID || hello.Kind != ep.Kind || hello.Home != ep.Home {
			report.add("error", "endpoint "+strconv.Quote(ep.ID), "control hello identity mismatch")
		} else {
			liveEndpoints[ep.ID] = struct{}{}
			report.add("ok", "endpoint "+strconv.Quote(ep.ID), "control hello answered and identity matches")
		}
		doctorVersionCheck(&report, ep, &hello)
	}

	ids := make([]string, 0, len(homeIDs))
	for id := range homeIDs {
		if doctorHomeID(id) {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	serviceStates := c.doctorServices(ctx, ids, h.ID, &report)
	checkDoctorHomeDirs(app, ids, serviceStates, h.ID, &report)
	reportDoctorPort(portProbe, serviceStates[h.ID], h.ID, &report)
	if configOK {
		doctorClassifier(&report, cfg, c.getenv)
	} else {
		report.add("warning", "classifier", "not checked because config is invalid or unavailable")
	}
	doctorCatalog(&report, h)
	if stateTreeSafe {
		doctorLauncherLogs(&report, filepath.Join(app, "logs"))
	} else {
		report.add("warning", "launcher-logs", "not inspected because the state directory is not private")
	}
	if registryOK {
		doctorRuntimeDirs(&report, launcher.RuntimeBase(c.getenv), liveEndpoints)
	} else {
		report.add("warning", "runtime-dirs", "not checked because the registry could not be read")
	}

	if err := emitDoctor(c.stdout, report, *asJSON); err != nil {
		return c.fail(err)
	}
	if report.Errors > 0 {
		return exitError
	}
	return exitOK
}

func (c cli) doctorConfigPath(flagPath string) (string, string, error) {
	if flagPath != "" {
		return flagPath, "--config", nil
	}
	if path := c.getenv(envConfig); path != "" {
		return path, "$CODEX_ROUTER_CONFIG", nil
	}
	if dir := c.getenv("XDG_CONFIG_HOME"); filepath.IsAbs(dir) {
		return filepath.Join(dir, config.DirName, config.FileName), "$XDG_CONFIG_HOME", nil
	}
	h := c.getenv("HOME")
	if !filepath.IsAbs(h) {
		return "", "", errors.New("HOME is not an absolute path")
	}
	return filepath.Join(h, ".config", config.DirName, config.FileName), "$HOME/.config", nil
}

func safeDoctorVersion(v string) string {
	if v == "" || len(v) > 64 {
		return "unknown"
	}
	for _, r := range v {
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') && !strings.ContainsRune("._+-", r) {
			return "unrecognized"
		}
	}
	return v
}

func doctorVersionCheck(report *doctorReport, ep registry.Endpoint, hello *control.Hello) {
	var findings []string
	if ep.Version == "" {
		findings = append(findings, "registry software version missing")
	} else if ep.Version != version {
		findings = append(findings, fmt.Sprintf("registry version differs from doctor version (%s vs %s)", safeDoctorVersion(ep.Version), safeDoctorVersion(version)))
	}
	if hello != nil {
		if hello.Version == "" {
			findings = append(findings, "hello software version missing")
		} else {
			if ep.Version != "" && ep.Version != hello.Version {
				findings = append(findings, fmt.Sprintf("registry and hello versions differ (%s vs %s)", safeDoctorVersion(ep.Version), safeDoctorVersion(hello.Version)))
			}
			if hello.Version != version {
				findings = append(findings, fmt.Sprintf("hello version differs from doctor version (%s vs %s)", safeDoctorVersion(hello.Version), safeDoctorVersion(version)))
			}
		}
	}
	name := "version " + strconv.Quote(ep.ID)
	if len(findings) == 0 {
		report.add("ok", name, "registry and hello software versions match the doctor version")
		return
	}
	findings = append(findings, "release mismatch does not imply protocol incompatibility")
	report.add("warning", name, strings.Join(findings, "; "))
}

func doctorHomeID(id string) bool {
	if len(id) != 12 {
		return false
	}
	for _, r := range id {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}

func doctorRegistry(report *doctorReport, root string) ([]registry.Endpoint, bool) {
	fi, err := os.Lstat(root)
	if errors.Is(err, os.ErrNotExist) {
		report.add("warning", "registry", "directory is not present; no endpoints are registered yet")
		return nil, true
	}
	if err != nil {
		report.add("error", "registry", "directory could not be inspected")
		return nil, false
	}
	if !fi.IsDir() || fi.Mode()&os.ModeSymlink != 0 {
		report.add("error", "registry", "path is not a real directory")
		return nil, false
	}
	if err := statedir.CheckPrivate(root); err != nil {
		report.add("error", "registry", "directory is not private to this user")
		return nil, false
	}
	eps, skipped, err := registry.List(root)
	if err != nil {
		report.add("error", "registry", "entries could not be listed")
		return nil, false
	}
	report.add("ok", "registry", fmt.Sprintf("listed %d endpoint entries", len(eps)))
	if len(skipped) > 0 {
		report.add("error", "registry", fmt.Sprintf("%d invalid entries were skipped", len(skipped)))
	}
	return eps, true
}

type doctorServiceState struct {
	setup, readable, edgeUp bool
}

func (c cli) doctorServices(ctx context.Context, ids []string, selectedID string, report *doctorReport) map[string]doctorServiceState {
	states := make(map[string]doctorServiceState, len(ids))
	mgr, err := c.services()
	if err != nil {
		report.add("error", "services", "service status could not be initialized on this platform")
		return states
	}
	for _, id := range ids {
		allUnits := mgr.Units(id)
		installed := mgr.Installed(id)
		status := doctorServiceState{setup: len(installed) > 0, readable: true}
		statusCtx, cancel := context.WithTimeout(ctx, ensureTimeout)
		units, err := mgr.StatusReadOnly(statusCtx, id)
		cancel()
		if err != nil {
			status.readable = false
			if id == selectedID || len(installed) > 0 {
				report.add("error", "services "+id, "read-only service status could not be read")
			}
			states[id] = status
			continue
		}
		if id == selectedID || len(installed) > 0 {
			report.add("ok", "services "+id, "read-only service status is available")
		}
		if len(installed) == 0 {
			if id == selectedID {
				report.add("warning", "services "+id, "no service units are installed for this home")
			}
			states[id] = status
			continue
		}
		if len(installed) != len(allUnits) {
			report.add("error", "services "+id, fmt.Sprintf("only %d of %d service unit files are installed", len(installed), len(allUnits)))
		}
		for _, unit := range units {
			if unit.Role == service.RoleEdge {
				status.edgeUp = unit.State == service.Up
			}
			if unit.State == service.Up {
				report.add("ok", "service "+id+" "+unit.Role, "up")
			} else {
				report.add("error", "service "+id+" "+unit.Role, unit.State.String())
			}
		}
		states[id] = status
	}
	return states
}

func checkPrivatePath(report *doctorReport, name, path string) bool {
	_, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		report.add("warning", name, path+" is not present yet")
		return false
	}
	if err != nil {
		report.add("error", name, path+" could not be inspected")
		return false
	}
	if err := statedir.CheckPrivate(path); err != nil {
		report.add("error", name, path+" is not private to this user")
		return false
	}
	report.add("ok", name, path+" is private to this user")
	return true
}

func checkDoctorHomeDirs(app string, ids []string, services map[string]doctorServiceState, selectedID string, report *doctorReport) {
	for _, id := range ids {
		path := statedir.HomeDir(app, id)
		_, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			parent := filepath.Dir(path)
			if _, perr := os.Lstat(parent); perr == nil {
				if err := statedir.CheckPrivate(parent); err != nil {
					report.add("error", "home-state "+id, "homes directory is not private to this user")
					continue
				}
			}
			if services[id].setup {
				report.add("error", "home-state "+id, "set-up home's state directory is missing")
			} else {
				report.add("warning", "home-state "+id, "state directory is not present; privacy could not be checked")
			}
			continue
		}
		if err != nil {
			report.add("error", "home-state "+id, "state directory could not be inspected")
			continue
		}
		if err := statedir.CheckHome(app, path); err != nil {
			report.add("error", "home-state "+id, "state directory or a parent is not private to this user")
			continue
		}
		report.add("ok", "home-state "+id, "state directory and parents are private to this user")
	}
}

type doctorPortProbe struct {
	port     int
	present  bool
	invalid  bool
	free     bool
	occupied bool
	unknown  bool
}

func probeDoctorPort(app, homeID string) doctorPortProbe {
	dir := statedir.HomeDir(app, homeID)
	if _, err := os.Lstat(dir); errors.Is(err, os.ErrNotExist) {
		return doctorPortProbe{}
	} else if err != nil || statedir.CheckHome(app, dir) != nil {
		return doctorPortProbe{unknown: true}
	}
	path := filepath.Join(dir, statedir.PortFile)
	fi, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return doctorPortProbe{}
	}
	if err != nil || !fi.Mode().IsRegular() {
		return doctorPortProbe{present: true, invalid: true}
	}
	port, err := statedir.ReadPort(dir)
	if err != nil {
		return doctorPortProbe{present: true, invalid: true}
	}
	probe := doctorPortProbe{port: port, present: true}
	ln, err := net.Listen("tcp4", net.JoinHostPort(proxy.LoopbackHost, strconv.Itoa(port)))
	if err != nil {
		if errors.Is(err, syscall.EADDRINUSE) {
			probe.occupied = true
		} else {
			probe.unknown = true
		}
		return probe
	}
	if err := ln.Close(); err != nil {
		probe.unknown = true
		return probe
	}
	probe.free = true
	return probe
}

func reportDoctorPort(probe doctorPortProbe, serviceStatus doctorServiceState, homeID string, report *doctorReport) {
	name := "proxy-port " + homeID
	if !probe.present {
		report.add("warning", name, "unverified; port file is absent")
		return
	}
	if probe.invalid {
		report.add("error", name, "port file is unreadable or does not contain a valid port")
		return
	}
	if probe.unknown {
		report.add("warning", name, fmt.Sprintf("port %d bind probe was inconclusive; no connection was attempted", probe.port))
		return
	}
	if probe.free {
		if serviceStatus.readable && serviceStatus.edgeUp {
			report.add("warning", name, fmt.Sprintf("port %d is bindable although the edge reports up; status and probe may have raced", probe.port))
		} else {
			report.add("ok", name, fmt.Sprintf("port %d is free; no connection was attempted", probe.port))
		}
		return
	}
	if probe.occupied {
		switch {
		case !serviceStatus.readable:
			report.add("warning", name, fmt.Sprintf("port %d is held but service status is unavailable; listener is unverified", probe.port))
		case serviceStatus.setup && serviceStatus.edgeUp:
			report.add("ok", name, fmt.Sprintf("port %d is held by the set-up edge; no connection was attempted", probe.port))
		default:
			report.add("error", name, fmt.Sprintf("port %d is held without an up edge service; unexpected listener is unverified", probe.port))
		}
	}
}

func doctorClassifier(report *doctorReport, cfg config.Config, getenv func(string) string) {
	if cfg.Classifier == config.ClassifierOff {
		report.add("ok", "classifier", "off in config; key not required")
		return
	}
	cc, ok := cfg.Classifiers[cfg.Classifier]
	if !ok {
		report.add("error", "classifier", "selected classifier is not configured")
		return
	}
	missingPlaceholder := false
	if names, err := config.EndpointPlaceholders(cc.Endpoint); err == nil {
		for _, name := range names {
			if getenv(name) == "" {
				missingPlaceholder = true
			}
		}
	}
	if missingPlaceholder && cc.EnvFile != "" {
		keyState := "key not required"
		if cc.KeyEnv != "" {
			if getenv(cc.KeyEnv) != "" {
				keyState = "key present in process environment"
			} else {
				keyState = "key presence in credential file not inspected"
			}
		}
		report.add("warning", "classifier", "configured; "+keyState+"; endpoint values in credential file were not inspected")
		return
	}
	if cc.KeyEnv == "" {
		if missingPlaceholder {
			report.add("warning", "classifier", "off; a required endpoint value is absent from the process environment")
		} else {
			report.add("ok", "classifier", "on; no key is required")
		}
		return
	}
	if getenv(cc.KeyEnv) != "" {
		report.add("ok", "classifier", "on; key present in process environment (value hidden)")
		return
	}
	if cc.EnvFile != "" {
		report.add("warning", "classifier", "configured; key presence in credential file was not inspected")
		return
	}
	report.add("warning", "classifier", "off; key absent from process environment")
}

func doctorCatalog(report *doctorReport, h home.Home) {
	path := filepath.Join(h.Path, catalog.CacheFile)
	fi, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		report.add("info", "catalog", "cache absent")
		return
	}
	if err != nil {
		report.add("warning", "catalog", "cache could not be inspected")
		return
	}
	if fi.Mode().IsRegular() {
		report.add("ok", "catalog", "cache present")
	} else {
		report.add("warning", "catalog", "cache path is present but is not a regular file")
	}
}

func doctorLauncherLogs(report *doctorReport, dir string) {
	fi, err := os.Lstat(dir)
	if errors.Is(err, os.ErrNotExist) {
		report.add("info", "launcher-logs", "directory absent; 0 bytes")
		return
	}
	if err != nil || !fi.IsDir() || fi.Mode()&os.ModeSymlink != 0 {
		report.add("warning", "launcher-logs", "directory could not be inspected safely")
		return
	}
	if err := statedir.CheckPrivate(dir); err != nil {
		report.add("error", "launcher-logs", "directory is not private to this user")
		return
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		report.add("warning", "launcher-logs", "directory could not be listed")
		return
	}
	var files int
	var size int64
	for _, entry := range entries {
		if fi, err := os.Lstat(filepath.Join(dir, entry.Name())); err == nil && fi.Mode().IsRegular() {
			files++
			size += fi.Size()
		}
	}
	report.add("ok", "launcher-logs", fmt.Sprintf("%d regular files, %d bytes; per-run retention cap 40", files, size))
}

func doctorRuntimeDirs(report *doctorReport, base string, live map[string]struct{}) {
	fi, err := os.Lstat(base)
	if errors.Is(err, os.ErrNotExist) {
		report.add("info", "runtime-dirs", "runtime base absent; no stale directories")
		return
	}
	if err != nil || !fi.IsDir() || fi.Mode()&os.ModeSymlink != 0 {
		report.add("warning", "runtime-dirs", "runtime base could not be inspected safely")
		return
	}
	entries, err := os.ReadDir(base)
	if err != nil {
		report.add("warning", "runtime-dirs", "runtime base could not be listed")
		return
	}
	var stale []string
	for _, entry := range entries {
		if !runtimeID(entry.Name()) {
			continue
		}
		path := filepath.Join(base, entry.Name())
		fi, err := os.Lstat(path)
		if err != nil || !fi.IsDir() || fi.Mode()&os.ModeSymlink != 0 {
			continue
		}
		if _, ok := live[entry.Name()]; !ok {
			stale = append(stale, entry.Name())
		}
	}
	slices.Sort(stale)
	if len(stale) == 0 {
		report.add("ok", "runtime-dirs", "no stale runtime directories found")
	} else {
		report.add("warning", "runtime-dirs", fmt.Sprintf("%d directories have no matching live registry endpoint: %s", len(stale), strings.Join(stale, ", ")))
	}
}

func runtimeID(id string) bool {
	if len(id) != 8 {
		return false
	}
	for _, r := range id {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}

func emitDoctor(w io.Writer, report doctorReport, asJSON bool) error {
	if asJSON {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(report)
	}
	if _, err := fmt.Fprintf(w, "codex-router doctor (%s)\n", report.Version); err != nil {
		return err
	}
	for _, check := range report.Checks {
		if _, err := fmt.Fprintf(w, "%s %s: %s\n", strings.ToUpper(check.Status), check.Name, check.Detail); err != nil {
			return err
		}
	}
	_, err := fmt.Fprintf(w, "Summary: %d error(s), %d warning(s)\n", report.Errors, report.Warnings)
	return err
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
