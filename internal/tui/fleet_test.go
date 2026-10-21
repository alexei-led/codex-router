package tui

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/alexei-led/codex-router/internal/config"
	"github.com/alexei-led/codex-router/internal/control"
	"github.com/alexei-led/codex-router/internal/home"
	"github.com/alexei-led/codex-router/internal/registry"
	"github.com/alexei-led/codex-router/internal/route"
)

var (
	homeWork    = home.Home{ID: "aaaaaaaaaaaa", Path: "/u/.codex-work", Name: "work"}
	homeDefault = home.Home{ID: "bbbbbbbbbbbb", Path: "/u/.codex", Name: "default"}
)

type fakeConn struct {
	hello    control.Hello
	updates  chan route.Snapshot
	sessions chan []route.SessionInfo
	lost     sync.Once

	mu         sync.Mutex
	sent       []route.Command
	subscribed []string
	err        error
	closed     bool
}

func newFakeConn(h control.Hello) *fakeConn {
	return &fakeConn{hello: h, updates: make(chan route.Snapshot, 4), sessions: make(chan []route.SessionInfo, 4)}
}

func (f *fakeConn) Hello() control.Hello                 { return f.hello }
func (f *fakeConn) Snapshots() <-chan route.Snapshot     { return f.updates }
func (f *fakeConn) Sessions() <-chan []route.SessionInfo { return f.sessions }

func (f *fakeConn) Subscribe(s string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.subscribed = append(f.subscribed, s)
	return f.err
}

func (f *fakeConn) Send(c route.Command) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, c)
	return f.err
}

func (f *fakeConn) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = true
	return nil
}

func (f *fakeConn) lose() {
	f.lost.Do(func() {
		close(f.sessions)
		close(f.updates)
	})
}

func (f *fakeConn) setErr(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.err = err
}

func (f *fakeConn) sentCmds() []route.Command {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.sent)
}

func (f *fakeConn) subs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.subscribed)
}

func (f *fakeConn) isClosed() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.closed
}

type testFleet struct {
	*fleet
	t *testing.T

	mu      sync.Mutex
	entries []registry.Endpoint
	conns   map[string]*fakeConn
	all     []*fakeConn
	pruned  []string
	dialed  int
	clock   time.Time
}

func newTestFleet(t *testing.T) *testFleet {
	t.Helper()
	tf := &testFleet{t: t, conns: map[string]*fakeConn{}, clock: at(18, 0)}
	tf.fleet = newFleet(tf.list, tf.dial)
	tf.now = tf.clockNow
	tf.prune = func(ep registry.Endpoint) {
		tf.mu.Lock()
		defer tf.mu.Unlock()
		tf.pruned = append(tf.pruned, ep.ID)
	}
	t.Cleanup(func() {
		tf.mu.Lock()
		defer tf.mu.Unlock()
		for _, c := range tf.all {
			c.lose()
		}
	})
	return tf
}

func (tf *testFleet) list() ([]registry.Endpoint, error) {
	tf.mu.Lock()
	defer tf.mu.Unlock()
	return slices.Clone(tf.entries), nil
}

func (tf *testFleet) dial(ep registry.Endpoint) (conn, error) {
	tf.mu.Lock()
	defer tf.mu.Unlock()
	tf.dialed++
	c := tf.conns[ep.ID]
	if c == nil {
		return nil, errors.New("connection refused")
	}
	delete(tf.conns, ep.ID)
	return c, nil
}

func (tf *testFleet) clockNow() time.Time {
	tf.mu.Lock()
	defer tf.mu.Unlock()
	return tf.clock
}

func (tf *testFleet) advance(d time.Duration) {
	tf.mu.Lock()
	defer tf.mu.Unlock()
	tf.clock = tf.clock.Add(d)
}

func (tf *testFleet) add(id, kind string, h home.Home) *fakeConn {
	tf.mu.Lock()
	defer tf.mu.Unlock()
	if !slices.ContainsFunc(tf.entries, func(ep registry.Endpoint) bool { return ep.ID == id }) {
		tf.entries = append(tf.entries, registry.Endpoint{ID: id, Kind: kind, Home: h, Socket: "/run/" + id + ".sock"})
	}
	c := newFakeConn(control.Hello{EndpointID: id, Kind: kind, Home: h})
	tf.conns[id] = c
	tf.all = append(tf.all, c)
	return c
}

