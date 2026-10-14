package hook

import (
	"bytes"
	"io"
	"maps"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/alexei-led/codex-router/internal/config"
	"github.com/alexei-led/codex-router/internal/control"
	"github.com/alexei-led/codex-router/internal/home"
	"github.com/alexei-led/codex-router/internal/registry"
	"github.com/alexei-led/codex-router/internal/route"
)

var t0 = time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)

func dec(thread, turn string, sec int, model string, effort config.Effort) route.Decision {
	return route.Decision{
		ThreadID: thread, TurnID: turn, At: t0.Add(time.Duration(sec) * time.Second),
		Tier: config.TierMicro, Activity: config.ActivityCode, Why: "simple edit",
		Route: config.Route{Model: model, Effort: effort},
	}
}

func TestMessage(t *testing.T) {
	a := dec("th", "t1", 1, "gpt-5.1", config.EffortLow)
	a2 := dec("th", "t1", 2, "gpt-5.1", config.EffortLow)
	same := dec("th", "t2", 3, "gpt-5.1", config.EffortLow)
	diff := dec("th", "t2", 3, "gpt-5.5", config.EffortHigh)
	other := dec("other", "t9", 4, "gpt-5.5", config.EffortHigh)
	want := "router: micro · code → gpt-5.5 · high · simple edit"
	pinned := dec("th", "t2", 3, "gpt-5.5", config.EffortHigh)
	pinned.Tier, pinned.Activity, pinned.Why = config.TierHigh, "", "pinned to high for this turn"
	noEffort := dec("th", "t2", 3, "gpt-5.5", "")

	tests := []struct {
		name string
		snap route.Snapshot
		in   Input
		want string
	}{
		{"switch", route.Snapshot{Recent: []route.Decision{a, a2}, Last: &diff}, Input{SessionID: "th", TurnID: "t2"}, want},
		{"switch with shuffled recent", route.Snapshot{Recent: []route.Decision{diff, a2, a}}, Input{SessionID: "th", TurnID: "t2"}, want},
		{"same route", route.Snapshot{Recent: []route.Decision{a}, Last: &same}, Input{SessionID: "th", TurnID: "t2"}, ""},
		{"first turn", route.Snapshot{Last: &a}, Input{SessionID: "th", TurnID: "t1"}, ""},
		{"turn not routed yet", route.Snapshot{Recent: []route.Decision{a}, Last: &diff}, Input{SessionID: "th", TurnID: "t3"}, ""},
		{"no turn id uses latest", route.Snapshot{Recent: []route.Decision{a}, Last: &diff}, Input{SessionID: "th"}, want},
		{"other thread ignored", route.Snapshot{Recent: []route.Decision{a}, Last: &other}, Input{SessionID: "th"}, ""},
		{"empty snapshot", route.Snapshot{}, Input{SessionID: "th", TurnID: "t1"}, ""},
		{"pinned turn without activity", route.Snapshot{Recent: []route.Decision{a}, Last: &pinned}, Input{SessionID: "th"},
			"router: high → gpt-5.5 · high · pinned to high for this turn"},
		{"route keeps Codex's effort", route.Snapshot{Recent: []route.Decision{a}, Last: &noEffort}, Input{SessionID: "th"},
			"router: micro · code → gpt-5.5 · simple edit"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Message(tt.snap, tt.in); got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

// handler is one endpoint: it serves the snapshots in snaps, a zero snapshot
// for any other thread, and lists the threads in list (nil: those in snaps).
type handler struct {
	hello control.Hello
	snaps map[string]route.Snapshot
	list  []string
}

func (h handler) Apply(route.Command) error { return nil }
func (h handler) Hello() control.Hello      { return h.hello }

func (h handler) Sessions() []route.SessionInfo {
	list := h.list
	if list == nil {
		list = slices.Sorted(maps.Keys(h.snaps))
	}
	var out []route.SessionInfo
	for _, th := range list {
		out = append(out, route.SessionInfo{ID: control.SessionID(h.hello.Home.ID, th), Home: h.hello.Home.ID, ThreadID: th})
	}
	return out
}

func (h handler) SnapshotFor(thread string) route.Snapshot { return h.snaps[thread] }

// socketDir is short: t.TempDir paths can exceed the unix socket limit.
func socketDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "hk")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func serveHandler(t *testing.T, h handler) string {
	t.Helper()
	path := filepath.Join(socketDir(t), control.SocketName)
	s, err := control.Listen(path, h)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return path
}

// serve serves snap as thread "th"'s snapshot only.
func serve(t *testing.T, snap route.Snapshot) string {
	t.Helper()
	return serveHandler(t, handler{
		hello: control.Hello{Home: home.Home{ID: "h1"}},
		snaps: map[string]route.Snapshot{"th": snap},
	})
}

// silentSocket accepts connections and never answers.
func silentSocket(t *testing.T) string {
	t.Helper()
	path := filepath.Join(socketDir(t), "silent.sock")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	return path
}

// ceiling is the longest a bounded run may take: well past Timeout, so a
// loaded host does not fail it, far short of a hang.
const ceiling = 4 * Timeout

// boundedRun fails the test when the call to f takes longer than ceiling.
func boundedRun(t *testing.T, f func()) {
	t.Helper()
	start := time.Now()
	f()
	if d := time.Since(start); d > ceiling {
		t.Errorf("took %v, want at most %v", d, ceiling)
	}
}

func runEnv(t *testing.T, stdin io.Reader, env map[string]string) string {
	t.Helper()
	var out bytes.Buffer
	if err := Run(stdin, &out, func(k string) string { return env[k] }); err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(out.String())
}

func runReader(t *testing.T, stdin io.Reader, socket string) string {
	t.Helper()
	return runEnv(t, stdin, map[string]string{control.EnvSocket: socket})
}

func run(t *testing.T, stdin, socket string) string {
	t.Helper()
	return runReader(t, strings.NewReader(stdin), socket)
}

const stdinT2 = `{"session_id":"th","turn_id":"t2","hook_event_name":"Stop","extra":1}`

func TestRunOverSocket(t *testing.T) {
	last := dec("th", "t2", 3, "gpt-5.5", config.EffortHigh)
	sock := serve(t, route.Snapshot{Recent: []route.Decision{dec("th", "t1", 1, "gpt-5.1", config.EffortLow)}, Last: &last})

	got := run(t, stdinT2, sock)
	want := `{"systemMessage":"router: micro · code → gpt-5.5 · high · simple edit"}`
	if got != want {
		t.Errorf("got %s, want %s", got, want)
	}
	if got := run(t, `{"session_id":"th","turn_id":"t1"}`, sock); got != "{}" {
		t.Errorf("non-switch: got %s, want {}", got)
	}
}

func TestRunFailsOpen(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "nope.sock")
	tests := []struct{ name, stdin, socket string }{
		{"no socket env", stdinT2, ""},
		{"unreachable socket", stdinT2, missing},
		{"garbage stdin", "not json", missing},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := run(t, tt.stdin, tt.socket); got != "{}" {
				t.Errorf("got %s, want {}", got)
			}
		})
	}
}

