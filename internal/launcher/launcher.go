package launcher

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/alexei-led/codex-router/internal/appserver"
	"github.com/alexei-led/codex-router/internal/config"
	"github.com/alexei-led/codex-router/internal/control"
	"github.com/alexei-led/codex-router/internal/proxy"
	"github.com/alexei-led/codex-router/internal/registry"
	"github.com/alexei-led/codex-router/internal/route"
)

// Names inside the runtime dir and the app-server's env.
const (
	RuntimeDirName = "codex-router"
	AppSocketName  = "app.sock"
	PortFileName   = "proxy.port"
	// EnvToken holds the proxy's per-session token in the app-server's env.
	// Codex sends it as proxy.HeaderToken through env_http_headers, so the
	// value never appears in argv, which other uids can read.
	EnvToken = "CODEX_ROUTER_TOKEN"
)

const defaultCodexBin = "codex"

// Options configure Run and Serve.
type Options struct {
	Config config.Config
	// RuntimeDir is the parent of the per-session dirs; empty means
	// RuntimeBase of Env.
	RuntimeDir string
	// CodexBin is the codex executable; empty means Config.CodexBin, else
	// "codex" on PATH.
	CodexBin  string
	CodexArgs []string
	Version   string
	// Env is the environment for codex and for the router's own lookups
	// (classifier key, CODEX_HOME, XDG dirs); nil means os.Environ().
	Env    []string
	Stderr io.Writer // nil means os.Stderr
	// Logger replaces the default log: a JSON file under the XDG state dir
	// for Run, JSON on Stderr for Serve.
	Logger *slog.Logger
	// Probe checks the proxy on port; nil means GET /healthz within 2 s.
	Probe func(ctx context.Context, port int) error
	// Exec replaces the process with plain codex on fallback; nil means
	// syscall.Exec.
	Exec func(path string, argv, env []string) error
}

func (o Options) withDefaults() Options {
	if o.Env == nil {
		o.Env = os.Environ()
	}
	if o.Stderr == nil {
		o.Stderr = os.Stderr
	}
	if o.CodexBin == "" {
		o.CodexBin = o.Config.CodexBin
	}
	if o.CodexBin == "" {
		o.CodexBin = defaultCodexBin
	}
	if o.Probe == nil {
		o.Probe = probeHealth
	}
	if o.Exec == nil {
		o.Exec = syscall.Exec
	}
	return o
}

func (o Options) getenv(key string) string {
	for _, kv := range slices.Backward(o.Env) {
		if k, v, ok := strings.Cut(kv, "="); ok && k == key {
			return v
		}
	}
	return ""
}

// ProviderOverrides returns the -c values that point Codex at the proxy on
// port. requires_openai_auth makes Codex send its own ChatGPT auth, which the
// proxy forwards upstream only. env_http_headers sends $CODEX_ROUTER_TOKEN as
// the proxy token; Codex leaves the header out when the variable is unset.
func ProviderOverrides(port int) []string {
	return []string{
		"model_provider=" + proxy.ProviderID,
		fmt.Sprintf(`model_providers.%s={name="%s", base_url="http://%s:%d/v1", wire_api="responses", requires_openai_auth=true, env_http_headers={"%s"="%s"}}`,
			proxy.ProviderID, proxy.ProviderName, proxy.LoopbackHost, port, proxy.HeaderToken, EnvToken),
	}
}

// newToken returns 128 random bits in hex. crypto/rand.Read does not fail.
func newToken() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// Run runs one routed Codex session and returns codex's exit code (128+N
// when a signal N ended it). If a startup step fails it prints one line why
// and replaces the process with plain `codex <args>` through Options.Exec;
// the returned error is then Exec's. A signal during startup aborts it
// without the fallback.
func Run(ctx context.Context, o Options) (int, error) {
	o = o.withDefaults()
	sigs := make(chan os.Signal, 4)
	// Caught, not ignored: a caught signal resets to its default in codex,
	// an ignored one would stay ignored there.
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGQUIT, syscall.SIGTERM, syscall.SIGHUP)
	defer signal.Stop(sigs)

	s, sig, err := startGuarded(ctx, o, sigs)
	switch {
	case sig != nil:
		n, _ := sig.(syscall.Signal)
		return 128 + int(n), nil
	case err != nil && ctx.Err() != nil:
		return 0, ctx.Err()
	case err != nil:
		return 0, Fallback(o, err)
	}
	if err := s.startCodex(); err != nil {
		s.close()
		return 0, Fallback(o, err)
	}
	code := s.waitCodex(ctx, sigs)
	s.close()
	return code, nil
}