func (tf *testFleet) started(id string, at time.Time) {
	tf.mu.Lock()
	defer tf.mu.Unlock()
	for i := range tf.entries {
		if tf.entries[i].ID == id {
			tf.entries[i].Started = at
		}
	}
}

func (tf *testFleet) label(id, label string) {
	tf.mu.Lock()
	defer tf.mu.Unlock()
	tf.conns[id].hello.Label = label
}

func (tf *testFleet) remove(id string) {
	tf.mu.Lock()
	defer tf.mu.Unlock()
	tf.entries = slices.DeleteFunc(tf.entries, func(ep registry.Endpoint) bool { return ep.ID == id })
}

func (tf *testFleet) prunedIDs() []string {
	tf.mu.Lock()
	defer tf.mu.Unlock()
	return slices.Clone(tf.pruned)
}

func (tf *testFleet) settle(ok func(fleetState) bool) fleetState {
	tf.t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		st := tf.state()
		if ok(st) {
			return st
		}
		select {
		case <-tf.changed:
		case <-deadline:
			tf.t.Fatalf("fleet did not settle: %+v", st)
		}
	}
}

func (tf *testFleet) settleCount(n int) fleetState {
	tf.t.Helper()
	return tf.settle(func(st fleetState) bool { return len(flat(st)) == n })
}

func session(h home.Home, thread, cwd string, active time.Time) route.SessionInfo {
	return route.SessionInfo{
		ID: control.SessionID(h.ID, thread), Home: h.ID, ThreadID: thread, Cwd: cwd,
		Label: h.Name + " · " + filepath.Base(cwd), Mode: route.ModeOn, LastActive: active,
	}
}

func flat(st fleetState) []fleetSession {
	var out []fleetSession
	for _, h := range st.homes {
		out = append(out, h.sessions...)
	}
	return out
}

func ids(ss []fleetSession) []string {
	out := make([]string, len(ss))
	for i, s := range ss {
		out[i] = s.ID
	}
	return out
}

func TestFleetMergesAndGroupsByHome(t *testing.T) {
	tf := newTestFleet(t)
	e1 := tf.add("e1", registry.KindSession, homeWork)
	e2 := tf.add("e2", registry.KindBrain, homeWork)
	e3 := tf.add("e3", registry.KindSession, homeDefault)
	tf.poll()
	s1 := session(homeWork, "t1", "/r/app", at(18, 10))
	s2 := session(homeWork, "t2", "/q/app", at(18, 12))
	s3 := session(homeDefault, "t3", "/r/lib", at(18, 20))
	e1.sessions <- []route.SessionInfo{s1}
	e2.sessions <- []route.SessionInfo{s2}
	e3.sessions <- []route.SessionInfo{s3}
	st := tf.settleCount(3)

	if len(st.homes) != 2 || st.homes[0].home != homeDefault || st.homes[1].home != homeWork {
		t.Fatalf("homes = %+v", st.homes)
	}
	if got, want := ids(st.homes[1].sessions), []string{s2.ID, s1.ID}; !slices.Equal(got, want) {
		t.Errorf("work sessions = %v, want %v (most recent first)", got, want)
	}
	work := st.homes[1].sessions
	if work[0].Label == work[1].Label || work[0].Label == s2.Label || work[1].Label == s1.Label {
		t.Errorf("colliding labels not disambiguated: %q, %q", work[0].Label, work[1].Label)
	}
	if got := st.homes[0].sessions[0].Label; got != s3.Label {
		t.Errorf("unique label changed to %q", got)
	}
	if work[0].endpoint != "e2" || work[1].endpoint != "e1" {
		t.Errorf("endpoints = %s, %s", work[0].endpoint, work[1].endpoint)
	}
	want := []fleetEndpointInfo{{id: "e1", kind: registry.KindSession}, {id: "e2", kind: registry.KindBrain}}
	if !slices.Equal(st.homes[1].endpoints, want) {
		t.Errorf("work endpoints = %+v", st.homes[1].endpoints)
	}
}

func TestFleetInitialFocus(t *testing.T) {
	tests := []struct {
		name  string
		cwd   string
		focus string
	}{
		{"deepest containing cwd", "/r/a/sub", "t-ra"},
		{"equal cwd", "/x", "t-x"},
		{"sibling with a common prefix is no match", "/r/ab", "t-r"},
		{"no match takes the most recent", "/elsewhere", "t-x"},
		{"unknown panel cwd takes the most recent", "", "t-x"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tf := newTestFleet(t)
			tf.cwd = tt.cwd
			e := tf.add("e1", registry.KindSession, homeWork)
			tf.poll()
			e.sessions <- []route.SessionInfo{
				session(homeWork, "t-x", "/x", at(18, 30)),
				session(homeWork, "t-r", "/r", at(18, 20)),
				session(homeWork, "t-ra", "/r/a", at(18, 10)),
			}
			want := control.SessionID(homeWork.ID, tt.focus)
			tf.settle(func(st fleetState) bool { return st.focus == want && slices.Equal(e.subs(), []string{want}) })
		})
	}
}

