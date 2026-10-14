package brain

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/alexei-led/codex-router/internal/appserver"
	"github.com/alexei-led/codex-router/internal/config"
	"github.com/alexei-led/codex-router/internal/control"
	"github.com/alexei-led/codex-router/internal/home"
	"github.com/alexei-led/codex-router/internal/link"
	"github.com/alexei-led/codex-router/internal/registry"
	"github.com/alexei-led/codex-router/internal/responses"
	"github.com/alexei-led/codex-router/internal/route"
)

const waitTime = 5 * time.Second

var (
	native = config.Route{Model: "gpt-6.1-sol"}
	luna   = config.Route{Model: "gpt-6-luna", Effort: config.EffortLow}
)

// fakeThread is a thread the fake daemon holds in memory.
type fakeThread struct {
	cwd       string
	source    string // threadSource; "" is null
	ephemeral bool
	subagent  bool
	route     config.Route
	noRollout int // resumes that fail with -32600 before one succeeds
}

func (th *fakeThread) json(id string) map[string]any {
	m := map[string]any{"id": id, "cwd": th.cwd, "ephemeral": th.ephemeral, "source": "cli", "threadSource": nil}
	if th.source != "" {
		m["threadSource"] = th.source
	}
	if th.subagent {
		m["source"] = map[string]any{"subAgent": "review"}
	}
	return m
}

type call struct {
	method, thread string
	route          config.Route
}

// fakeDaemon is a home's shared Codex daemon: JSON-RPC over WebSocket on a
// unix socket. It can stop and start again on the same path.
type fakeDaemon struct {
	t    *testing.T
	path string

	mu      sync.Mutex
	threads map[string]*fakeThread
	loaded  []string
	calls   []call
	conns   map[*websocket.Conn]struct{}
	srv     *http.Server
}

func newFakeDaemon(t *testing.T, path string) *fakeDaemon {
	t.Helper()
	d := &fakeDaemon{t: t, path: path, threads: map[string]*fakeThread{}, conns: map[*websocket.Conn]struct{}{}}
	d.start()
	t.Cleanup(d.stop)
	return d
}

func (d *fakeDaemon) load(id string, th *fakeThread) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.threads[id] = th
	d.loaded = append(d.loaded, id)
}

func (d *fakeDaemon) start() {
	d.t.Helper()
	ln, err := net.Listen("unix", d.path)
	if err != nil {
		d.t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(d.serve)}
	d.mu.Lock()
	d.srv = srv
	d.mu.Unlock()
	go func() { _ = srv.Serve(ln) }()
}

func (d *fakeDaemon) stop() {
	d.mu.Lock()
	srv, conns := d.srv, d.conns
	d.srv, d.conns = nil, map[*websocket.Conn]struct{}{}
	d.mu.Unlock()
	if srv != nil {
		_ = srv.Close()
	}
	for c := range conns {
		_ = c.CloseNow()
	}
}

// restart drops every client and comes back with the threads' footers back
// at their native route, as a daemon that reloads them from rollouts.
func (d *fakeDaemon) restart() {
	d.stop()
	d.mu.Lock()
	for _, th := range d.threads {
		th.route = native
	}
	d.calls = nil
	d.mu.Unlock()
	d.start()
}

