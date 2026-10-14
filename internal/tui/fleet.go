package tui

import (
	"cmp"
	"context"
	"errors"
	"maps"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/alexei-led/codex-router/internal/control"
	"github.com/alexei-led/codex-router/internal/home"
	"github.com/alexei-led/codex-router/internal/registry"
	"github.com/alexei-led/codex-router/internal/route"
)

const (
	pollInterval = time.Second
	// lostGrace is how long a lost endpoint's sessions stay listed as
	// disconnected before they are dropped.
	lostGrace = 10 * time.Second
)

// errNotConnected is returned for a command whose target has no live
// connection.
var errNotConnected = errors.New("disconnected: command not sent")

// fleetSession is one session in the merged list: its engine's summary, the
// endpoint serving it, and whether that endpoint's connection is lost.
// Label is already disambiguated across the fleet.
type fleetSession struct {
	route.SessionInfo
	endpoint     string
	disconnected bool
}

// fleetEndpointInfo is one endpoint of a home as the panel shows it. Up is
// how long a live endpoint has run, 0 when its start is unknown.
type fleetEndpointInfo struct {
	id   string
	kind string
	lost bool
	up   time.Duration
}

// fleetHome is one Codex home: its endpoints and its sessions, most
// recently active first.
type fleetHome struct {
	home      home.Home
	endpoints []fleetEndpointInfo
	sessions  []fleetSession
}

// fleetState is a copy of the fleet for one render. Homes are ordered by
// name, then id. Snap is the focused session's newest snapshot, nil until
// one arrives.
type fleetState struct {
	homes        []fleetHome
	focus        string
	snap         *route.Snapshot
	disconnected bool // no live endpoint serves the focus; with no focus, none is connected
	ended        bool // locked mode: the session was listed, and now no endpoint lists it or its endpoint is gone
}

// focused returns the focused session.
func (s fleetState) focused() (fleetSession, bool) {
	for _, h := range s.homes {
		for _, x := range h.sessions {
			if x.ID == s.focus {
				return x, true
			}
		}
	}
	return fleetSession{}, false
}

// homeOf returns the home group with id.
func (s fleetState) homeOf(id string) (fleetHome, bool) {
	for _, h := range s.homes {
		if h.home.ID == id {
			return h, true
		}
	}
	return fleetHome{}, false
}

// fleetEndpoint is one dialed endpoint. c is nil while the connection is
// lost; gen tells a pump of an old connection from the current one.
type fleetEndpoint struct {
	ep      registry.Endpoint
	c       conn
	hello   control.Hello
	items   []route.SessionInfo
	listed  bool // its first sessions list arrived
	lostAt  time.Time
	gone    bool   // lost and its registry entry is gone: the endpoint exited
	session string // subscribed session, "" for none
	gen     int
}

// fleet watches the registry, keeps one control connection per live
// endpoint, and merges their sessions. It subscribes only the focused
// session's endpoint to that session. In locked mode the focus is fixed and
// every other session is ignored.
type fleet struct {
	list   func() ([]registry.Endpoint, error)
	dial   func(registry.Endpoint) (conn, error)
	prune  func(registry.Endpoint) // after a failed dial: drop the entry if its endpoint is gone
	now    func() time.Time
	cwd    string // the panel's cwd, for the initial focus
	locked string // session id, "" for all sessions

	changed chan struct{}
	submu   sync.Mutex // serializes subscription changes

	mu    sync.Mutex
	eps   map[string]*fleetEndpoint
	focus string
	auto  bool // focus is the most recent session, not a cwd match
	// provisional: auto picked while a live endpoint had not listed its
	// sessions yet, so a more recent session may still show up.
	provisional bool
	seen        bool // locked mode: the locked session was listed once
	seenBrain   bool // locked mode: a brain listed it last
	snap        *route.Snapshot
	gen         int
}

func newFleet(list func() ([]registry.Endpoint, error), dial func(registry.Endpoint) (conn, error)) *fleet {
	return &fleet{
		list: list, dial: dial, prune: func(registry.Endpoint) {}, now: time.Now,
		changed: make(chan struct{}, 1), eps: map[string]*fleetEndpoint{},
	}
}

// registrySource lists the registry at root.
func registrySource(root string) func() ([]registry.Endpoint, error) {
	return func() ([]registry.Endpoint, error) {
		eps, _, err := registry.List(root)
		return eps, err
	}
}

