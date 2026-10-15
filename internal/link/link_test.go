package link

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/alexei-led/codex-router/internal/catalog"
	"github.com/alexei-led/codex-router/internal/classifier"
	"github.com/alexei-led/codex-router/internal/config"
	"github.com/alexei-led/codex-router/internal/proxy"
	"github.com/alexei-led/codex-router/internal/responses"
	"github.com/alexei-led/codex-router/internal/route"
)

var _ Handler = (*route.Engine)(nil)

const (
	homeA = "3fa9c0d1e2b4"
	homeB = "0b1c2d3e4f5a"
)

var (
	stepMeta  = responses.Meta{SessionID: "s-1", ThreadID: "th-1", TurnID: "tu-1", RequestKind: responses.RequestKindTurn}
	toLow     = responses.Rewrite{Model: "gpt-6-luna", Effort: config.EffortLow}
	smallBody = []byte(`{"model":"gpt-6.1-sol","input":[]}`)
)

type decideCall struct {
	meta        responses.Meta
	body        []byte
	hasDeadline bool
}

type observeCall struct {
	meta   responses.Meta
	served string
	usage  responses.Usage
	status int
}

type fakeBrain struct {
	rw      responses.Rewrite
	ok      bool
	slow    map[string]bool
	expired chan struct{}
	hold    chan struct{}

	mu       sync.Mutex
	decides  []decideCall
	observes []observeCall
}

func (b *fakeBrain) Decide(ctx context.Context, req *responses.Request, m responses.Meta) (responses.Rewrite, bool) {
	_, hasDeadline := ctx.Deadline()
	b.mu.Lock()
	b.decides = append(b.decides, decideCall{meta: m, body: req.Body(), hasDeadline: hasDeadline})
	b.mu.Unlock()
	if b.slow[m.TurnID] {
		<-ctx.Done()
		select {
		case b.expired <- struct{}{}:
		default:
		}
	}
	return b.rw, b.ok
}

func (b *fakeBrain) Observe(m responses.Meta, served string, u responses.Usage, status int) {
	if b.hold != nil {
		<-b.hold
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.observes = append(b.observes, observeCall{m, served, u, status})
}

func (b *fakeBrain) calls() []decideCall {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]decideCall(nil), b.decides...)
}

func (b *fakeBrain) observed() []observeCall {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]observeCall(nil), b.observes...)
}

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func turn(id string) responses.Meta {
	m := stepMeta
	m.TurnID = id
	return m
}

func slowTurns(ids ...string) map[string]bool {
	slow := map[string]bool{}
	for _, id := range ids {
		slow[id] = true
	}
	return slow
}

func sockPath(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "lk")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return filepath.Join(dir, SocketName)
}