func TestRunBoundedWhenServerSilent(t *testing.T) {
	boundedRun(t, func() {
		if got := run(t, stdinT2, silentSocket(t)); got != "{}" {
			t.Errorf("got %s, want {}", got)
		}
	})
}

func TestRunBoundedWhenStdinNeverCloses(t *testing.T) {
	last := dec("th", "t2", 3, "gpt-5.5", config.EffortHigh)
	sock := serve(t, route.Snapshot{Recent: []route.Decision{dec("th", "t1", 1, "gpt-5.1", config.EffortLow)}, Last: &last})
	pr, pw := io.Pipe()
	t.Cleanup(func() { _ = pw.Close() })

	boundedRun(t, func() {
		if got := runReader(t, pr, sock); got != "{}" {
			t.Errorf("got %s, want {}", got)
		}
	})
}

// switchTo is thread's snapshot whose turn t2 switched to model at high.
func switchTo(thread, model string) route.Snapshot {
	last := dec(thread, "t2", 3, model, config.EffortHigh)
	return route.Snapshot{ThreadID: thread, Recent: []route.Decision{dec(thread, "t1", 1, "gpt-5.1", config.EffortLow)}, Last: &last}
}

func switchMessage(model string) string {
	return `{"systemMessage":"router: micro · code → ` + model + ` · high · simple edit"}`
}

func TestRunReportsOwnThreadOnly(t *testing.T) {
	same := dec("th2", "t2", 3, "gpt-5.1", config.EffortLow)
	sock := serveHandler(t, handler{
		hello: control.Hello{Home: home.Home{ID: "h1"}},
		snaps: map[string]route.Snapshot{
			"th1": switchTo("th1", "gpt-5.5"),
			"th2": {ThreadID: "th2", Recent: []route.Decision{dec("th2", "t1", 1, "gpt-5.1", config.EffortLow)}, Last: &same},
		},
	})

	if got, want := run(t, `{"session_id":"th1","turn_id":"t2"}`, sock), switchMessage("gpt-5.5"); got != want {
		t.Errorf("th1: got %s, want %s", got, want)
	}
	if got := run(t, `{"session_id":"th2","turn_id":"t2"}`, sock); got != "{}" {
		t.Errorf("th2: got %s, want {}", got)
	}
}

// fleet is a registry under a temp XDG_STATE_HOME and two Codex homes.
type fleet struct {
	state, root  string
	homeA, homeB home.Home
}

func newFleet(t *testing.T) fleet {
	t.Helper()
	f := fleet{state: t.TempDir()}
	var err error
	if f.root, err = registry.Root(func(k string) string { return f.env(home.Home{})[k] }); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	f.homeA, f.homeB = resolveHome(t, filepath.Join(dir, ".codex")), resolveHome(t, filepath.Join(dir, ".codex-work"))
	return f
}

func resolveHome(t *testing.T, path string) home.Home {
	t.Helper()
	h, err := home.Resolve(func(k string) string { return map[string]string{"CODEX_HOME": path}[k] })
	if err != nil {
		t.Fatal(err)
	}
	return h
}