// socketSource is one fixed socket outside the registry. Its entry names no
// home, so the hello is not checked against it.
func socketSource(path string) func() ([]registry.Endpoint, error) {
	return func() ([]registry.Endpoint, error) {
		return []registry.Endpoint{{ID: path, Socket: path}}, nil
	}
}

// registryPrune removes the entry at root when its endpoint is dead.
func registryPrune(root string) func(registry.Endpoint) {
	return func(ep registry.Endpoint) {
		if !registry.Alive(ep) {
			_ = registry.Remove(root, ep.ID)
		}
	}
}

// run polls until ctx ends, then closes every connection.
func (f *fleet) run(ctx context.Context) {
	t := time.NewTicker(pollInterval)
	defer t.Stop()
	for {
		f.poll()
		select {
		case <-ctx.Done():
			f.closeAll()
			return
		case <-t.C:
		}
	}
}

// poll dials every listed endpoint without a live connection, in parallel,
// and drops lost endpoints past lostGrace. A lost brain stays, without its
// sessions, so its home reads brain down until it returns: a brain is a
// service, not a session. A lost endpoint whose entry left the registry is
// marked gone. An unreadable registry keeps the current connections.
func (f *fleet) poll() {
	eps, err := f.list()
	if err == nil {
		var wg sync.WaitGroup
		for _, ep := range eps {
			f.mu.Lock()
			e := f.eps[ep.ID]
			live := e != nil && e.c != nil
			f.mu.Unlock()
			if !live {
				wg.Go(func() { f.connect(ep) })
			}
		}
		wg.Wait()
	}
	now := f.now()
	f.mu.Lock()
	for id, e := range f.eps {
		if e.c == nil && now.Sub(e.lostAt) >= lostGrace {
			if e.hello.Kind == registry.KindBrain {
				e.items = nil
			} else {
				delete(f.eps, id)
			}
			continue
		}
		if e.c == nil && err == nil {
			e.gone = !slices.ContainsFunc(eps, func(ep registry.Endpoint) bool { return ep.ID == id })
		}
	}
	f.refocus()
	f.mu.Unlock()
	f.sync()
}

// connect dials ep and starts its pumps. A hello naming another endpoint or
// home means the entry is stale: the registry entry is a hint only (M3).
func (f *fleet) connect(ep registry.Endpoint) {
	c, err := f.dial(ep)
	if err != nil {
		f.prune(ep)
		return
	}
	h := c.Hello()
	if ep.Home.ID != "" && (h.EndpointID != ep.ID || h.Home.ID != ep.Home.ID) {
		_ = c.Close()
		return
	}
	f.mu.Lock()
	e := f.eps[ep.ID]
	if e == nil {
		e = &fleetEndpoint{}
		f.eps[ep.ID] = e
	}
	f.gen++
	*e = fleetEndpoint{ep: ep, c: c, hello: h, items: e.items, gen: f.gen}
	gen := f.gen
	f.mu.Unlock()
	go f.pumpSessions(ep.ID, gen, c)
	go f.pumpSnapshots(ep.ID, gen, c)
}

func (f *fleet) pumpSessions(id string, gen int, c conn) {
	for items := range c.Sessions() {
		f.update(id, gen, func(e *fleetEndpoint) { e.items, e.listed = items, true })
	}
	f.update(id, gen, func(e *fleetEndpoint) {
		_ = e.c.Close()
		e.c, e.lostAt, e.session = nil, f.now(), ""
	})
}

func (f *fleet) pumpSnapshots(id string, gen int, c conn) {
	for s := range c.Snapshots() {
		f.mu.Lock()
		e := f.eps[id]
		if e != nil && e.gen == gen && e.session == f.focus && s.ThreadID != "" &&
			f.focus == control.SessionID(e.hello.Home.ID, s.ThreadID) {
			f.snap = &s
		}
		f.mu.Unlock()
		f.notify()
	}
}

// update applies fn to endpoint id while its connection is still gen, then
// refocuses and syncs subscriptions.
func (f *fleet) update(id string, gen int, fn func(*fleetEndpoint)) {
	f.mu.Lock()
	e := f.eps[id]
	if e == nil || e.gen != gen {
		f.mu.Unlock()
		return
	}
	fn(e)
	f.refocus()
	f.mu.Unlock()
	f.sync()
}

func (f *fleet) notify() {
	select {
	case f.changed <- struct{}{}:
	default:
	}
}