func startBrain(t *testing.T, path, home string, h Handler) *Server {
	t.Helper()
	s, err := Listen(path, home, h, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func newClient(t *testing.T, path, home string, timeout time.Duration) *Client {
	t.Helper()
	c := NewClient(ClientOptions{Path: func() string { return path }, HomeID: home, Timeout: timeout})
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func parse(t *testing.T, body []byte) *responses.Request {
	t.Helper()
	req, err := responses.Parse(body)
	if err != nil {
		t.Fatal(err)
	}
	return req
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestFrameRoundTrip(t *testing.T) {
	cases := []struct {
		name string
		h    Header
		body []byte
	}{
		{"hello", Header{Type: TypeHello, HomeID: homeA}, nil},
		{"decide", Header{Type: TypeDecide, ID: 7, Meta: &stepMeta, DeadlineUnixMs: 1_790_000_001_700}, smallBody},
		{"rewrite", Header{Type: TypeRewrite, ID: 7, Rewrite: &toLow}, nil},
		{"body with newlines", Header{Type: TypeDecide, ID: 1}, []byte("{\n\"model\":\"m\"\n}\n")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			if err := WriteFrame(&buf, tc.h, tc.body); err != nil {
				t.Fatal(err)
			}
			h, body, err := ReadFrame(&buf)
			if err != nil {
				t.Fatal(err)
			}
			if h.Type != tc.h.Type || h.ID != tc.h.ID || h.HomeID != tc.h.HomeID || h.DeadlineUnixMs != tc.h.DeadlineUnixMs {
				t.Fatalf("header = %+v, want %+v", h, tc.h)
			}
			if !bytes.Equal(body, tc.body) {
				t.Fatalf("body = %q, want %q", body, tc.body)
			}
			if buf.Len() != 0 {
				t.Fatalf("%d bytes left after the frame", buf.Len())
			}
		})
	}
}

func frame(n uint32, payload string) []byte {
	b := binary.BigEndian.AppendUint32(nil, n)
	return append(b, payload...)
}

func TestReadFrameRejects(t *testing.T) {
	cases := []struct {
		name string
		in   []byte
		want error
	}{
		{"length over the cap", frame(MaxFrameBytes+1, ""), errFrame},
		{"no header line", frame(4, `{"a"`), errFrame},
		{"header not JSON", frame(4, "xyz\n"), errFrame},
		{"header without type", frame(3, "{}\n"), errFrame},
		{"truncated payload", frame(10, `{"type"`), io.ErrUnexpectedEOF},
		{"truncated prefix", []byte{0, 0}, io.ErrUnexpectedEOF},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, err := ReadFrame(bytes.NewReader(tc.in)); !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestWriteFrameRefusesBodyOverCap(t *testing.T) {
	body := make([]byte, proxy.MaxBodyBytes+MaxHeaderBytes)
	if err := WriteFrame(io.Discard, Header{Type: TypeDecide}, body); !errors.Is(err, errFrame) {
		t.Fatalf("err = %v, want errFrame", err)
	}
}

func TestDecideAndObserveRoundTrip(t *testing.T) {
	path := sockPath(t)
	brain := &fakeBrain{rw: toLow, ok: true}
	startBrain(t, path, homeA, brain)
	c := newClient(t, path, homeA, time.Second)

	rw, ok := c.Decide(context.Background(), parse(t, smallBody), stepMeta)
	if !ok || rw != toLow {
		t.Fatalf("Decide = %+v, %v; want %+v, true", rw, ok, toLow)
	}
	calls := brain.calls()
	if len(calls) != 1 || calls[0].meta != stepMeta || !bytes.Equal(calls[0].body, smallBody) {
		t.Fatalf("brain saw %+v", calls)
	}
	if !calls[0].hasDeadline {
		t.Fatal("brain decided without the edge's deadline")
	}

	usage := responses.Usage{InputTokens: 900, CachedInputTokens: 800, OutputTokens: 40, TotalTokens: 940}
	c.Observe(stepMeta, "gpt-6-luna", usage, 200)
	eventually(t, "observe", func() bool { return len(brain.observed()) == 1 })
	want := observeCall{stepMeta, "gpt-6-luna", usage, 200}
	if got := brain.observed()[0]; got != want {
		t.Fatalf("observed %+v, want %+v", got, want)
	}
}

func TestDecideNoRewrite(t *testing.T) {
	path := sockPath(t)
	brain := &fakeBrain{}
	startBrain(t, path, homeA, brain)
	c := newClient(t, path, homeA, 0)

	if _, ok := c.Decide(context.Background(), parse(t, smallBody), stepMeta); ok {
		t.Fatal("Decide rewrote though the brain said none")
	}
	if n := len(brain.calls()); n != 1 {
		t.Fatalf("brain got %d decides, want 1", n)
	}
	if _, ok := c.Decide(context.Background(), nil, stepMeta); ok {
		t.Fatal("Decide rewrote a nil request")
	}
}

func TestBrainDownPassesThroughAtOnce(t *testing.T) {
	c := newClient(t, sockPath(t), homeA, time.Second)
	start := time.Now()
	if _, ok := c.Decide(context.Background(), parse(t, smallBody), stepMeta); ok {
		t.Fatal("Decide rewrote with no brain")
	}
	if took := time.Since(start); took > 5*DialTimeout {
		t.Fatalf("Decide took %v with no brain", took)
	}
	c.Observe(stepMeta, "m", responses.Usage{}, 200)
}

func TestBrainSlowPassesThroughAtDeadline(t *testing.T) {
	path := sockPath(t)
	brain := &fakeBrain{rw: toLow, ok: true, slow: slowTurns(stepMeta.TurnID)}
	startBrain(t, path, homeA, brain)
	c := newClient(t, path, homeA, 100*time.Millisecond)

	start := time.Now()
	if _, ok := c.Decide(context.Background(), parse(t, smallBody), stepMeta); ok {
		t.Fatal("Decide rewrote past the deadline")
	}
	if took := time.Since(start); took > time.Second {
		t.Fatalf("Decide took %v with a 100ms deadline", took)
	}
}

func TestBreakerSkipsSlowBrainUntilCooldown(t *testing.T) {
	path := sockPath(t)
	brain := &fakeBrain{rw: toLow, ok: true, slow: slowTurns(stepMeta.TurnID)}
	startBrain(t, path, homeA, brain)
	c := newClient(t, path, homeA, 100*time.Millisecond)
	clk := &clock{t: time.Unix(1_000_000, 0)}
	c.now = clk.now
	req := parse(t, smallBody)
	if !c.Up(t.Context()) {
		t.Fatal("link not up")
	}

	for range FailuresToOpen {
		if _, ok := c.Decide(context.Background(), req, stepMeta); ok {
			t.Fatal("slow brain rewrote")
		}
	}
	if _, ok := c.Decide(context.Background(), req, turn("tu-open")); ok {
		t.Fatal("open breaker rewrote")
	}

	clk.add(BreakerCooldown)
	if rw, ok := c.Decide(context.Background(), req, turn("tu-after")); !ok || rw != toLow {
		t.Fatalf("after cooldown Decide = %+v, %v; want %+v, true", rw, ok, toLow)
	}
	if slices.ContainsFunc(brain.calls(), func(d decideCall) bool { return d.meta.TurnID == "tu-open" }) {
		t.Error("the open breaker let a decide reach the brain")
	}
}

func TestCancelledCallerDoesNotOpenBreaker(t *testing.T) {
	path := sockPath(t)
	brain := &fakeBrain{rw: toLow, ok: true, slow: slowTurns(stepMeta.TurnID)}
	startBrain(t, path, homeA, brain)
	c := newClient(t, path, homeA, 10*time.Second)
	req := parse(t, smallBody)
	if !c.Up(t.Context()) {
		t.Fatal("link not up")
	}

	for range FailuresToOpen {
		ctx, cancel := context.WithCancel(context.Background())
		stop := time.AfterFunc(20*time.Millisecond, cancel)
		if _, ok := c.Decide(ctx, req, stepMeta); ok {
			t.Fatal("slow brain rewrote")
		}
		stop.Stop()
		cancel()
	}

	if rw, ok := c.Decide(context.Background(), req, turn("tu-after")); !ok || rw != toLow {
		t.Fatalf("Decide = %+v, %v; want %+v, true", rw, ok, toLow)
	}
}

func TestStaleDecideIsNotAnswered(t *testing.T) {
	cases := []struct {
		name     string
		deadline time.Duration
		reached  int
	}{
		{"expired before it arrived", -time.Second, 1},
		{"expires inside the handler", replyMargin + 50*time.Millisecond, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := sockPath(t)
			brain := &fakeBrain{rw: toLow, ok: true, slow: slowTurns(stepMeta.TurnID), expired: make(chan struct{}, 1)}
			srv := startBrain(t, path, homeA, brain)
			conn := rawEdge(t, path)
			stale := Header{Type: TypeDecide, ID: 1, Meta: &stepMeta, DeadlineUnixMs: deadlineIn(tc.deadline)}
			if err := WriteFrame(conn, stale, smallBody); err != nil {
				t.Fatal(err)
			}
			if tc.reached == 2 {
				eventually(t, "the handler to give up", func() bool { return len(brain.expired) == 1 })
			}

			fresh := turn("tu-2")
			if err := WriteFrame(conn, Header{Type: TypeDecide, ID: 2, Meta: &fresh, DeadlineUnixMs: deadlineIn(time.Second)}, smallBody); err != nil {
				t.Fatal(err)
			}
			if h, _, err := ReadFrame(conn); err != nil || h.ID != 2 || h.Type != TypeRewrite {
				t.Fatalf("first reply = %+v, %v; want only the rewrite for decide 2", h, err)
			}
			_ = srv.Close()
			if n := len(brain.calls()); n != tc.reached {
				t.Errorf("handler saw %d decides, want %d", n, tc.reached)
			}
		})
	}
}

func deadlineIn(d time.Duration) int64 { return time.Now().Add(d).UnixMilli() }

func rawEdge(t *testing.T, path string) net.Conn {
	t.Helper()
	conn, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	if err := WriteFrame(conn, Header{Type: TypeHello, HomeID: homeA}, nil); err != nil {
		t.Fatal(err)
	}
	if h, _, err := ReadFrame(conn); err != nil || h.Type != TypeHello {
		t.Fatalf("hello reply = %+v, %v", h, err)
	}
	return conn
}

const engineCatalog = `{"models":[
 {"slug":"gpt-6-sol","context_window":272000,"effective_context_window_percent":95,"comp_hash":"3000",
  "supported_reasoning_levels":[{"effort":"low"},{"effort":"medium"},{"effort":"high"}]},
 {"slug":"gpt-6-luna","context_window":272000,"effective_context_window_percent":95,"comp_hash":"3000",
  "supported_reasoning_levels":[{"effort":"low"},{"effort":"medium"},{"effort":"high"}]}
]}`

type slowAsker struct {
	delay time.Duration
	calls atomic.Int32
}

func (a *slowAsker) Ask(ctx context.Context, _ classifier.Input) (classifier.Advice, error) {
	a.calls.Add(1)
	select {
	case <-ctx.Done():
		return classifier.Advice{}, ctx.Err()
	case <-time.After(a.delay):
		return classifier.Advice{Choice: "low", Probabilities: map[classifier.Choice]float64{"low": 0.99}}, nil
	}
}

func (*slowAsker) Health() classifier.Health { return classifier.Health{} }

type decisionSink struct {
	mu sync.Mutex
	ds []route.Decision
}

func (s *decisionSink) OnDecision(d route.Decision) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ds = append(s.ds, d)
}

func (s *decisionSink) OnSnapshot(route.Snapshot) {}

func (s *decisionSink) decisions() []route.Decision {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]route.Decision(nil), s.ds...)
}

func TestDecideDeadlineCoversTransitAndParse(t *testing.T) {
	const wait, classify = 400 * time.Millisecond, 150 * time.Millisecond
	body := []byte(`{"model":"gpt-6-sol","reasoning":{"effort":"medium"},"input":[{"type":"message","role":"user","content":"hi"}]}`)
	cases := []struct {
		name    string
		transit time.Duration
		routed  bool
	}{
		{"classifier answers inside the edge's wait", 0, true},
		{"transit and parse leave too little of it", 300 * time.Millisecond, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cat, err := catalog.Parse([]byte(engineCatalog))
			if err != nil {
				t.Fatal(err)
			}
			store := &catalog.Store{}
			store.Set(cat)
			sink := &decisionSink{}
			asker := &slowAsker{delay: classify}
			eng := route.New(route.Deps{Config: config.Default(), Catalog: store, Classifier: asker, Sink: sink})
			path := sockPath(t)
			startBrain(t, path, homeA, eng)
			conn := rawEdge(t, path)

			if err := WriteFrame(conn, Header{Type: TypeDecide, ID: 1, Meta: &stepMeta, DeadlineUnixMs: deadlineIn(wait - tc.transit)}, body); err != nil {
				t.Fatal(err)
			}
			eventually(t, "the classifier call", func() bool { return asker.calls.Load() == 1 })
			next := turn("tu-2")
			if err := WriteFrame(conn, Header{Type: TypeDecide, ID: 2, Meta: &next, DeadlineUnixMs: deadlineIn(wait)}, body); err != nil {
				t.Fatal(err)
			}
			eventually(t, "the next turn's decision", func() bool {
				ds := sink.decisions()
				return len(ds) > 0 && ds[len(ds)-1].TurnID == next.TurnID
			})

			ds := sink.decisions()
			if !tc.routed {
				if len(ds) != 1 {
					t.Fatalf("decisions = %+v, want none for the late decide: the edge forwarded the original", ds)
				}
				return
			}
			if len(ds) != 2 || !ds[0].Rewritten || ds[0].Route.Model != "gpt-6-luna" {
				t.Fatalf("decisions = %+v, want a switch to gpt-6-luna first", ds)
			}
		})
	}
}

