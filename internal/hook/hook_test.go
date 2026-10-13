package hook

import (
	"bytes"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/alexei-led/codex-router/internal/config"
	"github.com/alexei-led/codex-router/internal/control"
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
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Message(tt.snap, tt.in); got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

type handler struct{ snap route.Snapshot }

func (h handler) Apply(route.Command) error { return nil }
func (h handler) Snapshot() route.Snapshot  { return h.snap }

func serve(t *testing.T, snap route.Snapshot) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "hk")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	path := filepath.Join(dir, control.SocketName)
	s, err := control.Listen(path, handler{snap})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return path
}

const budget = Timeout + 50*time.Millisecond

func runReader(t *testing.T, stdin io.Reader, socket string) string {
	t.Helper()
	var out bytes.Buffer
	env := func(k string) string {
		if k == control.EnvSocket {
			return socket
		}
		return ""
	}
	start := time.Now()
	if err := Run(stdin, &out, env); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d > budget {
		t.Errorf("took %v, want at most %v", d, budget)
	}
	return strings.TrimSpace(out.String())
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
	dir, err := os.MkdirTemp("", "hk")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	path := filepath.Join(dir, "silent.sock")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	if got := run(t, stdinT2, path); got != "{}" {
		t.Errorf("got %s, want {}", got)
	}
}

func TestRunBoundedWhenStdinNeverCloses(t *testing.T) {
	last := dec("th", "t2", 3, "gpt-5.5", config.EffortHigh)
	sock := serve(t, route.Snapshot{Recent: []route.Decision{dec("th", "t1", 1, "gpt-5.1", config.EffortLow)}, Last: &last})
	pr, pw := io.Pipe()
	t.Cleanup(func() { _ = pw.Close() })

	if got := runReader(t, pr, sock); got != "{}" {
		t.Errorf("got %s, want {}", got)
	}
}