func TestFleetFocusSurvivesReorder(t *testing.T) {
	tf := newTestFleet(t)
	tf.cwd = "/r/a"
	e1 := tf.add("e1", registry.KindSession, homeWork)
	tf.poll()

	recent := session(homeWork, "t-new", "/x", at(18, 30))
	other := session(homeWork, "t-old", "/y", at(18, 10))
	e1.sessions <- []route.SessionInfo{recent, other}
	tf.settle(func(st fleetState) bool { return st.focus == recent.ID })

	other.LastActive = at(18, 40)
	e1.sessions <- []route.SessionInfo{other, recent}
	tf.settle(func(st fleetState) bool { return flat(st)[0].ID == other.ID })
	if st := tf.state(); st.focus != recent.ID {
		t.Fatalf("reorder moved the focus to %s", st.focus)
	}

	e2 := tf.add("e2", registry.KindSession, homeWork)
	tf.poll()
	match := session(homeWork, "t-match", "/r", at(18, 5))
	e2.sessions <- []route.SessionInfo{match}
	tf.settle(func(st fleetState) bool { return st.focus == match.ID })

	other.LastActive = at(18, 50)
	e1.sessions <- []route.SessionInfo{other, recent}
	tf.settle(func(st fleetState) bool { return flat(st)[0].LastActive.Equal(at(18, 50)) })
	if st := tf.state(); st.focus != match.ID {
		t.Errorf("cwd focus moved to %s", st.focus)
	}
	tf.settle(func(fleetState) bool {
		return slices.Equal(e2.subs(), []string{match.ID}) && slices.Equal(e1.subs(), []string{recent.ID, ""})
	})
}

func TestFleetInitialFocusWaitsForEveryEndpoint(t *testing.T) {
	tf := newTestFleet(t)
	e1 := tf.add("e1", registry.KindSession, homeWork)
	e2 := tf.add("e2", registry.KindSession, homeDefault)
	tf.poll()
	older := session(homeWork, "t1", "/r", at(18, 10))
	newer := session(homeDefault, "t2", "/q", at(18, 30))
	e1.sessions <- []route.SessionInfo{older}
	tf.settle(func(st fleetState) bool { return st.focus == older.ID })
	e2.sessions <- []route.SessionInfo{newer}
	tf.settle(func(st fleetState) bool { return st.focus == newer.ID })

	older.LastActive = at(18, 40)
	e1.sessions <- []route.SessionInfo{older}
	tf.settle(func(st fleetState) bool { return flat(st)[1].LastActive.Equal(at(18, 40)) })
	if st := tf.state(); st.focus != newer.ID {
		t.Errorf("settled focus moved to %s", st.focus)
	}
}

func TestFleetLockedModeIgnoresOtherSessions(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		tf := newTestFleet(t)
		tf.cwd = "/r"
		locked := session(homeDefault, "t-locked", "/q", at(18, 1))
		tf.locked = locked.ID
		e1 := tf.add("e1", registry.KindSession, homeWork)
		e2 := tf.add("e2", registry.KindSession, homeDefault)
		tf.poll()

		e1.sessions <- []route.SessionInfo{session(homeWork, "t-other", "/r", at(18, 30))}
		e2.sessions <- []route.SessionInfo{locked}
		synctest.Wait()

		st := tf.state()
		if got := ids(flat(st)); !slices.Equal(got, []string{locked.ID}) || st.focus != locked.ID {
			t.Fatalf("listed %v, focus %s; want only %s", got, st.focus, locked.ID)
		}
		if got := e1.subs(); len(got) != 0 {
			t.Errorf("e1 subscribed %v, want none", got)
		}
		if got := e2.subs(); !slices.Equal(got, []string{locked.ID}) {
			t.Errorf("e2 subscribed %v, want [%s]", got, locked.ID)
		}

		e2.sessions <- nil
		synctest.Wait()
		st = tf.state()
		if len(flat(st)) != 0 || st.focus != locked.ID {
			t.Errorf("after the locked session left: %d listed, focus %q; want 0, %s", len(flat(st)), st.focus, locked.ID)
		}
	})
}