func (d *fakeDaemon) serve(w http.ResponseWriter, r *http.Request) {
	ws, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	d.mu.Lock()
	d.conns[ws] = struct{}{}
	d.mu.Unlock()
	defer func() {
		d.mu.Lock()
		delete(d.conns, ws)
		d.mu.Unlock()
		_ = ws.CloseNow()
	}()
	ctx := context.Background()
	for {
		_, b, err := ws.Read(ctx)
		if err != nil {
			return
		}
		var m struct {
			ID     *int64          `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if json.Unmarshal(b, &m) != nil || m.ID == nil {
			continue
		}
		result, code, notify := d.handle(m.Method, m.Params)
		reply := map[string]any{"id": *m.ID, "result": result}
		if code != 0 {
			reply = map[string]any{"id": *m.ID, "error": map[string]any{"code": code, "message": "no rollout found"}}
		}
		out, _ := json.Marshal(reply)
		if ws.Write(ctx, websocket.MessageText, out) != nil {
			return
		}
		if notify != nil {
			d.notify(appserver.MethodSettingsUpdated, notify)
		}
	}
}

func (d *fakeDaemon) handle(method string, params json.RawMessage) (result any, code int, notify map[string]any) {
	var p struct {
		ThreadID string        `json:"threadId"`
		Model    string        `json:"model"`
		Effort   config.Effort `json:"effort"`
	}
	_ = json.Unmarshal(params, &p)
	d.mu.Lock()
	defer d.mu.Unlock()
	d.calls = append(d.calls, call{method: method, thread: p.ThreadID, route: config.Route{Model: p.Model, Effort: p.Effort}})
	th := d.threads[p.ThreadID]
	switch method {
	case appserver.MethodLoadedList:
		return map[string]any{"data": slices.Clone(d.loaded), "nextCursor": nil}, 0, nil
	case appserver.MethodRead:
		if th == nil {
			return nil, -32600, nil
		}
		return map[string]any{"thread": th.json(p.ThreadID)}, 0, nil
	case appserver.MethodResume:
		if th == nil || th.noRollout > 0 {
			if th != nil {
				th.noRollout--
			}
			return nil, -32600, nil
		}
		return map[string]any{
			"thread": th.json(p.ThreadID), "model": th.route.Model, "reasoningEffort": th.route.Effort, "cwd": th.cwd,
		}, 0, nil
	case appserver.MethodSettingsUpdate:
		if th != nil {
			th.route = config.Route{Model: p.Model, Effort: p.Effort}
		}
		return map[string]any{}, 0, map[string]any{
			"threadId": p.ThreadID, "threadSettings": map[string]any{"model": p.Model, "effort": p.Effort},
		}
	case appserver.MethodUnsubscribe:
		return map[string]any{"status": "unsubscribed"}, 0, nil
	}
	return map[string]any{}, 0, nil
}

// notify sends a notification to every connected client.
func (d *fakeDaemon) notify(method string, params any) {
	out, _ := json.Marshal(map[string]any{"method": method, "params": params})
	d.mu.Lock()
	conns := make([]*websocket.Conn, 0, len(d.conns))
	for c := range d.conns {
		conns = append(conns, c)
	}
	d.mu.Unlock()
	for _, c := range conns {
		_ = c.Write(context.Background(), websocket.MessageText, out)
	}
}

func (d *fakeDaemon) clients() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.conns)
}

// called reports whether the daemon got method for thread (with route, when
// route is set).
func (d *fakeDaemon) called(method, thread string, r config.Route) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return slices.ContainsFunc(d.calls, func(c call) bool {
		return c.method == method && c.thread == thread && (r == config.Route{} || c.route == r)
	})
}

func (d *fakeDaemon) count(method, thread string) int {
	d.mu.Lock()
	defer d.mu.Unlock()
	n := 0
	for _, c := range d.calls {
		if c.method == method && c.thread == thread {
			n++
		}
	}
	return n
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(waitTime)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// clock is the fake clock the brain and engine read.
type clock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

// shortDir is a temp dir under /tmp: socket paths in it stay short.
func shortDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "brn")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

type rig struct {
	b     *Brain
	d     *fakeDaemon
	clock *clock
	home  home.Home
	env   map[string]string
}

func (r *rig) getenv(k string) string { return r.env[k] }

func (r *rig) stateDir() string { return filepath.Join(r.env["XDG_STATE_HOME"], "codex-router") }

func (r *rig) homeDir() string { return HomeDir(r.stateDir(), r.home.ID) }

func newRig(t *testing.T, state string) *rig {
	t.Helper()
	tmp := shortDir(t)
	if state == "" {
		state = filepath.Join(tmp, "state")
	}
	r := &rig{
		clock: &clock{now: time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)},
		home:  home.Home{ID: "3fa9c0d1e2b4", Path: filepath.Join(tmp, "codex-work"), Name: "work"},
		env:   map[string]string{"XDG_STATE_HOME": state, "TMPDIR": tmp},
	}
	r.d = newFakeDaemon(t, filepath.Join(tmp, "d.sock"))
	return r
}

func (r *rig) start(t *testing.T) {
	t.Helper()
	b, err := Start(Options{
		Home:   r.home,
		Deps:   route.Deps{Config: config.Default(), Now: r.clock.Now},
		Getenv: r.getenv,
		Daemon: r.d.path,
		Idle:   IdleAfter,
		Sweep:  5 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	r.b = b
	t.Cleanup(b.Close)
}

func userStep(thread string) responses.Meta {
	return responses.Meta{ThreadID: thread, TurnID: "tu-1", RequestKind: responses.RequestKindTurn, ThreadSource: responses.ThreadSourceUser}
}

func TestBrainResumesLoadedMainThreads(t *testing.T) {
	r := newRig(t, "")
	r.d.load("t1", &fakeThread{cwd: "/w1", source: "user", route: native})
	r.d.load("t2", &fakeThread{cwd: "/w2", route: native})
	r.d.load("title", &fakeThread{cwd: "/w1", source: "thread_title", ephemeral: true})
	r.d.load("review", &fakeThread{cwd: "/w1", source: "user", subagent: true})
	r.start(t)

	for _, id := range []string{"t1", "t2"} {
		eventually(t, "resume "+id, func() bool { return r.d.called(appserver.MethodResume, id, config.Route{}) })
	}
	for id, cwd := range map[string]string{"t1": "/w1", "t2": "/w2"} {
		if got := r.b.threads.cwd(id); got != cwd {
			t.Errorf("cwd(%s) = %q, want %q", id, got, cwd)
		}
	}
	for _, id := range []string{"title", "review"} {
		if r.d.called(appserver.MethodResume, id, config.Route{}) || r.b.threads.known(id) {
			t.Errorf("side thread %s is followed", id)
		}
	}
	if r.d.called(appserver.MethodSettingsUpdate, "t1", config.Route{}) {
		t.Error("a footer was written before any route was decided")
	}
}

func TestBrainServesControlLinkAndRegistry(t *testing.T) {
	r := newRig(t, "")
	r.start(t)

	root, err := registry.Root(r.getenv)
	if err != nil {
		t.Fatal(err)
	}
	eps, _, err := registry.List(root)
	if err != nil || len(eps) != 1 {
		t.Fatalf("registry = %+v, %v; want one entry", eps, err)
	}
	ep := eps[0]
	if ep.ID != EndpointID(r.home.ID) || ep.Kind != registry.KindBrain || ep.Home != r.home || ep.Protocol != control.Protocol {
		t.Errorf("entry = %+v", ep)
	}
	c, err := control.Dial(ep.Socket)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	if h := c.Hello(); h.EndpointID != ep.ID || h.Kind != registry.KindBrain || h.Home != r.home {
		t.Errorf("hello = %+v", h)
	}

	lc := link.NewClient(link.ClientOptions{Path: LinkPath(r.homeDir()), HomeID: r.home.ID})
	defer func() { _ = lc.Close() }()
	if !lc.Up(t.Context()) {
		t.Fatal("the edge cannot reach the brain over the link socket")
	}
	if _, err := os.Stat(filepath.Join(r.homeDir(), pathFile)); !os.IsNotExist(err) {
		t.Errorf("%s written for a short socket path (err %v)", pathFile, err)
	}

	r.b.Close()
	if eps, _, _ := registry.List(root); len(eps) != 0 {
		t.Errorf("registry after Close = %+v, want empty", eps)
	}
}

func TestBrainPicksUpStartedThreads(t *testing.T) {
	r := newRig(t, "")
	r.start(t)
	eventually(t, "daemon connection", func() bool { return r.d.count(appserver.MethodLoadedList, "") > 0 })

	r.d.load("t3", &fakeThread{cwd: "/w3", source: "user", route: native})
	r.d.notify(appserver.MethodThreadStarted, map[string]any{"thread": map[string]any{"id": "title", "cwd": "/w3", "ephemeral": true, "source": "cli", "threadSource": "thread_title"}})
	r.d.notify(appserver.MethodThreadStarted, map[string]any{"thread": map[string]any{"id": "t3", "cwd": "/w3", "ephemeral": false, "source": "cli", "threadSource": "user"}})

	eventually(t, "resume t3", func() bool { return r.d.called(appserver.MethodResume, "t3", config.Route{}) })
	if got := r.b.threads.cwd("t3"); got != "/w3" {
		t.Errorf("cwd(t3) = %q, want /w3", got)
	}
	if r.b.threads.known("title") {
		t.Error("an ephemeral side thread is followed")
	}
}

func TestBrainFollowsAThreadSeenOnTheLinkOnceItHasARollout(t *testing.T) {
	r := newRig(t, "")
	r.d.load("t9", &fakeThread{cwd: "/w9", source: "user", route: native, noRollout: 1})
	r.d.mu.Lock()
	r.d.loaded = nil // started before the brain, missed by its loaded list
	r.d.mu.Unlock()
	r.start(t)
	eventually(t, "daemon connection", func() bool { return r.d.count(appserver.MethodLoadedList, "") > 0 })

	h := linkHandler{r.b}
	h.Decide(t.Context(), nil, responses.Meta{ThreadID: "t9", RequestKind: "compaction"})
	time.Sleep(20 * time.Millisecond)
	if r.d.called(appserver.MethodRead, "t9", config.Route{}) {
		t.Fatal("a request that is not a user step made the brain look the thread up")
	}

	h.Decide(t.Context(), nil, userStep("t9"))
	eventually(t, "first resume of t9", func() bool { return r.d.count(appserver.MethodResume, "t9") == 1 })
	if got := r.b.threads.cwd("t9"); got != "/w9" {
		t.Errorf("cwd(t9) = %q, want /w9", got)
	}
	h.Decide(t.Context(), nil, userStep("t9"))
	eventually(t, "resume retried after the first turn", func() bool { return r.d.count(appserver.MethodResume, "t9") == 2 })
	eventually(t, "t9 subscribed", func() bool {
		r.b.threads.mu.Lock()
		defer r.b.threads.mu.Unlock()
		th := r.b.threads.m["t9"]
		return th != nil && th.subscribed
	})
}

func TestBrainUnsubscribesIdleThreads(t *testing.T) {
	r := newRig(t, "")
	r.d.load("t1", &fakeThread{cwd: "/w1", source: "user", route: native})
	r.d.load("t2", &fakeThread{cwd: "/w2", source: "user", route: native})
	r.d.load("t3", &fakeThread{cwd: "/w3", source: "user", route: native})
	r.start(t)
	for _, id := range []string{"t1", "t2", "t3"} {
		eventually(t, "resume "+id, func() bool { return r.d.called(appserver.MethodResume, id, config.Route{}) })
	}

	r.d.notify(appserver.MethodThreadClosed, map[string]any{"threadId": "t3"})
	eventually(t, "closed t3 forgotten", func() bool { return !r.b.threads.known("t3") })

	r.clock.advance(6 * time.Minute)
	r.d.notify(appserver.MethodTurnStarted, map[string]any{"threadId": "t2", "turn": map[string]any{}})
	eventually(t, "turn on t2 seen", func() bool { return len(r.b.threads.idle(6*time.Minute)) == 1 })
	r.clock.advance(5 * time.Minute)

	eventually(t, "unsubscribe t1", func() bool { return r.d.called(appserver.MethodUnsubscribe, "t1", config.Route{}) })
	time.Sleep(30 * time.Millisecond)
	if r.d.called(appserver.MethodUnsubscribe, "t2", config.Route{}) || !r.b.threads.known("t2") {
		t.Error("t2 had a turn 5 minutes ago and was dropped")
	}
	if r.d.called(appserver.MethodUnsubscribe, "t3", config.Route{}) {
		t.Error("a closed thread was unsubscribed")
	}
	if r.b.threads.known("t1") {
		t.Error("idle t1 is still followed")
	}
}

func TestBrainResyncsFootersAfterADaemonRestart(t *testing.T) {
	r := newRig(t, "")
	r.d.load("t1", &fakeThread{cwd: "/w1", source: "user", route: native})
	r.start(t)
	eventually(t, "resume t1", func() bool { return r.d.called(appserver.MethodResume, "t1", config.Route{}) })

	r.b.OnDecision(route.Decision{ThreadID: "t1", Route: luna})
	eventually(t, "footer write", func() bool { return r.d.called(appserver.MethodSettingsUpdate, "t1", luna) })

	r.d.restart()
	eventually(t, "resume after restart", func() bool { return r.d.called(appserver.MethodResume, "t1", config.Route{}) })
	eventually(t, "footer rewritten after restart", func() bool { return r.d.called(appserver.MethodSettingsUpdate, "t1", luna) })
	if n := r.d.count(appserver.MethodSettingsUpdate, "t1"); n != 1 {
		t.Errorf("footer writes after restart = %d, want 1", n)
	}
	if n := r.d.clients(); n != 1 {
		t.Errorf("daemon clients = %d, want 1", n)
	}
}

func TestBrainSkipsTheResyncWhenTheFooterIsCurrent(t *testing.T) {
	r := newRig(t, "")
	r.d.load("t1", &fakeThread{cwd: "/w1", source: "user", route: native})
	r.start(t)
	eventually(t, "resume t1", func() bool { return r.d.called(appserver.MethodResume, "t1", config.Route{}) })
	r.b.OnDecision(route.Decision{ThreadID: "t1", Route: luna})
	eventually(t, "footer write", func() bool { return r.d.called(appserver.MethodSettingsUpdate, "t1", luna) })

	r.d.stop()
	r.d.mu.Lock()
	r.d.calls = nil
	r.d.mu.Unlock()
	r.d.start() // the footer kept luna
	eventually(t, "resume after reconnect", func() bool { return r.d.called(appserver.MethodResume, "t1", config.Route{}) })
	time.Sleep(30 * time.Millisecond)
	if r.d.called(appserver.MethodSettingsUpdate, "t1", config.Route{}) {
		t.Error("the footer already showed the route and was written again")
	}
}

func TestBrainWaitsForTheDaemon(t *testing.T) {
	r := newRig(t, "")
	r.d.stop()
	r.d.load("t1", &fakeThread{cwd: "/w1", source: "user", route: native})
	r.start(t)
	lc := link.NewClient(link.ClientOptions{Path: LinkPath(r.homeDir()), HomeID: r.home.ID})
	defer func() { _ = lc.Close() }()
	if !lc.Up(t.Context()) {
		t.Fatal("the link is down while the daemon is")
	}
	r.d.start()
	eventually(t, "resume once the daemon is up", func() bool { return r.d.called(appserver.MethodResume, "t1", config.Route{}) })
}

func TestBrainMovesLongSocketPathsToTmp(t *testing.T) {
	long := filepath.Join(shortDir(t), strings.Repeat("s", 90))
	r := newRig(t, long)
	r.start(t)

	dir := r.homeDir()
	rec, err := os.ReadFile(filepath.Join(dir, pathFile))
	if err != nil {
		t.Fatalf("no link path record: %v", err)
	}
	want := filepath.Join(r.env["TMPDIR"], "codex-router-"+strconv.Itoa(os.Getuid()), r.home.ID+".sock")
	if got := strings.TrimSpace(string(rec)); got != want {
		t.Errorf("recorded %q, want %q", got, want)
	}
	if got := LinkPath(dir); got != want {
		t.Errorf("LinkPath = %q, want %q", got, want)
	}
	lc := link.NewClient(link.ClientOptions{Path: LinkPath(dir), HomeID: r.home.ID})
	defer func() { _ = lc.Close() }()
	if !lc.Up(t.Context()) {
		t.Error("the edge cannot reach the brain at the recorded path")
	}

	r.b.Close()
	// A brain that fits again removes the record.
	if err := recordLinkPath(dir, filepath.Join(dir, link.SocketName)); err != nil {
		t.Fatal(err)
	}
	if got := LinkPath(dir); got != filepath.Join(dir, link.SocketName) {
		t.Errorf("LinkPath after the record went = %q", got)
	}
}

func TestBrainRefusesAnOpenStateDir(t *testing.T) {
	r := newRig(t, "")
	app := r.stateDir()
	if err := os.MkdirAll(app, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(app, 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := Start(Options{Home: r.home, Deps: route.Deps{Config: config.Default()}, Getenv: r.getenv, Daemon: r.d.path})
	if err == nil || !strings.Contains(err.Error(), "must not be accessible") {
		t.Errorf("Start = %v, want a refusal", err)
	}
}