// refocus keeps the focus while its session is listed. It picks again when
// the focus is gone, when a cwd match shows up for a focus that was only the
// most recent session, and while that pick is provisional. A pick is the
// session whose cwd is the deepest one equal to or containing the panel's
// cwd, else the most recently active. The caller holds f.mu.
func (f *fleet) refocus() {
	sessions := f.sessions()
	if f.locked != "" {
		f.focus = f.locked
		if len(sessions) > 0 {
			f.seen, f.seenBrain = true, f.eps[sessions[0].endpoint].hello.Kind == registry.KindBrain
		}
		return
	}
	listed := slices.ContainsFunc(sessions, func(s fleetSession) bool { return s.ID == f.focus })
	if listed && !f.auto {
		return
	}
	id, byCwd := initialFocus(sessions, f.cwd)
	if listed && !byCwd && !f.provisional {
		return
	}
	if id != f.focus {
		f.focus, f.snap = id, nil
	}
	f.auto = id != "" && !byCwd
	f.provisional = f.auto && f.waiting()
}

// waiting reports whether a live endpoint has not listed its sessions yet.
// The caller holds f.mu.
func (f *fleet) waiting() bool {
	for _, e := range f.eps {
		if e.c != nil && !e.listed {
			return true
		}
	}
	return false
}

// initialFocus returns the session with the deepest cwd equal to or
// containing cwd (byCwd), else the most recently active session.
func initialFocus(sessions []fleetSession, cwd string) (id string, byCwd bool) {
	best, depth := "", -1
	var recent *fleetSession
	for i := range sessions {
		s := &sessions[i]
		if recent == nil || s.LastActive.After(recent.LastActive) {
			recent = s
		}
		if d := len(s.Cwd); contains(s.Cwd, cwd) && d > depth {
			best, depth = s.ID, d
		}
	}
	switch {
	case best != "":
		return best, true
	case recent != nil:
		return recent.ID, false
	}
	return "", false
}

// contains reports whether dir is path or one of its ancestors.
func contains(dir, path string) bool {
	if !filepath.IsAbs(dir) || !filepath.IsAbs(path) {
		return false
	}
	rel, err := filepath.Rel(dir, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, "../")
}

// sessions merges every endpoint's sessions, home by home, with labels
// disambiguated. A session two endpoints list is taken from the more
// recently active report. Locked mode keeps only the locked session. The
// caller holds f.mu.
func (f *fleet) sessions() []fleetSession {
	byID := map[string]fleetSession{}
	for id, e := range f.eps {
		for _, it := range e.items {
			if f.locked != "" && it.ID != f.locked {
				continue
			}
			if old, ok := byID[it.ID]; ok && !it.LastActive.After(old.LastActive) {
				continue
			}
			byID[it.ID] = fleetSession{SessionInfo: it, endpoint: id, disconnected: e.c == nil}
		}
	}
	homes := f.homeNames()
	out := make([]fleetSession, 0, len(byID))
	for _, s := range byID {
		out = append(out, s)
	}
	slices.SortFunc(out, func(a, b fleetSession) int {
		return cmp.Or(
			cmp.Compare(homes[a.Home], homes[b.Home]), cmp.Compare(a.Home, b.Home),
			b.LastActive.Compare(a.LastActive), cmp.Compare(a.ID, b.ID))
	})
	labels, ids := make([]string, len(out)), make([]string, len(out))
	for i, s := range out {
		labels[i], ids[i] = s.Label, s.ID
	}
	for i, l := range home.Disambiguate(labels, ids) {
		out[i].Label = l
	}
	return out
}

// homeNames maps each connected home's id to its name. The caller holds f.mu.
func (f *fleet) homeNames() map[string]string {
	names := map[string]string{}
	for _, e := range f.eps {
		names[e.hello.Home.ID] = e.hello.Home.Name
	}
	return names
}

// sync makes each connection's subscription match the focus: the focused
// session's endpoint subscribes to it, every other endpoint to none. A
// failed Subscribe is not retried; a lost connection resubscribes on
// redial.
func (f *fleet) sync() {
	f.submu.Lock()
	defer f.submu.Unlock()
	for {
		f.mu.Lock()
		owner := f.owner(f.focus)
		var c conn
		var want string
		for id, e := range f.eps {
			w := ""
			if id == owner {
				w = f.focus
			}
			if e.c != nil && e.session != w {
				c, want, e.session = e.c, w, w
				break
			}
		}
		f.mu.Unlock()
		if c == nil {
			f.notify()
			return
		}
		_ = c.Subscribe(want)
	}
}

