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
	"github.com/alexei-led/codex-router/internal/catalog"
	"github.com/alexei-led/codex-router/internal/classifier"
	"github.com/alexei-led/codex-router/internal/config"
	"github.com/alexei-led/codex-router/internal/control"
	"github.com/alexei-led/codex-router/internal/home"
	"github.com/alexei-led/codex-router/internal/link"
	"github.com/alexei-led/codex-router/internal/registry"
	"github.com/alexei-led/codex-router/internal/responses"
	"github.com/alexei-led/codex-router/internal/route"
	"github.com/alexei-led/codex-router/internal/statedir"
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
	gate    chan struct{} // when set, resumes wait for it to close
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
		if g := d.gate; g != nil {
			d.mu.Unlock()
			<-g
			d.mu.Lock()
		}
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

// pick sets a thread's route without a notification, as a /model pick does
// for a client that is not subscribed.
func (d *fakeDaemon) pick(id string, r config.Route) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.threads[id].route = r
	d.calls = nil
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
	deps  route.Deps
	// models, when set, reloads the catalog from the home's models cache.
	models *catalog.CacheWatch
	quiet  int
}

func (r *rig) getenv(k string) string { return r.env[k] }

func (r *rig) stateDir() string { return filepath.Join(r.env["XDG_STATE_HOME"], "codex-router") }

func (r *rig) homeDir() string { return statedir.HomeDir(r.stateDir(), r.home.ID) }

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
	r.deps = route.Deps{Config: config.Default(), Now: r.clock.Now}
	return r
}

const testCatalog = `{"models":[
 {"slug":"gpt-6.1-sol","context_window":500000,"effective_context_window_percent":95,"comp_hash":"3000",
  "supported_reasoning_levels":[{"effort":"low"},{"effort":"medium"},{"effort":"high"}]},
 {"slug":"gpt-6-sol","context_window":272000,"effective_context_window_percent":95,"comp_hash":"3000",
  "supported_reasoning_levels":[{"effort":"low"},{"effort":"medium"},{"effort":"high"}]},
 {"slug":"gpt-6-luna","context_window":272000,"effective_context_window_percent":95,"comp_hash":"3000",
  "supported_reasoning_levels":[{"effort":"low"},{"effort":"medium"},{"effort":"high"}]}
]}`

// lowAsker advises the low tier on every call.
type lowAsker struct{}

func (lowAsker) Ask(context.Context, classifier.Input) (classifier.Advice, error) {
	return classifier.Advice{Choice: "low", Probabilities: map[classifier.Choice]float64{"low": 0.99}}, nil
}

func (lowAsker) Health() classifier.Health { return classifier.Health{} }

// routing gives the brain a catalog and a classifier, so its engine routes.
func (r *rig) routing(t *testing.T) {
	t.Helper()
	cat, err := catalog.Parse([]byte(testCatalog))
	if err != nil {
		t.Fatal(err)
	}
	store := &catalog.Store{}
	store.Set(cat)
	r.deps.Catalog, r.deps.Classifier = store, lowAsker{}
}

func step(r config.Route, items ...responses.Item) *responses.Request {
	if len(items) == 0 {
		items = []responses.Item{{Type: responses.TypeMessage, Role: "user", Text: "fix it"}}
	}
	return &responses.Request{Model: r.Model, Effort: r.Effort, Input: items}
}

func turn(thread, id string) responses.Meta {
	m := userStep(thread)
	m.TurnID = id
	return m
}

// routeThread pins a seeded thread's next turn to the low tier, runs it and
// waits for the footer write's echo. It returns the route written.
func (r *rig) routeThread(t *testing.T, id string) config.Route {
	t.Helper()
	eventually(t, "seed "+id, func() bool { return r.b.eng.SnapshotFor(id).Baseline == native })
	r.apply(t, route.Command{Op: route.OpPin, Session: id, Tier: config.TierLow})
	rw, ok := linkHandler{r.b}.Decide(t.Context(), step(native), turn(id, "tu-routed"))
	if !ok {
		t.Fatalf("%s: a pinned turn was not routed", id)
	}
	want := config.Route{Model: rw.Model, Effort: rw.Effort}
	eventually(t, "footer echo", func() bool {
		s := r.b.eng.SnapshotFor(id)
		return s.FooterSynced && s.Footer == want
	})
	return want
}

func (r *rig) quiesce(t *testing.T) {
	t.Helper()
	r.quiet++
	id := "quiesce-" + strconv.Itoa(r.quiet)
	r.d.mu.Lock()
	r.d.threads[id] = &fakeThread{cwd: "/q", source: "user", route: native}
	r.d.mu.Unlock()
	linkHandler{r.b}.Decide(t.Context(), step(native), userStep(id))
	eventually(t, "daemon worker pass for "+id, func() bool { return r.d.called(appserver.MethodResume, id, config.Route{}) })
	r.b.w.Enqueue(id, luna)
	eventually(t, "footer writer pass for "+id, func() bool { return r.d.called(appserver.MethodSettingsUpdate, id, luna) })
}