func TestClientReconnectsAfterBrainRestart(t *testing.T) {
	path := sockPath(t)
	brain := &fakeBrain{rw: toLow, ok: true}
	first := startBrain(t, path, homeA, brain)
	c := newClient(t, path, homeA, time.Second)
	req := parse(t, smallBody)

	if _, ok := c.Decide(context.Background(), req, stepMeta); !ok {
		t.Fatal("no rewrite before the restart")
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	if _, ok := c.Decide(context.Background(), req, stepMeta); ok {
		t.Fatal("rewrite while the brain is down")
	}

	startBrain(t, path, homeA, brain)
	eventually(t, "reconnect", func() bool {
		_, ok := c.Decide(context.Background(), req, stepMeta)
		return ok
	})
}

func TestUpFollowsTheBrain(t *testing.T) {
	path := sockPath(t)
	c := newClient(t, path, homeA, time.Second)
	if c.Up(context.Background()) {
		t.Fatal("Up with no brain")
	}
	brain := startBrain(t, path, homeA, &fakeBrain{})
	eventually(t, "brain up", func() bool { return c.Up(context.Background()) })
	if err := brain.Close(); err != nil {
		t.Fatal(err)
	}
	eventually(t, "brain down", func() bool { return !c.Up(context.Background()) })
}

func TestWrongHomeRejected(t *testing.T) {
	path := sockPath(t)
	brain := &fakeBrain{rw: toLow, ok: true}
	startBrain(t, path, homeA, brain)

	c := newClient(t, path, homeB, time.Second)
	if _, ok := c.Decide(context.Background(), parse(t, smallBody), stepMeta); ok {
		t.Fatal("brain of another home rewrote")
	}
	if n := len(brain.calls()); n != 0 {
		t.Fatalf("brain decided %d times for another home", n)
	}

	conn, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	if err := WriteFrame(conn, Header{Type: TypeHello, HomeID: homeB}, nil); err != nil {
		t.Fatal(err)
	}
	h, _, err := ReadFrame(conn)
	if err != nil || h.Type != TypeError || !strings.Contains(h.Error, homeB) {
		t.Fatalf("reply = %+v, %v; want an error naming %s", h, err, homeB)
	}
	if _, _, err := ReadFrame(conn); !errors.Is(err, io.EOF) {
		t.Fatalf("connection still open after refusal: %v", err)
	}
}

func TestDecideCarriesMaxBody(t *testing.T) {
	head, tail := `{"model":"gpt-6.1-sol","input":[{"type":"message","role":"user","content":"`, `"}]}`
	body := []byte(head + strings.Repeat("a", proxy.MaxBodyBytes-len(head)-len(tail)) + tail)
	path := sockPath(t)
	brain := &fakeBrain{rw: toLow, ok: true}
	startBrain(t, path, homeA, brain)
	c := newClient(t, path, homeA, 30*time.Second)

	if rw, ok := c.Decide(context.Background(), parse(t, body), stepMeta); !ok || rw != toLow {
		t.Fatalf("Decide = %+v, %v", rw, ok)
	}
	if calls := brain.calls(); len(calls) != 1 || !bytes.Equal(calls[0].body, body) {
		t.Fatal("brain did not get the 32 MiB body intact")
	}
}

func TestObserveQueueFullDoesNotBlock(t *testing.T) {
	path := sockPath(t)
	brain := &fakeBrain{hold: make(chan struct{})}
	startBrain(t, path, homeA, brain)
	t.Cleanup(func() { close(brain.hold) })
	c := newClient(t, path, homeA, time.Second)

	done := make(chan struct{})
	go func() {
		for range 20 * ObserveQueue {
			c.Observe(stepMeta, "gpt-6-luna", responses.Usage{TotalTokens: 1}, 200)
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Observe blocked behind a stuck brain")
	}
}

func TestListenSocketMode(t *testing.T) {
	path := sockPath(t)
	startBrain(t, path, homeA, &fakeBrain{})
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != SocketMode {
		t.Fatalf("socket mode %v, want %v", fi.Mode().Perm(), SocketMode)
	}
}

func staleSocket(t *testing.T) string {
	t.Helper()
	path := sockPath(t)
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	ln.(*net.UnixListener).SetUnlinkOnClose(false)
	_ = ln.Close()
	return path
}

func TestListenRefuses(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T) string
	}{
		{"a live socket", func(t *testing.T) string {
			path := sockPath(t)
			startBrain(t, path, homeA, &fakeBrain{})
			return path
		}},
		{"a dir open to others", func(t *testing.T) string {
			path := sockPath(t)
			if err := os.Chmod(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			return path
		}},
		{"a regular file", func(t *testing.T) string {
			path := sockPath(t)
			if err := os.WriteFile(path, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			return path
		}},
		{"a directory", func(t *testing.T) string {
			path := sockPath(t)
			if err := os.Mkdir(path, 0o700); err != nil {
				t.Fatal(err)
			}
			return path
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := tc.setup(t)
			_, err := os.Lstat(path)
			existed := err == nil
			if s, err := Listen(path, homeA, &fakeBrain{}, nil); err == nil {
				_ = s.Close()
				t.Fatal("Listen succeeded")
			}
			if _, err := os.Lstat(path); (err == nil) != existed {
				t.Errorf("Lstat after Listen = %v, existed before: %v", err, existed)
			}
		})
	}
}

func TestListenReplacesStaleSocket(t *testing.T) {
	path := staleSocket(t)
	brain := &fakeBrain{rw: toLow, ok: true}
	startBrain(t, path, homeA, brain)
	c := newClient(t, path, homeA, time.Second)

	if rw, ok := c.Decide(context.Background(), parse(t, smallBody), stepMeta); !ok || rw != toLow {
		t.Fatalf("Decide = %+v, %v; want %+v, true", rw, ok, toLow)
	}
}

type sniff struct {
	mu   sync.Mutex
	sent bytes.Buffer
}

func (s *sniff) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sent.Write(p)
}

func (s *sniff) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sent.String()
}

