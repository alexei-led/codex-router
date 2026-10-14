package brain

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/alexei-led/codex-router/internal/appserver"
	"github.com/alexei-led/codex-router/internal/config"
	"github.com/alexei-led/codex-router/internal/control"
	"github.com/alexei-led/codex-router/internal/home"
	"github.com/alexei-led/codex-router/internal/launcher"
	"github.com/alexei-led/codex-router/internal/link"
	"github.com/alexei-led/codex-router/internal/registry"
	"github.com/alexei-led/codex-router/internal/responses"
	"github.com/alexei-led/codex-router/internal/route"
)

// IdleAfter is how long a followed thread may go without a turn, a settings
// event or a routed request before the brain takes its TUI for gone and
// unsubscribes, so the daemon can unload it. The daemon keeps a thread left
// by /new loaded for a long time, so the brain cannot wait for an unload.
const IdleAfter = 10 * time.Minute

const (
	sweepEvery = time.Minute
	rpcTimeout = 5 * time.Second
	redialMin  = 250 * time.Millisecond
	redialMax  = 8 * time.Second

	homesDir = "homes"
	// pathFile records the link socket when it lives outside the home's
	// state dir; the edge reads it through LinkPath.
	pathFile = "brain.path"
)

// HomeDir is the stage 2 state dir of a home, <state>/homes/<home id>/,
// where state is codex-router's state dir (registry.StateDir).
func HomeDir(state, homeID string) string {
	return filepath.Join(state, homesDir, homeID)
}

// LinkPath is the link socket of the brain whose home state dir is dir:
// brain.sock in dir, or the path the brain recorded there because that one
// was too long for a unix socket. The edge reads it once at start.
func LinkPath(dir string) string {
	if b, err := os.ReadFile(filepath.Join(dir, pathFile)); err == nil {
		if p := strings.TrimSpace(string(b)); filepath.IsAbs(p) {
			return p
		}
	}
	return filepath.Join(dir, link.SocketName)
}

// EndpointID is the registry id of a home's brain. It is stable, so a
// restarted brain replaces its own entry.
func EndpointID(homeID string) string { return "brain-" + homeID }

// Options configure a Brain.
type Options struct {
	Home home.Home
	// Deps carry the engine's config, catalog, classifier, logger and clock;
	// the brain sets Home, Sink and Cwd.
	Deps route.Deps
	// Getenv reads XDG_STATE_HOME and HOME (state dir and registry) and
	// TMPDIR (short socket paths).
	Getenv func(string) string
	// Daemon is the daemon socket; empty means appserver.DaemonSocket of
	// Home.Path.
	Daemon string
	// Dialer reaches the daemon; nil means appserver.WebSocket.
	Dialer appserver.Dialer
	// Idle and Sweep override IdleAfter and the idle check's period; tests
	// only.
	Idle, Sweep time.Duration
}

// Brain is one home's running brain.
type Brain struct {
	o       Options
	log     *slog.Logger
	eng     *route.Engine
	w       *launcher.SettingsWriter
	threads *threads
	ctl     atomic.Pointer[control.Server]
	link    *link.Server
	reg     string // registry root once the entry is written
	kick    chan struct{}
	cancel  context.CancelFunc
	wg      sync.WaitGroup
}

// Run starts a brain and serves until ctx ends.
func Run(ctx context.Context, o Options) error {
	b, err := Start(o)
	if err != nil {
		return err
	}
	<-ctx.Done()
	b.Close()
	return nil
}