// startGuarded runs start and aborts it on the first signal: the app-server
// runs in its own process group, so a launcher killed by Ctrl-C here would
// leave it behind.
func startGuarded(ctx context.Context, o Options, sigs <-chan os.Signal) (*session, os.Signal, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var got os.Signal
	stop, watched := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(watched)
		select {
		case got = <-sigs:
			cancel()
		case <-stop:
		}
	}()
	s, err := start(ctx, o)
	close(stop)
	<-watched
	if got != nil {
		if s != nil {
			s.close()
		}
		return nil, got, nil
	}
	return s, nil, err
}

// Fallback prints one line with the reason and replaces the process with
// plain `codex <args>`. It returns only when that fails, or when a test's
// Options.Exec returns.
func Fallback(o Options, reason error) error {
	o = o.withDefaults()
	_, _ = fmt.Fprintf(o.Stderr, "codex-router: %v; running plain codex\n", reason)
	path, err := exec.LookPath(o.CodexBin)
	if err != nil {
		return fmt.Errorf("find codex: %w", err)
	}
	if err := o.Exec(path, append([]string{o.CodexBin}, o.CodexArgs...), o.Env); err != nil {
		return fmt.Errorf("exec codex: %w", err)
	}
	return nil
}

// sink fans engine output out: decisions to the settings writer's queue,
// snapshots to the control socket. Both calls return at once; the engine
// calls OnDecision while it holds the thread's step lock.
type sink struct {
	writer *appserver.SettingsWriter
	ctl    atomic.Pointer[control.Server]
}

func (s *sink) OnDecision(d route.Decision) {
	if d.ThreadID != "" && d.Route.Model != "" {
		s.writer.Enqueue(d.ThreadID, d.Route)
	}
}

func (s *sink) OnSnapshot(snap route.Snapshot) {
	if c := s.ctl.Load(); c != nil {
		c.Publish(snap)
	}
}

// endpoint is the engine as the control socket serves it. The runtime id
// names the endpoint.
type endpoint struct {
	*route.Engine
	hello control.Hello
}

func (e endpoint) Hello() control.Hello { return e.hello }

// session is one started router: everything but the codex TUI child.
type session struct {
	o       Options
	log     *slog.Logger
	restore func()
	dir     string
	env     []string // children's env: Options.Env plus the control socket
	cancel  context.CancelFunc
	wg      sync.WaitGroup
	ctl     *control.Server
	reg     string // registry root once the entry is written
	id      string // runtime id: the endpoint id
	app     *appServer
	api     appserver.API
	writer  *appserver.SettingsWriter
	codex   *exec.Cmd
}

func start(ctx context.Context, o Options) (s *session, err error) {
	dir, id, err := makeRuntimeDir(o.RuntimeDir, o.getenv)
	if err != nil {
		return nil, err
	}
	s = &session{o: o, dir: dir, id: id}
	defer func() {
		if err != nil {
			s.close()
			s = nil
		}
	}()
	s.log, s.restore = openLog(o, id)
	s.log.Info("session-start", "runtime", id, "codex_args", len(o.CodexArgs))

	runCtx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	w := appserver.NewSettingsWriter(s.log, true)
	s.writer = w
	snk := &sink{writer: w}
	eng, store, h := newRouter(o, s.log, snk)

	ctlPath := filepath.Join(dir, control.SocketName)
	hello := control.Hello{EndpointID: id, Kind: registry.KindSession, Home: h, Label: launcherLabel(h), Version: o.Version}
	if s.ctl, err = control.Listen(ctlPath, endpoint{eng, hello}); err != nil {
		return s, err
	}
	snk.ctl.Store(s.ctl)
	s.register(registry.Endpoint{
		ID: id, Kind: registry.KindSession, Protocol: control.Protocol,
		PID: os.Getpid(), Started: time.Now(), Home: h, Socket: ctlPath, Version: o.Version,
	})
	s.env = append(slices.Clone(o.Env), control.EnvSocket+"="+ctlPath)

	token := newToken()
	port, err := startProxy(ctx, runCtx, &s.wg, o, token, eng, store, s.log)
	if err != nil {
		return s, err
	}
	if err := writePort(dir, port); err != nil {
		return s, err
	}
	sock := filepath.Join(dir, AppSocketName)
	// Only the app-server calls the proxy; the TUI child talks to it over
	// the socket and never gets the token.
	appEnv := append(slices.Clone(s.env), EnvToken+"="+token)
	if s.app, err = startAppServer(o.CodexBin, sock, port, appEnv); err != nil {
		return s, err
	}
	if s.api, err = dialAppServer(ctx, sock, s.app.done, s.log); err != nil {
		return s, err
	}
	s.log.Info("appserver-ready", "port", port)

	api := s.api
	s.wg.Go(func() { w.Run(runCtx, api) })
	s.wg.Go(func() {
		for u := range api.Updates() {
			r := config.Route{Model: u.Settings.Model, Effort: u.Settings.Effort}
			w.Reported(u.ThreadID, r)
			eng.SettingsUpdated(u.ThreadID, r)
		}
		s.log.Info("appserver-updates-closed")
	})
	return s, nil
}