func startSniff(t *testing.T, front, back string) *sniff {
	t.Helper()
	ln, err := net.Listen("unix", front)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	s := &sniff{}
	go func() {
		for {
			in, err := ln.Accept()
			if err != nil {
				return
			}
			out, err := net.Dial("unix", back)
			if err != nil {
				_ = in.Close()
				continue
			}
			go func() { _, _ = io.Copy(out, io.TeeReader(in, s)); _ = out.Close() }()
			go func() { _, _ = io.Copy(in, out); _ = in.Close() }()
		}
	}()
	return s
}

func TestLinkFramesCarryOnlyTheAccountHash(t *testing.T) {
	const account = "acct-raw-secret-4567"
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":"+
			"{\"model\":\"gpt-6-luna\",\"usage\":{\"input_tokens\":10,\"output_tokens\":2,\"total_tokens\":12}}}\n\n")
	}))
	t.Cleanup(upstream.Close)
	back := sockPath(t)
	brain := &fakeBrain{rw: toLow, ok: true}
	startBrain(t, back, homeA, brain)
	front := sockPath(t)
	frames := startSniff(t, front, back)
	c := newClient(t, front, homeA, time.Second)
	logs := &bytes.Buffer{}
	p := proxy.New(proxy.Options{
		Upstream: upstream.URL, Decider: c, Observer: c,
		Logger: slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
	})
	port, err := p.Listen()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- p.Serve(ctx) }()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost,
		fmt.Sprintf("http://127.0.0.1:%d%s", port, proxy.PathResponses), bytes.NewReader(smallBody))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set(responses.HeaderAccount, account)
	req.Header.Set(responses.HeaderTurnMetadata, `{"thread_id":"th-1","turn_id":"tu-1","request_kind":"turn"}`)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	eventually(t, "observe", func() bool { return len(brain.observed()) == 1 })

	hash := responses.AccountHash(account)
	if calls := brain.calls(); len(calls) != 1 || calls[0].meta.Account != hash {
		t.Fatalf("brain saw %+v, want account %s", calls, hash)
	}
	if got := brain.observed()[0].meta.Account; got != hash {
		t.Fatalf("observed account %q, want %s", got, hash)
	}
	if sent := frames.String(); !strings.Contains(sent, hash) || strings.Contains(sent, account) {
		t.Fatalf("frames carry the raw account or miss its hash:\n%q", sent)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Serve: %v", err)
	}
	if strings.Contains(logs.String(), account) {
		t.Fatalf("logs carry the raw account:\n%s", logs.String())
	}
}