// Start makes the home's state dir, serves control v2 and the link socket,
// registers the endpoint and starts following the daemon, which need not be
// up yet.
func Start(o Options) (b *Brain, err error) {
	if o.Daemon == "" {
		o.Daemon = appserver.DaemonSocket(o.Home.Path)
	}
	if o.Dialer == nil {
		o.Dialer = appserver.WebSocket()
	}
	if o.Idle <= 0 {
		o.Idle = IdleAfter
	}
	if o.Sweep <= 0 {
		o.Sweep = sweepEvery
	}
	if o.Deps.Now == nil {
		o.Deps.Now = time.Now
	}
	if o.Deps.Logger == nil {
		o.Deps.Logger = slog.New(slog.DiscardHandler)
	}
	b = &Brain{
		o: o, log: o.Deps.Logger, w: launcher.NewSettingsWriter(o.Deps.Logger, false),
		threads: newThreads(o.Deps.Now), kick: make(chan struct{}, 1),
	}
	defer func() {
		if err != nil {
			b.Close()
			b = nil
		}
	}()
	dir, err := b.stateDir()
	if err != nil {
		return b, err
	}
	linkPath, err := b.socketPath(dir, link.SocketName, o.Home.ID+".sock")
	if err != nil {
		return b, err
	}
	if err := recordLinkPath(dir, linkPath); err != nil {
		return b, err
	}
	ctlPath, err := b.socketPath(dir, control.SocketName, o.Home.ID+"-"+control.SocketName)
	if err != nil {
		return b, err
	}

	d := o.Deps
	d.Home, d.Sink, d.Cwd = o.Home, b, b.threads.cwd
	store := newIntentStore(dir, b.log)
	d.Intent, d.IntentLost = store.load()
	d.SaveIntent = store.save
	b.eng = route.New(d)
	id := EndpointID(o.Home.ID)
	hello := control.Hello{EndpointID: id, Kind: registry.KindBrain, Home: o.Home}
	ctl, err := control.Listen(ctlPath, endpoint{b.eng, hello})
	if err != nil {
		return b, err
	}
	b.ctl.Store(ctl)
	if b.link, err = link.Listen(linkPath, o.Home.ID, linkHandler{b}, b.log); err != nil {
		return b, err
	}
	b.register(registry.Endpoint{
		ID: id, Kind: registry.KindBrain, Protocol: control.Protocol,
		PID: os.Getpid(), Started: time.Now(), Home: o.Home, Socket: ctlPath,
	})
	ctx, cancel := context.WithCancel(context.Background())
	b.cancel = cancel
	b.wg.Go(func() { b.followDaemon(ctx) })
	b.log.Info("brain-ready", "home", o.Home.ID, "link", linkPath, "control", ctlPath)
	return b, nil
}

// Close stops the brain. The registry entry goes first, so panels stop
// listing it; then the link (the edge forwards unchanged), the daemon
// connection and the control socket.
func (b *Brain) Close() {
	if b.reg != "" {
		if err := registry.Remove(b.reg, EndpointID(b.o.Home.ID)); err != nil {
			b.log.Warn("registry-remove-failed", "err", err)
		}
	}
	if b.link != nil {
		_ = b.link.Close()
	}
	if b.cancel != nil {
		b.cancel()
	}
	b.wg.Wait()
	if c := b.ctl.Load(); c != nil {
		_ = c.Close()
	}
}

// stateDir makes <state>/homes/<home id>/ (0700) and checks it and its
// parents up to codex-router/ are private: whoever can write there can
// take the link socket.
func (b *Brain) stateDir() (string, error) {
	app, err := registry.StateDir(b.o.Getenv)
	if err != nil {
		return "", err
	}
	dir := HomeDir(app, b.o.Home.ID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("brain: %w", err)
	}
	for _, d := range []string{app, filepath.Dir(dir), dir} {
		if err := registry.CheckPrivate(d); err != nil {
			return "", fmt.Errorf("brain: %w", err)
		}
	}
	return dir, nil
}

// sunPathLen is the size of sockaddr_un's path, NUL included.
func sunPathLen() int {
	if runtime.GOOS == "darwin" {
		return 104
	}
	return 108
}

// socketPath is name in dir, or short in $TMPDIR/codex-router-<uid>/ (0700)
// when that path does not fit a unix socket address.
func (b *Brain) socketPath(dir, name, short string) (string, error) {
	p := filepath.Join(dir, name)
	if len(p) < sunPathLen() {
		return p, nil
	}
	tmp := b.o.Getenv("TMPDIR")
	if !filepath.IsAbs(tmp) {
		tmp = "/tmp"
	}
	alt := filepath.Join(tmp, fmt.Sprintf("codex-router-%d", os.Getuid()))
	if err := os.MkdirAll(alt, 0o700); err != nil {
		return "", fmt.Errorf("brain: %w", err)
	}
	if err := registry.CheckPrivate(alt); err != nil {
		return "", fmt.Errorf("brain: %w", err)
	}
	if p = filepath.Join(alt, short); len(p) >= sunPathLen() {
		return "", fmt.Errorf("brain: socket path %s is too long", p)
	}
	return p, nil
}

// recordLinkPath writes pathFile when the link socket is outside dir, and
// removes a stale one when it is not.
func recordLinkPath(dir, linkPath string) error {
	rec := filepath.Join(dir, pathFile)
	if filepath.Dir(linkPath) == dir {
		if err := os.Remove(rec); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("brain: %w", err)
		}
		return nil
	}
	if err := writeAtomic(dir, pathFile, []byte(linkPath+"\n")); err != nil {
		return fmt.Errorf("brain: record link path: %w", err)
	}
	return nil
}