// env is the hook's environment in a plain codex of h: no control socket.
func (f fleet) env(h home.Home) map[string]string {
	return map[string]string{"XDG_STATE_HOME": f.state, "CODEX_HOME": h.Path}
}

// register serves snaps as endpoint id of h, listing only the threads in
// list when it is not nil, and writes its registry entry.
func (f fleet) register(t *testing.T, id string, h home.Home, snaps map[string]route.Snapshot, list ...string) {
	t.Helper()
	sock := serveHandler(t, handler{hello: control.Hello{EndpointID: id, Kind: registry.KindBrain, Home: h}, snaps: snaps, list: list})
	f.entry(t, registry.Endpoint{ID: id, Home: h, Socket: sock})
}

func (f fleet) entry(t *testing.T, ep registry.Endpoint) {
	t.Helper()
	ep.Kind, ep.Protocol, ep.PID, ep.Started = registry.KindBrain, control.Protocol, os.Getpid(), time.Now()
	if err := registry.Write(f.root, ep); err != nil {
		t.Fatal(err)
	}
}

func (f fleet) run(t *testing.T, h home.Home, stdin string) string {
	t.Helper()
	return runEnv(t, strings.NewReader(stdin), f.env(h))
}

func TestRunFindsEndpointInRegistry(t *testing.T) {
	f := newFleet(t)
	f.register(t, "e-other", f.homeA, map[string]route.Snapshot{"th": switchTo("th", "gpt-5.4")}, "x")
	f.register(t, "e-mine", f.homeA, map[string]route.Snapshot{"th": switchTo("th", "gpt-5.5")})

	if got, want := f.run(t, f.homeA, stdinT2), switchMessage("gpt-5.5"); got != want {
		t.Errorf("got %s, want %s", got, want)
	}
}

func TestRunPicksOwnHome(t *testing.T) {
	f := newFleet(t)
	f.register(t, "e-a", f.homeA, map[string]route.Snapshot{"th": switchTo("th", "gpt-5.5")})
	f.register(t, "e-b", f.homeB, map[string]route.Snapshot{"th": switchTo("th", "gpt-5.4")})

	tests := []struct {
		name  string
		home  home.Home
		model string
	}{
		{"default home", f.homeA, "gpt-5.5"},
		{"work home", f.homeB, "gpt-5.4"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got, want := f.run(t, tt.home, stdinT2), switchMessage(tt.model); got != want {
				t.Errorf("got %s, want %s", got, want)
			}
		})
	}
}

func TestRunRegistryFailsOpen(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, f fleet)
	}{
		{"empty registry", func(*testing.T, fleet) {}},
		{"only another home", func(t *testing.T, f fleet) {
			f.register(t, "e-b", f.homeB, map[string]route.Snapshot{"th": switchTo("th", "gpt-5.5")})
		}},
		{"thread not listed", func(t *testing.T, f fleet) {
			f.register(t, "e-a", f.homeA, map[string]route.Snapshot{"th": switchTo("th", "gpt-5.5")}, "x")
		}},
		{"dead socket", func(t *testing.T, f fleet) {
			f.entry(t, registry.Endpoint{ID: "e-a", Home: f.homeA, Socket: filepath.Join(socketDir(t), "gone.sock")})
		}},
		{"stale entry", func(t *testing.T, f fleet) {
			sock := serveHandler(t, handler{
				hello: control.Hello{EndpointID: "e-new", Home: f.homeA},
				snaps: map[string]route.Snapshot{"th": switchTo("th", "gpt-5.5")},
			})
			f.entry(t, registry.Endpoint{ID: "e-old", Home: f.homeA, Socket: sock})
		}},
		{"silent endpoint", func(t *testing.T, f fleet) {
			f.entry(t, registry.Endpoint{ID: "e-a", Home: f.homeA, Socket: silentSocket(t)})
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFleet(t)
			tt.setup(t, f)
			if got := f.run(t, f.homeA, stdinT2); got != "{}" {
				t.Errorf("got %s, want {}", got)
			}
		})
	}
}

func TestRunSlowEndpointDoesNotHideOwn(t *testing.T) {
	f := newFleet(t)
	f.entry(t, registry.Endpoint{ID: "e-slow", Home: f.homeA, Socket: silentSocket(t)})
	f.register(t, "e-mine", f.homeA, map[string]route.Snapshot{"th": switchTo("th", "gpt-5.5")})

	if got, want := f.run(t, f.homeA, stdinT2), switchMessage("gpt-5.5"); got != want {
		t.Errorf("got %s, want %s", got, want)
	}
}

func TestRunEnvSocketBypassesRegistry(t *testing.T) {
	f := newFleet(t)
	f.register(t, "e-a", f.homeA, map[string]route.Snapshot{"th": switchTo("th", "gpt-5.4")})
	sock := serve(t, switchTo("th", "gpt-5.5"))

	env := f.env(f.homeA)
	env[control.EnvSocket] = sock
	got := runEnv(t, strings.NewReader(stdinT2), env)
	if want := switchMessage("gpt-5.5"); got != want {
		t.Errorf("got %s, want %s", got, want)
	}
}