func silentBrain(t *testing.T, path string) *atomic.Int32 {
	t.Helper()
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	var accepts atomic.Int32
	var mu sync.Mutex
	var conns []net.Conn
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			accepts.Add(1)
			mu.Lock()
			conns = append(conns, c)
			mu.Unlock()
		}
	}()
	t.Cleanup(func() {
		_ = ln.Close()
		mu.Lock()
		defer mu.Unlock()
		for _, c := range conns {
			_ = c.Close()
		}
	})
	return &accepts
}

func TestHelloStallIsBoundedAndBacksOff(t *testing.T) {
	path := sockPath(t)
	accepts := silentBrain(t, path)
	c := newClient(t, path, homeA, 5*time.Second)
	clk := &clock{t: time.Unix(1_000_000, 0)}
	c.now = clk.now
	req := parse(t, smallBody)

	const callers = 8
	var wg sync.WaitGroup
	took := make([]time.Duration, callers)
	for i := range callers {
		wg.Go(func() {
			start := time.Now()
			if _, ok := c.Decide(context.Background(), req, stepMeta); ok {
				t.Error("a silent brain rewrote")
			}
			took[i] = time.Since(start)
		})
	}
	wg.Wait()

	for i, d := range took {
		if d > DialTimeout+time.Second {
			t.Errorf("caller %d waited %v on a silent brain, want about %v", i, d, DialTimeout)
		}
	}
	if n := accepts.Load(); n != 1 {
		t.Fatalf("%d dials for %d concurrent callers, want 1", n, callers)
	}

	if _, ok := c.Decide(context.Background(), req, stepMeta); ok {
		t.Fatal("rewrite inside the backoff")
	}
	if n := accepts.Load(); n != 1 {
		t.Fatalf("%d dials after a call inside the backoff, want 1", n)
	}

	clk.add(BackoffMin)
	c.Decide(context.Background(), req, stepMeta)
	if n := accepts.Load(); n != 2 {
		t.Fatalf("%d dials after the backoff ended, want 2", n)
	}
}