// register lists the brain in the shared registry. A brain that cannot
// register still routes, so a failure only warns: panels will not list it.
func (b *Brain) register(ep registry.Endpoint) {
	root, err := registry.Root(b.o.Getenv)
	if err == nil {
		err = registry.Write(root, ep)
	}
	if err != nil {
		b.log.Warn("registry-write-failed", "err", err)
		return
	}
	b.reg = root
}

// OnDecision queues a routed thread's footer write. It never blocks.
func (b *Brain) OnDecision(d route.Decision) {
	if d.ThreadID != "" && d.Route.Model != "" {
		b.w.Enqueue(d.ThreadID, d.Route)
	}
}

// OnSnapshot passes a thread's snapshot to the control socket.
func (b *Brain) OnSnapshot(s route.Snapshot) {
	if c := b.ctl.Load(); c != nil {
		c.Publish(s)
	}
}

// touch records activity on a thread and wakes the daemon worker when that
// gives it work.
func (b *Brain) touch(id string, lookup bool) {
	if b.threads.touch(id, lookup) {
		b.poke()
	}
}

func (b *Brain) poke() {
	select {
	case b.kick <- struct{}{}:
	default:
	}
}

// endpoint is the engine as the control socket serves it.
type endpoint struct {
	*route.Engine
	hello control.Hello
}

func (e endpoint) Hello() control.Hello { return e.hello }

// linkHandler is the engine as the edge sees it. A user step on a thread the
// brain does not follow yet makes it look the thread up; any request or
// reply on a followed thread counts as activity.
type linkHandler struct{ b *Brain }

func (h linkHandler) Decide(ctx context.Context, req *responses.Request, m responses.Meta) (responses.Rewrite, bool) {
	if m.ThreadID != "" {
		h.b.touch(m.ThreadID, responses.Classify(m) == responses.KindUserStep)
	}
	return h.b.eng.Decide(ctx, req, m)
}

func (h linkHandler) Observe(m responses.Meta, served string, u responses.Usage, status int) {
	if m.ThreadID != "" {
		h.b.touch(m.ThreadID, false)
	}
	h.b.eng.Observe(m, served, u, status)
}

// followDaemon keeps a connection to the home's daemon until ctx ends,
// redialing with backoff: plain codex starts the daemon on demand, and a
// restarted daemon comes back on the same socket.
func (b *Brain) followDaemon(ctx context.Context) {
	delay, down := redialMin, false
	for ctx.Err() == nil {
		c, err := b.connect(ctx)
		if err != nil {
			if !down {
				b.log.Warn("daemon-unreachable", "socket", b.o.Daemon, "err", err)
				down = true
			}
			select {
			case <-ctx.Done():
			case <-time.After(delay):
			}
			delay = min(delay*2, redialMax)
			continue
		}
		delay, down = redialMin, false
		b.log.Info("daemon-connected")
		b.serveDaemon(ctx, c)
		b.threads.lost()
		if ctx.Err() == nil {
			b.log.Warn("daemon-disconnected")
		}
	}
}

func (b *Brain) connect(ctx context.Context) (*appserver.Client, error) {
	ctx, cancel := context.WithTimeout(ctx, rpcTimeout)
	defer cancel()
	c, err := appserver.DialDaemon(ctx, b.o.Daemon, b.o.Dialer)
	if err != nil {
		return nil, err
	}
	if err := c.Initialize(ctx); err != nil {
		_ = c.Close()
		return nil, err
	}
	return c, nil
}

// serveDaemon follows the daemon on one connection until it is lost or ctx
// ends. Notifications are drained on their own goroutine, which makes no
// RPC calls: a full channel stops the client's reads, and with them the
// replies this loop waits for.
func (b *Brain) serveDaemon(ctx context.Context, c *appserver.Client) {
	ctx, cancel := context.WithCancel(ctx)
	var wg sync.WaitGroup
	defer func() {
		cancel()
		_ = c.Close()
		wg.Wait()
	}()
	wg.Go(func() { b.w.Run(ctx, c) })
	wg.Go(func() {
		b.drain(c)
		cancel()
	})
	b.discover(ctx, c)
	tick := time.NewTicker(b.o.Sweep)
	defer tick.Stop()
	for {
		b.work(ctx, c)
		select {
		case <-ctx.Done():
			return
		case <-b.kick:
		case <-tick.C:
			b.sweep(ctx, c)
		}
	}
}

