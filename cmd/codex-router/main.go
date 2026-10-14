// Command codex-router routes Codex CLI requests to a model and reasoning
// effort per step. See docs/architecture.md and docs/user-guide.md.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"slices"
	"strings"
	"syscall"

	"github.com/alexei-led/codex-router/internal/config"
	"github.com/alexei-led/codex-router/internal/hook"
	"github.com/alexei-led/codex-router/internal/launcher"
	"github.com/alexei-led/codex-router/internal/registry"
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
  codex-router tui [--socket PATH]                   show the router panel
  codex-router hook                                  Codex hook: print a line when the route switched
  codex-router [--config PATH] proxy [--port N]      run the proxy alone (debugging, codex exec)
  codex-router version                               print the version

Global flags:
  --config PATH  config file; else $CODEX_ROUTER_CONFIG; else
                 $XDG_CONFIG_HOME/codex-router/config.json (~/.config/...)
  --version      print the version
  -h, --help     show this help

Arguments after the router flags (or after --) go to codex unchanged. If the
router cannot start, it prints one line why and runs plain codex.
tui --socket defaults to the newest running session's control socket, looked up
again on every reconnect so the panel follows a restarted session.
proxy --port defaults to the config's proxy.port (0 = any free port) and
prints the codex -c flags that point at it.
The classifier key comes from the env var named by the classifier's keyEnv.
When it is unset the classifier is off: requests keep their model unless a
route is pinned from the panel.

Exit codes:
  run      codex's exit code; 128+N when signal N ended codex;
           1 when neither routed nor plain codex could start
  tui      0 on quit, 1 on error
  hook     0, also when the router is not running; 1 only when stdout fails
  proxy    0 after SIGINT or SIGTERM, 1 on error
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
		case "proxy":
			return c.proxy(ctx, g.config, rest)
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

func (c cli) loadConfig(flagPath string) (config.Config, error) {
	path := flagPath
	if path == "" {
		path = c.getenv(envConfig)
	}
	if path == "" {
		p, err := config.Path()
		if err != nil {
			return config.Config{}, err
		}
		path = p
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

func (c cli) tui(ctx context.Context, args []string) int {
	fs := flag.NewFlagSet("tui", flag.ContinueOnError)
	fs.SetOutput(c.stderr)
	socket := fs.String("socket", "", "control socket `path` (default: newest running session)")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if fs.NArg() > 0 {
		return c.usageError(fmt.Errorf("tui: unexpected argument %q", fs.Arg(0)))
	}
	o := tui.Options{Socket: *socket}
	if *socket == "" {
		o.Locate = func() (string, error) { return newestSession(c.getenv) }
		s, err := o.Locate()
		if err != nil {
			return c.fail(err)
		}
		o.Socket = s
	}
	ctx, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := tui.Run(ctx, o); err != nil {
		return c.fail(err)
	}
	return exitOK
}

// newestSession returns the control socket of the most recently started
// live endpoint in the registry.
func newestSession(getenv func(string) string) (string, error) {
	root, err := registry.Root(getenv)
	if err != nil {
		return "", err
	}
	eps, _, err := registry.List(root)
	if err != nil {
		return "", err
	}
	for _, ep := range slices.Backward(eps) {
		if registry.Alive(ep) {
			return ep.Socket, nil
		}
	}
	return "", fmt.Errorf("no running codex-router session in %s", root)
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