func TestFleetTakesSnapshotsOfTheFocusOnly(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		tf := newTestFleet(t)
		e := tf.add("e1", registry.KindSession, homeWork)
		tf.poll()
		s := session(homeWork, "t1", "/r", at(18, 1))
		e.sessions <- []route.SessionInfo{s}
		synctest.Wait()

		e.updates <- route.Snapshot{ThreadID: "t2", Seq: 1}
		synctest.Wait()
		if st := tf.state(); st.snap != nil {
			t.Fatalf("snapshot of another thread taken: %+v", st.snap)
		}
		e.updates <- route.Snapshot{ThreadID: "t1", Seq: 2}
		synctest.Wait()
		if st := tf.state(); st.snap == nil || st.snap.Seq != 2 {
			t.Errorf("snapshot = %+v, want the focused thread's, seq 2", st.snap)
		}
	})
}

func TestFleetEndpointLossAndReturn(t *testing.T) {
	tf := newTestFleet(t)
	e1 := tf.add("e1", registry.KindSession, homeWork)
	e2 := tf.add("e2", registry.KindSession, homeDefault)
	tf.poll()
	s1 := session(homeWork, "t1", "/r", at(18, 30))
	s2 := session(homeDefault, "t2", "/q", at(18, 10))
	e1.sessions <- []route.SessionInfo{s1}
	e2.sessions <- []route.SessionInfo{s2}
	tf.settle(func(st fleetState) bool { return len(flat(st)) == 2 && st.focus == s1.ID })

	e1.lose()
	st := tf.settle(func(st fleetState) bool { return st.disconnected })
	got := flat(st)
	if len(got) != 2 || !got[1].disconnected || got[0].disconnected {
		t.Fatalf("after loss: %+v", got)
	}
	if !e1.isClosed() {
		t.Error("lost connection not closed")
	}

	tf.advance(lostGrace - time.Second)
	tf.poll()
	if got := flat(tf.state()); len(got) != 2 {
		t.Fatalf("dropped before the grace period: %+v", got)
	}
	if got := tf.prunedIDs(); !slices.Equal(got, []string{"e1"}) {
		t.Errorf("failed redial pruned %v, want [e1]", got)
	}

	tf.remove("e1")
	tf.advance(time.Second)
	tf.poll()
	st = tf.state()
	if got := ids(flat(st)); !slices.Equal(got, []string{s2.ID}) {
		t.Fatalf("after the grace period: %v", got)
	}
	if st.focus != s2.ID || st.disconnected {
		t.Errorf("focus %q, disconnected %v; want %s", st.focus, st.disconnected, s2.ID)
	}

	back := tf.add("e1", registry.KindSession, homeWork)
	tf.poll()
	back.sessions <- []route.SessionInfo{s1}
	st = tf.settleCount(2)
	if flat(st)[1].disconnected {
		t.Error("returned endpoint still disconnected")
	}
}

func TestFleetRedialsALostEndpointWithinGrace(t *testing.T) {
	tf := newTestFleet(t)
	e := tf.add("e1", registry.KindSession, homeWork)
	tf.poll()
	s := session(homeWork, "t1", "/r", at(18, 1))
	e.sessions <- []route.SessionInfo{s}
	tf.settle(func(st fleetState) bool { return st.focus == s.ID && !st.disconnected })

	e.lose()
	tf.settle(func(st fleetState) bool { return st.disconnected })
	again := tf.add("e1", registry.KindSession, homeWork)
	tf.poll()
	st := tf.settle(func(st fleetState) bool { return !st.disconnected })
	if st.focus != s.ID {
		t.Errorf("focus %q", st.focus)
	}
	tf.settle(func(fleetState) bool { return slices.Equal(again.subs(), []string{s.ID}) })
}

func TestFleetRejectsAStaleEntry(t *testing.T) {
	tf := newTestFleet(t)
	c := tf.add("e1", registry.KindSession, homeWork)
	c.hello.EndpointID = "someone-else"
	tf.poll()
	if !c.isClosed() {
		t.Error("connection with a mismatched hello kept")
	}
	if st := tf.state(); !st.disconnected || len(st.homes) != 0 {
		t.Errorf("state = %+v", st)
	}
}