// owner returns the id of the live endpoint that lists session, else "".
// The caller holds f.mu.
func (f *fleet) owner(session string) string {
	if session == "" {
		return ""
	}
	best := ""
	var at time.Time
	for id, e := range f.eps {
		if e.c == nil {
			continue
		}
		for _, it := range e.items {
			if it.ID == session && (best == "" || it.LastActive.After(at)) {
				best, at = id, it.LastActive
			}
		}
	}
	return best
}

// focusOn moves the focus to session id when it is listed. Locked mode
// keeps its focus. A chosen focus is never replaced by a cwd match.
func (f *fleet) focusOn(id string) {
	f.mu.Lock()
	if f.locked != "" || id == f.focus ||
		!slices.ContainsFunc(f.sessions(), func(s fleetSession) bool { return s.ID == id }) {
		f.mu.Unlock()
		return
	}
	f.focus, f.snap, f.auto, f.provisional = id, nil, false, false
	f.mu.Unlock()
	f.sync()
}

// state copies the fleet for rendering.
func (f *fleet) state() fleetState {
	f.mu.Lock()
	defer f.mu.Unlock()
	st := fleetState{focus: f.focus, snap: f.snap}
	if f.focus != "" {
		st.disconnected = f.owner(f.focus) == ""
	} else {
		st.disconnected = true
		for e := range maps.Values(f.eps) {
			st.disconnected = st.disconnected && e.c == nil
		}
	}
	byHome := map[string]*fleetHome{}
	var order []string
	group := func(h home.Home) *fleetHome {
		g := byHome[h.ID]
		if g == nil {
			g = &fleetHome{home: h}
			byHome[h.ID] = g
			order = append(order, h.ID)
		}
		return g
	}
	sessions := f.sessions()
	// A brain's thread outlives the brain: a restarted brain lists it again at
	// its next user step, and an idle thread leaves the list after 10 min.
	// Neither ends the session, so a brain session never shows ended.
	st.ended = f.seen && !f.seenBrain && !slices.ContainsFunc(sessions, func(s fleetSession) bool { return !f.eps[s.endpoint].gone })
	for _, s := range sessions {
		g := group(f.eps[s.endpoint].hello.Home)
		g.sessions = append(g.sessions, s)
	}
	now := f.now()
	for id, e := range f.eps {
		g := group(e.hello.Home)
		info := fleetEndpointInfo{id: id, kind: e.hello.Kind, lost: e.c == nil}
		if !info.lost && !e.ep.Started.IsZero() {
			info.up = max(now.Sub(e.ep.Started), 0)
		}
		g.endpoints = append(g.endpoints, info)
	}
	for _, id := range order {
		g := byHome[id]
		slices.SortFunc(g.endpoints, func(a, b fleetEndpointInfo) int { return cmp.Compare(a.id, b.id) })
		st.homes = append(st.homes, *g)
	}
	slices.SortFunc(st.homes, func(a, b fleetHome) int {
		return cmp.Or(cmp.Compare(a.home.Name, b.home.Name), cmp.Compare(a.home.ID, b.home.ID))
	})
	return st
}

// send issues a session command through the endpoint serving cmd.Session.
func (f *fleet) send(cmd route.Command) error {
	f.mu.Lock()
	var c conn
	if e := f.eps[f.owner(cmd.Session)]; e != nil {
		c = e.c
	}
	f.mu.Unlock()
	if c == nil {
		return errNotConnected
	}
	return c.Send(cmd)
}

// sendHome issues cmd with scope home to every live endpoint of home, of any
// kind: each applies it to its own threads (M5).
func (f *fleet) sendHome(homeID string, cmd route.Command) error {
	cmd.Scope, cmd.Session = route.ScopeHome, ""
	f.mu.Lock()
	var cs []conn
	for _, e := range f.eps {
		if e.c != nil && e.hello.Home.ID == homeID {
			cs = append(cs, e.c)
		}
	}
	f.mu.Unlock()
	if len(cs) == 0 {
		return errNotConnected
	}
	var errs []error
	for _, c := range cs {
		errs = append(errs, c.Send(cmd))
	}
	return errors.Join(errs...)
}

// heartbeat asks the focused endpoint to re-send its snapshot, so times in
// the panel move while the session is idle.
func (f *fleet) heartbeat() {
	f.mu.Lock()
	var c conn
	if e := f.eps[f.owner(f.focus)]; e != nil {
		c = e.c
	}
	f.mu.Unlock()
	if c != nil {
		_ = c.Send(route.Command{Op: route.OpSnapshot})
	}
}

func (f *fleet) closeAll() {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, e := range f.eps {
		if e.c != nil {
			_ = e.c.Close()
		}
	}
}