func TestBrainClosingMidDecideReturnsAtOnce(t *testing.T) {
	path := sockPath(t)
	brain := &fakeBrain{rw: toLow, ok: true, slow: slowTurns(stepMeta.TurnID)}
	srv := startBrain(t, path, homeA, brain)
	c := newClient(t, path, homeA, 30*time.Second)

	done := make(chan bool, 1)
	go func() {
		_, ok := c.Decide(context.Background(), parse(t, smallBody), stepMeta)
		done <- ok
	}()
	eventually(t, "the decide to reach the brain", func() bool { return len(brain.calls()) == 1 })
	if err := srv.Close(); err != nil {
		t.Fatal(err)
	}

	select {
	case ok := <-done:
		if ok {
			t.Error("a closing brain rewrote")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Decide kept waiting after the brain closed")
	}
}

func TestDecideBehindABusyConnPassesThrough(t *testing.T) {
	cases := []struct {
		name         string
		timeout      time.Duration
		queueObserve bool
		closeClient  bool
	}{
		{"write waits past the deadline", 150 * time.Millisecond, false, false},
		{"flush waits past the deadline", 150 * time.Millisecond, true, false},
		{"client closes during the flush", 30 * time.Second, true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := sockPath(t)
			brain := &fakeBrain{rw: toLow, ok: true}
			startBrain(t, path, homeA, brain)
			c := newClient(t, path, homeA, tc.timeout)
			if !c.Up(t.Context()) {
				t.Fatal("link not up")
			}
			c.mu.Lock()
			cc := c.conn
			c.mu.Unlock()
			cc.wsem <- struct{}{}
			if tc.queueObserve {
				c.Observe(stepMeta, "m", responses.Usage{}, 200)
				eventually(t, "the writer to take the observe", func() bool { return len(c.observes) == 0 })
			}

			done := make(chan bool, 1)
			go func() {
				_, ok := c.Decide(context.Background(), parse(t, smallBody), stepMeta)
				done <- ok
			}()
			if tc.closeClient {
				eventually(t, "the decide to queue behind the observe", func() bool { return len(c.observes) == 1 })
				_ = c.Close()
			}

			select {
			case ok := <-done:
				if ok {
					t.Error("Decide rewrote though it could not be written")
				}
			case <-time.After(5 * time.Second):
				t.Fatal("Decide kept waiting behind the busy conn")
			}
			if n := len(brain.calls()); n != 0 {
				t.Errorf("brain got %d decides, want 0", n)
			}
		})
	}
}