func TestFleetBacksOffIdentityMismatchesWithoutPruning(t *testing.T) {
	tf := newTestFleet(t)
	first := tf.add("e1", registry.KindSession, homeWork)
	first.hello.EndpointID = "wrong"
	tf.poll()
	if !first.isClosed() {
		t.Fatal("mismatched connection was not closed")
	}
	tf.poll()
	if tf.dialed != 1 {
		t.Fatalf("immediate retries = %d, want 1 dial", tf.dialed)
	}

	tf.advance(pollInterval)
	second := newFakeConn(control.Hello{EndpointID: "wrong-again", Kind: registry.KindSession, Home: homeWork})
	tf.mu.Lock()
	tf.conns["e1"] = second
	tf.all = append(tf.all, second)
	tf.mu.Unlock()
	tf.poll()
	if tf.dialed != 2 || !second.isClosed() {
		t.Fatalf("after first backoff, dials = %d, closed = %v; want 2 and true", tf.dialed, second.isClosed())
	}
	tf.poll()
	if tf.dialed != 2 {
		t.Errorf("repeated mismatch retried before its longer backoff: dials = %d", tf.dialed)
	}
	tf.advance(2*time.Second - time.Nanosecond)
	tf.poll()
	if tf.dialed != 2 {
		t.Errorf("retry occurred before the doubled backoff elapsed: dials = %d", tf.dialed)
	}
	tf.advance(time.Nanosecond)
	third := newFakeConn(control.Hello{EndpointID: "wrong-third", Kind: registry.KindSession, Home: homeWork})
	tf.mu.Lock()
	tf.conns["e1"] = third
	tf.all = append(tf.all, third)
	tf.mu.Unlock()
	tf.poll()
	if tf.dialed != 3 || !third.isClosed() {
		t.Errorf("retry after doubled backoff = dials %d, closed %v; want 3 and true", tf.dialed, third.isClosed())
	}
	if got := tf.prunedIDs(); len(got) != 0 {
		t.Errorf("mismatch pruned registry entries: %v", got)
	}
}

func TestFleetMetadataChangeResetsMismatchBackoffAndDeadEntriesStillPrune(t *testing.T) {
	tf := newTestFleet(t)
	first := tf.add("e1", registry.KindSession, homeWork)
	first.hello.EndpointID = "wrong"
	tf.poll()

	tf.mu.Lock()
	tf.entries[0].PID++
	tf.entries[0].Socket = "/run/e1-restarted.sock"
	changed := newFakeConn(control.Hello{EndpointID: "still-wrong", Kind: registry.KindSession, Home: homeWork})
	tf.conns["e1"] = changed
	tf.all = append(tf.all, changed)
	tf.entries = append(tf.entries, registry.Endpoint{ID: "dead", Kind: registry.KindSession, PID: -1, Home: homeDefault, Socket: "/run/dead.sock"})
	tf.mu.Unlock()
	tf.poll()
	if tf.dialed != 3 || !changed.isClosed() {
		t.Fatalf("changed identity did not retry immediately: dials = %d, closed = %v", tf.dialed, changed.isClosed())
	}
	if got := tf.prunedIDs(); !slices.Equal(got, []string{"dead"}) {
		t.Errorf("dead unchanged entry prune calls = %v, want [dead]", got)
	}
}

func TestFleetHomeCommandsReachEveryEndpointOfTheFocusedHome(t *testing.T) {
	tf := newTestFleet(t)
	w1 := tf.add("w1", registry.KindSession, homeWork)
	w2 := tf.add("w2", registry.KindBrain, homeWork)
	d1 := tf.add("d1", registry.KindSession, homeDefault)
	tf.poll()
	focus := session(homeWork, "t1", "/r", at(18, 30))
	w1.sessions <- []route.SessionInfo{focus}
	w2.sessions <- []route.SessionInfo{session(homeWork, "t2", "/q", at(18, 10))}
	d1.sessions <- []route.SessionInfo{session(homeDefault, "t3", "/z", at(18, 20))}
	tf.settle(func(st fleetState) bool { return len(flat(st)) == 3 && slices.Equal(w1.subs(), []string{focus.ID}) })
	w1.updates <- route.Snapshot{ThreadID: "t1", Seq: 1, RoutingMode: config.ModeBalanced}
	tf.settle(func(st fleetState) bool { return st.snap != nil })

	m := newModel(tf.fleet)
	m.Update(fleetMsg{})
	pressAll(m, "P")
	m.tab = TabPolicy
	pressAll(m, "s", "3")

	home := route.Command{Op: route.OpPause, Scope: route.ScopeHome}
	mode := route.Command{Op: route.OpMode, Session: focus.ID, Mode: config.ModeQuality}
	for c, want := range map[*fakeConn][]route.Command{w1: {home, mode}, w2: {home}} {
		if got := c.sentCmds(); !slices.Equal(got, want) {
			t.Errorf("%s got %+v, want %+v", c.hello.EndpointID, got, want)
		}
	}
	if got := d1.sentCmds(); len(got) != 0 {
		t.Errorf("other home got %+v", got)
	}
}