// register lists the session in the shared registry, where panels in any
// shell find it. A session that cannot register still routes, so a failure
// only warns: the panel will not list it.
func (s *session) register(ep registry.Endpoint) {
	root, err := registry.Root(s.o.getenv)
	if err == nil {
		err = registry.Write(root, ep)
	}
	if err != nil {
		s.log.Warn("registry-write-failed", "err", err)
		_, _ = fmt.Fprintf(s.o.Stderr, "codex-router: the panel will not list this session: %v\n", err)
		return
	}
	s.reg = root
}

// startCodex runs `codex --remote` as a child on the terminal: the proxy
// lives in this process, so the launcher must stay.
func (s *session) startCodex() error {
	args := append([]string{"--remote", "unix://" + filepath.Join(s.dir, AppSocketName)}, s.o.CodexArgs...)
	cmd := exec.Command(s.o.CodexBin, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	cmd.Env = s.env
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start codex --remote: %w", err)
	}
	s.codex = cmd
	s.log.Info("codex-started", "pid", cmd.Process.Pid)
	return nil
}

// waitCodex waits for the codex child. SIGINT and SIGQUIT are dropped: the
// terminal sends them to codex as well. SIGTERM and SIGHUP are passed on, and
// so is ctx's end, as SIGTERM.
func (s *session) waitCodex(ctx context.Context, sigs <-chan os.Signal) int {
	done := make(chan struct{})
	go func() {
		_ = s.codex.Wait()
		close(done)
	}()
	ctxDone := ctx.Done()
	for {
		select {
		case sig := <-sigs:
			if sig == syscall.SIGTERM || sig == syscall.SIGHUP {
				_ = s.codex.Process.Signal(sig)
			}
		case <-ctxDone:
			ctxDone = nil
			_ = s.codex.Process.Signal(syscall.SIGTERM)
		case <-done:
			code := exitCode(s.codex.ProcessState)
			s.log.Info("codex-exited", "code", code)
			return code
		}
	}
}

func exitCode(ps *os.ProcessState) int {
	if ps == nil {
		return 1
	}
	if ws, ok := ps.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		return 128 + int(ws.Signal())
	}
	return ps.ExitCode()
}

// close stops everything start began, in reverse, and removes the runtime
// dir. It is safe on a partly started session. The registry entry goes
// first, so panels stop listing the session before its socket closes.
func (s *session) close() {
	if s.reg != "" {
		if err := registry.Remove(s.reg, s.id); err != nil {
			s.log.Warn("registry-remove-failed", "err", err)
		}
	}
	if s.api != nil {
		_ = s.api.Close()
	}
	if s.app != nil {
		s.app.stop()
	}
	if s.cancel != nil {
		s.cancel()
	}
	s.wg.Wait()
	if s.ctl != nil {
		_ = s.ctl.Close()
	}
	if err := os.RemoveAll(s.dir); err != nil && s.log != nil {
		s.log.Warn("runtime-dir-remove-failed", "err", err)
	}
	if s.log != nil {
		s.log.Info("session-end")
	}
	if s.restore != nil {
		s.restore()
	}
}