func TestClosedClientPassesThrough(t *testing.T) {
	path := sockPath(t)
	startBrain(t, path, homeA, &fakeBrain{rw: toLow, ok: true})
	c := newClient(t, path, homeA, time.Second)
	if !c.Up(t.Context()) {
		t.Fatal("link not up")
	}

	for range 2 {
		if err := c.Close(); err != nil {
			t.Fatalf("Close = %v", err)
		}
	}
	if _, ok := c.Decide(context.Background(), parse(t, smallBody), stepMeta); ok {
		t.Error("closed client rewrote")
	}
	if c.Up(context.Background()) {
		t.Error("closed client is up")
	}
}

type panicBrain struct{}

func (*panicBrain) Decide(context.Context, *responses.Request, responses.Meta) (responses.Rewrite, bool) {
	panic("boom")
}

func (*panicBrain) Observe(responses.Meta, string, responses.Usage, int) { panic("boom") }

func TestBadFramesDoNotBreakTheConnection(t *testing.T) {
	cases := []struct {
		name    string
		handler Handler
		frame   Header
		body    []byte
		reply   string
		next    string
	}{
		{"decide without meta", &fakeBrain{rw: toLow, ok: true}, Header{Type: TypeDecide, ID: 1}, smallBody, TypeNone, TypeRewrite},
		{"decide with an unparseable body", &fakeBrain{rw: toLow, ok: true}, Header{Type: TypeDecide, ID: 1, Meta: &stepMeta}, []byte("not json"), TypeNone, TypeRewrite},
		{"decide that panics the handler", &panicBrain{}, Header{Type: TypeDecide, ID: 1, Meta: &stepMeta}, smallBody, TypeNone, TypeNone},
		{"observe without meta", &fakeBrain{rw: toLow, ok: true}, Header{Type: TypeObserve}, nil, "", TypeRewrite},
		{"observe that panics the handler", &panicBrain{}, Header{Type: TypeObserve, Meta: &stepMeta, Usage: &responses.Usage{}}, nil, "", TypeNone},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := sockPath(t)
			startBrain(t, path, homeA, tc.handler)
			conn := rawEdge(t, path)
			if err := WriteFrame(conn, tc.frame, tc.body); err != nil {
				t.Fatal(err)
			}
			if tc.frame.Type == TypeDecide {
				if h, _, err := ReadFrame(conn); err != nil || h.ID != 1 || h.Type != tc.reply {
					t.Fatalf("reply = %+v, %v; want %s for decide 1", h, err, tc.reply)
				}
			}

			next := Header{Type: TypeDecide, ID: 2, Meta: &stepMeta, DeadlineUnixMs: deadlineIn(time.Second)}
			if err := WriteFrame(conn, next, smallBody); err != nil {
				t.Fatal(err)
			}
			if h, _, err := ReadFrame(conn); err != nil || h.ID != 2 || h.Type != tc.next {
				t.Fatalf("reply to the next decide = %+v, %v; want %s", h, err, tc.next)
			}
		})
	}
}