func TestFleetHomeCommandReportsFailures(t *testing.T) {
	tf := newTestFleet(t)
	w1 := tf.add("w1", registry.KindSession, homeWork)
	w2 := tf.add("w2", registry.KindSession, homeWork)
	tf.poll()
	w1.sessions <- []route.SessionInfo{session(homeWork, "t1", "/r", at(18, 1))}
	tf.settleCount(1)
	w2.setErr(errors.New("unknown op"))

	err := tf.sendHome(homeWork.ID, route.Command{Op: route.OpPause})
	if err == nil || len(w1.sentCmds()) != 1 {
		t.Errorf("err %v, w1 sent %v", err, w1.sentCmds())
	}
	if err := tf.sendHome(homeDefault.ID, route.Command{Op: route.OpPause}); !errors.Is(err, errNotConnected) {
		t.Errorf("home without endpoints: %v", err)
	}
}

func TestFleetHeartbeatGoesToTheFocusedEndpoint(t *testing.T) {
	tf := newTestFleet(t)
	e1 := tf.add("e1", registry.KindSession, homeWork)
	e2 := tf.add("e2", registry.KindSession, homeDefault)
	tf.poll()
	s := session(homeWork, "t1", "/r", at(18, 30))
	e1.sessions <- []route.SessionInfo{s}
	e2.sessions <- []route.SessionInfo{session(homeDefault, "t2", "/q", at(18, 10))}
	tf.settle(func(st fleetState) bool { return len(flat(st)) == 2 && st.focus == s.ID })

	tf.heartbeat()
	if got := e1.sentCmds(); len(got) != 1 || got[0].Op != route.OpSnapshot {
		t.Errorf("focused endpoint got %+v", got)
	}
	if got := e2.sentCmds(); len(got) != 0 {
		t.Errorf("other endpoint got %+v", got)
	}
}

type endpointHandler struct {
	hello    control.Hello
	sessions []route.SessionInfo
}

func (h endpointHandler) Apply(route.Command) error         { return nil }
func (h endpointHandler) Sessions() []route.SessionInfo     { return h.sessions }
func (h endpointHandler) SnapshotFor(string) route.Snapshot { return route.Snapshot{} }
func (h endpointHandler) Hello() control.Hello              { return h.hello }

func TestFleetOverTheRegistry(t *testing.T) {
	env := map[string]string{"XDG_STATE_HOME": t.TempDir()}
	root, err := registry.Root(func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	sockDir, err := os.MkdirTemp("", "crf")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(sockDir) })
	serve := func(id string, h home.Home, s route.SessionInfo) {
		t.Helper()
		sock := filepath.Join(sockDir, id+".sock")
		srv, err := control.Listen(sock, endpointHandler{
			hello:    control.Hello{EndpointID: id, Kind: registry.KindSession, Home: h},
			sessions: []route.SessionInfo{s},
		})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = srv.Close() })
		if err := registry.Write(root, registry.Endpoint{ID: id, Kind: registry.KindSession, Home: h, Socket: sock}); err != nil {
			t.Fatal(err)
		}
	}
	serve("w1", homeWork, session(homeWork, "t1", "/r", at(18, 1)))
	serve("d1", homeDefault, session(homeDefault, "t2", "/q", at(18, 2)))
	dead := registry.Endpoint{ID: "dead", Kind: registry.KindSession, Home: homeWork, Socket: filepath.Join(sockDir, "dead.sock")}
	if err := registry.Write(root, dead); err != nil {
		t.Fatal(err)
	}

	f := newFleet(registrySource(root), dialControl)
	f.prune = registryPrune(root)
	tf := &testFleet{fleet: f, t: t}
	t.Cleanup(f.closeAll)
	f.poll()
	st := tf.settleCount(2)
	if len(st.homes) != 2 {
		t.Errorf("homes = %+v", st.homes)
	}
	eps, _, err := registry.List(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, ep := range eps {
		if ep.ID == dead.ID {
			t.Error("dead entry not pruned")
		}
	}
}