func (r *rig) start(t *testing.T) {
	t.Helper()
	b, err := Start(Options{
		Home:   r.home,
		Deps:   r.deps,
		Getenv: r.getenv,
		Daemon: r.d.path,
		Idle:   IdleAfter,
		Sweep:  5 * time.Millisecond,
		Models: r.models,
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

	lc := link.NewClient(link.ClientOptions{Path: func() string { return LinkPath(r.homeDir()) }, HomeID: r.home.ID})
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
	r.quiesce(t)
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
	r.quiesce(t)
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
	r.routing(t)
	r.d.load("t1", &fakeThread{cwd: "/w1", source: "user", route: native})
	r.start(t)
	routed := r.routeThread(t, "t1")

	r.d.restart()
	eventually(t, "resume after restart", func() bool { return r.d.called(appserver.MethodResume, "t1", config.Route{}) })
	eventually(t, "footer rewritten after restart", func() bool { return r.d.called(appserver.MethodSettingsUpdate, "t1", routed) })
	if n := r.d.count(appserver.MethodSettingsUpdate, "t1"); n != 1 {
		t.Errorf("footer writes after restart = %d, want 1", n)
	}
	if n := r.d.clients(); n != 1 {
		t.Errorf("daemon clients = %d, want 1", n)
	}
	if m := r.b.eng.SnapshotFor("t1").Mode; m != route.ModeOn {
		t.Errorf("mode = %s, want on", m)
	}
}

func TestBrainSkipsTheResyncWhenTheFooterIsCurrent(t *testing.T) {
	r := newRig(t, "")
	r.routing(t)
	r.d.load("t1", &fakeThread{cwd: "/w1", source: "user", route: native})
	r.start(t)
	r.routeThread(t, "t1")

	r.d.stop()
	r.d.mu.Lock()
	r.d.calls = nil
	r.d.mu.Unlock()
	r.d.start()
	eventually(t, "resume after reconnect", func() bool { return r.d.called(appserver.MethodResume, "t1", config.Route{}) })
	r.quiesce(t)
	if r.d.called(appserver.MethodSettingsUpdate, "t1", config.Route{}) {
		t.Error("the footer already showed the route and was written again")
	}
	if m := r.b.eng.SnapshotFor("t1").Mode; m != route.ModeOn {
		t.Errorf("mode = %s, want on", m)
	}
}

func TestBrainKeepsAPickMadeWhileTheDaemonWasAway(t *testing.T) {
	r := newRig(t, "")
	r.routing(t)
	r.d.load("t1", &fakeThread{cwd: "/w1", source: "user", route: native})
	r.start(t)
	r.routeThread(t, "t1")

	r.d.stop()
	picked := config.Route{Model: "gpt-6-sol", Effort: config.EffortHigh}
	r.d.pick("t1", picked)
	r.d.start()
	eventually(t, "resume after reconnect", func() bool { return r.d.called(appserver.MethodResume, "t1", config.Route{}) })
	eventually(t, "t1 manual", func() bool { return r.b.eng.SnapshotFor("t1").Mode == route.ModeManual })
	r.quiesce(t)
	if r.d.called(appserver.MethodSettingsUpdate, "t1", config.Route{}) {
		t.Error("the router's route overwrote the /model pick")
	}
}

func TestBrainTakesAPickOnAnIdleThreadFromItsRequest(t *testing.T) {
	r := newRig(t, "")
	r.routing(t)
	r.d.load("t1", &fakeThread{cwd: "/w1", source: "user", route: native})
	r.start(t)
	r.routeThread(t, "t1")
	r.clock.advance(IdleAfter + time.Minute)
	eventually(t, "unsubscribe t1", func() bool { return r.d.called(appserver.MethodUnsubscribe, "t1", config.Route{}) })

	picked := config.Route{Model: "gpt-6-sol", Effort: config.EffortHigh}
	r.d.pick("t1", picked)
	rw, ok := linkHandler{r.b}.Decide(t.Context(), step(picked), turn("t1", "tu-after-pick"))

	if ok {
		t.Fatalf("rewrite %+v over the /model pick", rw)
	}
	if s := r.session(t, "t1"); s.Attention != route.AttentionManual {
		t.Fatalf("session = %+v, want manual", s)
	}
	eventually(t, "resume of t1", func() bool { return r.d.called(appserver.MethodResume, "t1", config.Route{}) })
	r.quiesce(t)
	if r.d.called(appserver.MethodSettingsUpdate, "t1", config.Route{}) {
		t.Error("the router's route overwrote the /model pick")
	}
}

func TestBrainHoldsAThreadSeenBeforeItsResume(t *testing.T) {
	r := newRig(t, "")
	r.routing(t)
	r.d.load("t1", &fakeThread{cwd: "/w1", source: "user", route: native})
	gate := make(chan struct{})
	release := sync.OnceFunc(func() { close(gate) })
	t.Cleanup(release)
	r.d.gate = gate
	r.start(t)
	eventually(t, "resume of t1 sent", func() bool { return r.d.called(appserver.MethodResume, "t1", config.Route{}) })

	midTurn := step(native,
		responses.Item{Type: responses.TypeMessage, Role: "user", Text: "fix it"},
		responses.Item{Type: responses.TypeFunctionCall, Name: "exec_command", CallID: "c1"},
		responses.Item{Type: responses.TypeFunctionCallOutput, CallID: "c1", Output: "ok"},
	)
	rw, ok := linkHandler{r.b}.Decide(t.Context(), midTurn, userStep("t1"))

	if ok {
		t.Fatalf("rewrite %+v before the cache was measured", rw)
	}
	if s := r.session(t, "t1"); s.Attention != route.AttentionUnknown {
		t.Fatalf("session = %+v, want held as unknown", s)
	}
	release()
	eventually(t, "t1 subscribed", func() bool {
		r.b.threads.mu.Lock()
		defer r.b.threads.mu.Unlock()
		th := r.b.threads.m["t1"]
		return th != nil && th.subscribed
	})
	if s := r.session(t, "t1"); s.Attention != route.AttentionUnknown {
		t.Fatalf("after the resume: session = %+v, want still held", s)
	}
}

func TestBrainReloadsTheModelsCacheCodexRewrites(t *testing.T) {
	r := newRig(t, "")
	store := &catalog.Store{}
	r.deps.Catalog, r.deps.Classifier = store, lowAsker{}
	if err := os.MkdirAll(r.home.Path, 0o700); err != nil {
		t.Fatal(err)
	}
	cache := filepath.Join(r.home.Path, catalog.CacheFile)
	r.models = catalog.NewCacheWatch(cache, store, r.clock.Now)
	r.start(t)
	h := linkHandler{r.b}
	sent := config.Route{Model: "gpt-6.1-sol", Effort: config.EffortHigh}

	if rw, ok := h.Decide(t.Context(), step(sent), turn("t1", "tu-1")); ok {
		t.Fatalf("rewrite %+v without a catalog", rw)
	}
	if err := os.WriteFile(cache, []byte(testCatalog), 0o600); err != nil {
		t.Fatal(err)
	}
	if rw, ok := h.Decide(t.Context(), step(sent), turn("t1", "tu-2")); ok {
		t.Fatalf("rewrite %+v: the cache was read again within %v", rw, catalog.CacheCheckEvery)
	}
	r.clock.advance(catalog.CacheCheckEvery)
	if rw, ok := h.Decide(t.Context(), step(sent), turn("t1", "tu-3")); !ok || rw.Model != "gpt-6-luna" {
		t.Fatalf("Decide = %+v, %v; want gpt-6-luna from the rewritten cache", rw, ok)
	}
}

func TestBrainWaitsForTheDaemon(t *testing.T) {
	r := newRig(t, "")
	r.d.stop()
	r.d.load("t1", &fakeThread{cwd: "/w1", source: "user", route: native})
	r.start(t)
	lc := link.NewClient(link.ClientOptions{Path: func() string { return LinkPath(r.homeDir()) }, HomeID: r.home.ID})
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
	lc := link.NewClient(link.ClientOptions{Path: func() string { return LinkPath(dir) }, HomeID: r.home.ID})
	defer func() { _ = lc.Close() }()
	if !lc.Up(t.Context()) {
		t.Error("the edge cannot reach the brain at the recorded path")
	}

	r.b.Close()
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

func TestBrainCountsAReplyAsActivityAndEndsTheSeededHold(t *testing.T) {
	r := newRig(t, "")
	r.d.load("replied", &fakeThread{cwd: "/w1", source: "user", route: native})
	r.d.load("silent", &fakeThread{cwd: "/w2", source: "user", route: native})
	r.start(t)
	for _, id := range []string{"replied", "silent"} {
		eventually(t, "seed "+id, func() bool { return r.b.eng.SnapshotFor(id).Baseline == native })
	}
	h := linkHandler{r.b}
	h.Decide(t.Context(), step(native), userStep("replied"))
	if s := r.session(t, "replied"); s.Attention != route.AttentionUnknown {
		t.Fatalf("session = %+v, want held as unknown", s)
	}

	r.clock.advance(IdleAfter - time.Minute)
	h.Observe(userStep("replied"), native.Model, responses.Usage{InputTokens: 1000, TotalTokens: 1100}, 200)
	r.clock.advance(2 * time.Minute)

	eventually(t, "unsubscribe silent", func() bool { return r.d.called(appserver.MethodUnsubscribe, "silent", config.Route{}) })
	r.quiesce(t)
	if s := r.session(t, "replied"); s.Attention != "" {
		t.Errorf("attention after a measured reply = %q, want the hold ended", s.Attention)
	}
	if !r.b.threads.known("replied") || r.d.called(appserver.MethodUnsubscribe, "replied", config.Route{}) {
		t.Error("a thread with a reply a minute ago was dropped as idle")
	}
}