// drain applies notifications until the connection ends.
func (b *Brain) drain(c *appserver.Client) {
	updates, events := c.Updates(), c.Threads()
	for updates != nil || events != nil {
		select {
		case u, ok := <-updates:
			if !ok {
				updates = nil
				continue
			}
			r := config.Route{Model: u.Settings.Model, Effort: u.Settings.Effort}
			b.w.Reported(u.ThreadID, r)
			b.eng.SettingsUpdated(u.ThreadID, r)
			b.touch(u.ThreadID, false)
		case ev, ok := <-events:
			if !ok {
				events = nil
				continue
			}
			b.threadEvent(ev)
		}
	}
}

func (b *Brain) threadEvent(ev appserver.ThreadEvent) {
	switch ev.Method {
	case appserver.MethodThreadStarted:
		if ev.Thread.Main() && b.threads.add(ev.ThreadID, ev.Thread.Cwd) {
			b.log.Info("thread-started", "thread", ev.ThreadID)
			b.poke()
		}
	case appserver.MethodThreadClosed:
		if b.threads.known(ev.ThreadID) {
			b.forget(ev.ThreadID)
			b.log.Info("thread-closed", "thread", ev.ThreadID)
		}
	default:
		b.touch(ev.ThreadID, false)
	}
}

// discover follows every loaded main thread.
func (b *Brain) discover(ctx context.Context, c *appserver.Client) {
	rctx, cancel := context.WithTimeout(ctx, rpcTimeout)
	ids, err := c.LoadedThreads(rctx)
	cancel()
	if err != nil {
		b.log.Warn("daemon-loaded-list-failed", "err", err)
		return
	}
	for _, id := range ids {
		b.lookup(ctx, c, id)
	}
}

// lookup reads a thread the brain does not follow and follows it when it is
// a main thread.
func (b *Brain) lookup(ctx context.Context, c *appserver.Client, id string) {
	if b.threads.known(id) {
		return
	}
	rctx, cancel := context.WithTimeout(ctx, rpcTimeout)
	th, err := c.ReadThread(rctx, id)
	cancel()
	switch {
	case err != nil:
		b.log.Warn("thread-read-failed", "thread", id, "err", err)
	case th.Main() && b.threads.add(id, th.Cwd):
		b.log.Info("thread-found", "thread", id)
	}
}

// work looks up the unknown threads seen in routed requests, then resumes
// every thread due for it.
func (b *Brain) work(ctx context.Context, c *appserver.Client) {
	for _, id := range b.threads.takeLookups() {
		b.lookup(ctx, c, id)
	}
	for _, id := range b.threads.takeResumes() {
		b.resume(ctx, c, id)
	}
}

// resume subscribes to a thread and resyncs its footer: the route it reports
// is the footer's, and the last route the engine wanted is written again
// when they differ (a restarted daemon). A thread the engine has no request
// on yet (a brain start) is seeded with that route and held there until a
// reply measures its cache.
func (b *Brain) resume(ctx context.Context, c *appserver.Client, id string) {
	rctx, cancel := context.WithTimeout(ctx, rpcTimeout)
	r, err := c.Resume(rctx, id)
	cancel()
	var rpcErr *appserver.RPCError
	switch {
	case err == nil:
		reported := config.Route{Model: r.Settings.Model, Effort: r.Settings.Effort}
		b.threads.subscribed(id, r.Cwd)
		b.eng.Seed(id, reported)
		b.w.Resync(id, reported)
		b.log.Info("thread-subscribed", "thread", id)
	case errors.As(err, &rpcErr):
		// A thread has no rollout until its first turn; its next routed
		// request or turn retries.
		b.log.Info("thread-resume-skipped", "thread", id, "code", rpcErr.Code)
	default:
		b.log.Warn("thread-resume-failed", "thread", id, "err", err)
	}
}

// sweep unsubscribes from the threads idle for Options.Idle and stops
// following them; a later routed request on one looks it up again.
func (b *Brain) sweep(ctx context.Context, c *appserver.Client) {
	for _, id := range b.threads.idle(b.o.Idle) {
		if b.forget(id) {
			rctx, cancel := context.WithTimeout(ctx, rpcTimeout)
			if err := c.Unsubscribe(rctx, id); err != nil {
				b.log.Warn("thread-unsubscribe-failed", "thread", id, "err", err)
			}
			cancel()
		}
		b.log.Info("thread-idle", "thread", id)
	}
}

// forget stops following a thread and reports whether it was subscribed.
func (b *Brain) forget(id string) bool {
	sub := b.threads.remove(id)
	b.w.Forget(id)
	return sub
}