func TestSocketSourceSkipsTheHelloCheck(t *testing.T) {
	tf := newTestFleet(t)
	tf.fleet.list = socketSource("/run/x.sock")
	c := newFakeConn(control.Hello{EndpointID: "ep-1", Kind: registry.KindSession, Home: homeWork})
	tf.conns["/run/x.sock"] = c
	tf.all = append(tf.all, c)
	tf.poll()
	c.sessions <- []route.SessionInfo{session(homeWork, "t1", "/r", at(18, 1))}
	tf.settleCount(1)
}

func TestFleetFocusOn(t *testing.T) {
	tf := newTestFleet(t)
	tf.cwd = "/r"
	e := tf.add("e1", registry.KindSession, homeWork)
	tf.poll()
	match := session(homeWork, "t1", "/r", at(18, 1))
	other := session(homeWork, "t2", "/q", at(18, 30))
	e.sessions <- []route.SessionInfo{other, match}
	tf.settle(func(st fleetState) bool { return st.focus == match.ID })

	tf.focusOn("h9/unknown")
	if st := tf.state(); st.focus != match.ID {
		t.Fatalf("unlisted session took the focus: %s", st.focus)
	}
	tf.focusOn(other.ID)
	tf.settle(func(fleetState) bool { return slices.Equal(e.subs(), []string{match.ID, other.ID}) })

	match.LastActive = at(18, 40)
	e.sessions <- []route.SessionInfo{match, other}
	tf.settle(func(st fleetState) bool { return flat(st)[0].ID == match.ID })
	if st := tf.state(); st.focus != other.ID {
		t.Errorf("a cwd match replaced the chosen focus: %s", st.focus)
	}
}

func TestFleetFocusOnKeepsTheLockedFocus(t *testing.T) {
	tf := newTestFleet(t)
	locked := session(homeWork, "t1", "/r", at(18, 1))
	tf.locked = locked.ID
	e := tf.add("e1", registry.KindSession, homeWork)
	tf.poll()
	e.sessions <- []route.SessionInfo{locked, session(homeWork, "t2", "/q", at(18, 30))}
	tf.settleCount(1)
	tf.focusOn(control.SessionID(homeWork.ID, "t2"))
	if st := tf.state(); st.focus != locked.ID {
		t.Errorf("focus = %s", st.focus)
	}
}

func TestFleetLockedSessionEnds(t *testing.T) {
	tests := []struct {
		name string
		end  func(tf *testFleet, e *fakeConn)
	}{
		{"endpoint exits and leaves the registry", func(tf *testFleet, e *fakeConn) {
			e.lose()
			tf.settle(func(st fleetState) bool { return st.disconnected })
			tf.remove("e1")
			tf.poll()
		}},
		{"endpoint stops listing it", func(_ *testFleet, e *fakeConn) { e.sessions <- nil }},
		{"lost endpoint past the grace period", func(tf *testFleet, e *fakeConn) {
			e.lose()
			tf.settle(func(st fleetState) bool { return st.disconnected })
			tf.advance(lostGrace)
			tf.poll()
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tf := newTestFleet(t)
			locked := session(homeWork, "t1", "/r", at(18, 1))
			tf.locked = locked.ID
			e := tf.add("e1", registry.KindSession, homeWork)
			tf.poll()
			if tf.state().ended {
				t.Fatal("ended before the session was listed")
			}
			e.sessions <- []route.SessionInfo{locked}
			tf.settleCount(1)
			tt.end(tf, e)
			tf.settle(func(st fleetState) bool { return st.ended })
		})
	}
}

func TestFleetLostButRegisteredSessionHasNotEnded(t *testing.T) {
	tf := newTestFleet(t)
	locked := session(homeWork, "t1", "/r", at(18, 1))
	tf.locked = locked.ID
	e := tf.add("e1", registry.KindSession, homeWork)
	tf.poll()
	e.sessions <- []route.SessionInfo{locked}
	tf.settleCount(1)
	e.lose()
	tf.settle(func(st fleetState) bool { return st.disconnected })
	tf.poll()
	if st := tf.state(); st.ended {
		t.Error("a lost endpoint still in the registry ended the session")
	}
}

func TestFleetAllModeNeverEnds(t *testing.T) {
	tf := newTestFleet(t)
	e := tf.add("e1", registry.KindSession, homeWork)
	tf.poll()
	e.sessions <- []route.SessionInfo{session(homeWork, "t1", "/r", at(18, 1))}
	tf.settleCount(1)
	e.sessions <- nil
	if st := tf.settleCount(0); st.ended {
		t.Error("all mode ended")
	}
}