func TestSilentClientIsDroppedAfterHelloTimeout(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		s := &Server{homeID: homeA, h: &fakeBrain{}, log: slog.Default(), ctx: ctx, cancel: cancel, conns: map[net.Conn]struct{}{}}
		edge, brainEnd := net.Pipe()
		s.wg.Add(1)
		go s.serve(brainEnd)

		start := time.Now()
		h, _, err := ReadFrame(edge)
		if err != nil || h.Type != TypeError {
			t.Fatalf("first frame = %+v, %v; want an error before the close", h, err)
		}
		if _, _, err := ReadFrame(edge); !errors.Is(err, io.EOF) {
			t.Fatalf("read after the error = %v, want EOF", err)
		}
		if took := time.Since(start); took != helloTimeout {
			t.Errorf("dropped after %v, want the hello timeout %v", took, helloTimeout)
		}
		cancel()
		s.wg.Wait()
	})
}

type frameLog struct {
	mu     sync.Mutex
	frames []string
}

func (l *frameLog) got() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.frames)
}

func startFrameLog(t *testing.T, path string) *frameLog {
	t.Helper()
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	l := &frameLog{}
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		if _, _, err := ReadFrame(conn); err != nil {
			return
		}
		if WriteFrame(conn, Header{Type: TypeHello, HomeID: homeA}, nil) != nil {
			return
		}
		for {
			h, _, err := ReadFrame(conn)
			if err != nil {
				return
			}
			l.mu.Lock()
			l.frames = append(l.frames, h.Type+" "+h.Meta.TurnID)
			l.mu.Unlock()
			if h.Type == TypeDecide {
				_ = WriteFrame(conn, Header{Type: TypeNone, ID: h.ID}, nil)
			}
		}
	}()
	return l
}

func TestDecideIsWrittenAfterTheObservesQueuedBeforeIt(t *testing.T) {
	path := sockPath(t)
	brain := startFrameLog(t, path)
	c := newClient(t, path, homeA, time.Second)
	if !c.Up(t.Context()) {
		t.Fatal("link not up")
	}
	c.mu.Lock()
	cc := c.conn
	c.mu.Unlock()
	cc.wsem <- struct{}{}
	c.Observe(turn("tu-1"), "m", responses.Usage{InputTokens: 1}, 200)
	eventually(t, "the writer to take the first observe", func() bool { return len(c.observes) == 0 })
	c.Observe(turn("tu-2"), "m", responses.Usage{InputTokens: 1}, 200)

	decided := make(chan struct{})
	go func() {
		defer close(decided)
		c.Decide(context.Background(), parse(t, smallBody), turn("tu-3"))
	}()
	eventually(t, "the decide to queue behind the observes", func() bool { return len(c.observes) == 2 })
	<-cc.wsem
	<-decided

	want := []string{"observe tu-1", "observe tu-2", "decide tu-3"}
	eventually(t, "three frames", func() bool { return len(brain.got()) == len(want) })
	if got := brain.got(); !slices.Equal(got, want) {
		t.Fatalf("frames = %q, want %q", got, want)
	}
}