func TestFleetBrainStaysWhileDown(t *testing.T) {
	tf := newTestFleet(t)
	brain := tf.add("b1", registry.KindBrain, homeWork)
	tf.started("b1", at(18, 0).Add(-2*time.Hour))
	launcher := tf.add("e1", registry.KindSession, homeWork)
	tf.poll()
	sb := session(homeWork, "t-brain", "/r", at(18, 1))
	sl := session(homeWork, "t-launcher", "/q", at(17, 50))
	brain.sessions <- []route.SessionInfo{sb}
	launcher.sessions <- []route.SessionInfo{sl}
	st := tf.settleCount(2)
	want := []fleetEndpointInfo{{id: "b1", kind: registry.KindBrain, up: 2 * time.Hour}, {id: "e1", kind: registry.KindSession}}
	if len(st.homes) != 1 || !slices.Equal(st.homes[0].endpoints, want) {
		t.Fatalf("homes = %+v, want one group with endpoints %+v", st.homes, want)
	}

	brain.lose()
	tf.settle(func(st fleetState) bool { return len(flat(st)) == 2 && flat(st)[0].disconnected })
	tf.remove("b1")
	tf.advance(lostGrace)
	tf.poll()
	st = tf.state()
	if got := ids(flat(st)); !slices.Equal(got, []string{sl.ID}) {
		t.Fatalf("sessions past the grace period: %v, want only the launcher's", got)
	}
	want = []fleetEndpointInfo{{id: "b1", kind: registry.KindBrain, lost: true}, {id: "e1", kind: registry.KindSession}}
	if !slices.Equal(st.homes[0].endpoints, want) {
		t.Errorf("endpoints = %+v, want the brain kept as lost", st.homes[0].endpoints)
	}

	back := tf.add("b1", registry.KindBrain, homeWork)
	tf.started("b1", at(18, 0))
	tf.poll()
	back.sessions <- []route.SessionInfo{sb}
	st = tf.settleCount(2)
	if b := st.homes[0].endpoints[0]; b.lost || b.up != lostGrace {
		t.Errorf("returned brain = %+v, want up %v", b, lostGrace)
	}
}

func TestFleetLockedBrainSessionNeverEnds(t *testing.T) {
	tests := []struct {
		name string
		end  func(tf *testFleet, e *fakeConn)
	}{
		{"brain stops listing it", func(tf *testFleet, e *fakeConn) {
			e.sessions <- nil
			tf.settleCount(0)
		}},
		{"brain exits and leaves the registry", func(tf *testFleet, e *fakeConn) {
			e.lose()
			tf.settle(func(st fleetState) bool { return st.disconnected })
			tf.remove("b1")
			tf.poll()
		}},
		{"lost brain past the grace period", func(tf *testFleet, e *fakeConn) {
			e.lose()
			tf.settle(func(st fleetState) bool { return st.disconnected })
			tf.remove("b1")
			tf.advance(lostGrace)
			tf.poll()
			tf.settleCount(0)
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tf := newTestFleet(t)
			locked := session(homeWork, "t1", "/r", at(18, 1))
			tf.locked = locked.ID
			e := tf.add("b1", registry.KindBrain, homeWork)
			tf.poll()
			e.sessions <- []route.SessionInfo{locked}
			tf.settleCount(1)
			tt.end(tf, e)
			if st := tf.state(); st.ended {
				t.Error("a brain session ended")
			}
		})
	}
}

func TestRegistryPruneKeepsAnEntryRewrittenSinceTheList(t *testing.T) {
	root, err := registry.Root(func(k string) string { return map[string]string{"XDG_STATE_HOME": t.TempDir()}[k] })
	if err != nil {
		t.Fatal(err)
	}
	listed := registry.Endpoint{ID: "brain-x", Kind: registry.KindBrain, Home: homeWork, Socket: "/nonexistent/control.sock", Started: at(18, 1)}
	restarted := listed
	restarted.Started = at(18, 2)
	if err := registry.Write(root, restarted); err != nil {
		t.Fatal(err)
	}

	registryPrune(root)(listed)

	eps, _, err := registry.List(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(eps) != 1 || !eps[0].Started.Equal(restarted.Started) {
		t.Fatalf("entries = %+v, want the restarted endpoint's kept", eps)
	}
}
