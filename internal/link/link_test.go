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
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

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

// fakeBrain answers rw (when ok) to every decide. slow makes a decide wait
// for its context; hold, when set, blocks every observe until closed.
type fakeBrain struct {
	rw   responses.Rewrite
	ok   bool
	slow atomic.Bool
	hold chan struct{}

	mu       sync.Mutex
	decides  []decideCall
	observes []observeCall
}

func (b *fakeBrain) Decide(ctx context.Context, req *responses.Request, m responses.Meta) (responses.Rewrite, bool) {
	_, hasDeadline := ctx.Deadline()
	b.mu.Lock()
	b.decides = append(b.decides, decideCall{meta: m, body: req.Body(), hasDeadline: hasDeadline})
	b.mu.Unlock()
	if b.slow.Load() {
		<-ctx.Done()
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

// sockPath is a socket path in a fresh 0700 dir, short enough for macOS.
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
	c := NewClient(ClientOptions{Path: path, HomeID: home, Timeout: timeout})
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
		{"decide", Header{Type: TypeDecide, ID: 7, Meta: &stepMeta, TimeoutMs: 1700}, smallBody},
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
			if h.Type != tc.h.Type || h.ID != tc.h.ID || h.HomeID != tc.h.HomeID || h.TimeoutMs != tc.h.TimeoutMs {
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
	c := newClient(t, path, homeA, time.Second)

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
	if took := time.Since(start); took > DialTimeout {
		t.Fatalf("Decide took %v with no brain", took)
	}
	c.Observe(stepMeta, "m", responses.Usage{}, 200) // must not block or panic
}

func TestBrainSlowPassesThroughAtDeadline(t *testing.T) {
	path := sockPath(t)
	brain := &fakeBrain{rw: toLow, ok: true}
	brain.slow.Store(true)
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
	brain := &fakeBrain{rw: toLow, ok: true}
	brain.slow.Store(true)
	startBrain(t, path, homeA, brain)
	c := newClient(t, path, homeA, 50*time.Millisecond)
	clk := &clock{t: time.Unix(1_000_000, 0)}
	c.now = clk.now
	req := parse(t, smallBody)

	for range FailuresToOpen {
		if _, ok := c.Decide(context.Background(), req, stepMeta); ok {
			t.Fatal("slow brain rewrote")
		}
	}
	eventually(t, "slow decides", func() bool { return len(brain.calls()) == FailuresToOpen })
	brain.slow.Store(false)

	start := time.Now()
	if _, ok := c.Decide(context.Background(), req, stepMeta); ok {
		t.Fatal("open breaker rewrote")
	}
	if took := time.Since(start); took > 20*time.Millisecond {
		t.Fatalf("open breaker took %v", took)
	}
	if n := len(brain.calls()); n != FailuresToOpen {
		t.Fatalf("open breaker reached the brain: %d decides", n)
	}

	clk.add(BreakerCooldown)
	if rw, ok := c.Decide(context.Background(), req, stepMeta); !ok || rw != toLow {
		t.Fatalf("after cooldown Decide = %+v, %v", rw, ok)
	}
}

func TestLateDecideIsNotAnswered(t *testing.T) {
	path := sockPath(t)
	brain := &fakeBrain{rw: toLow, ok: true}
	brain.slow.Store(true)
	startBrain(t, path, homeA, brain)
	conn, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	if err := WriteFrame(conn, Header{Type: TypeHello, HomeID: homeA}, nil); err != nil {
		t.Fatal(err)
	}
	if h, _, err := ReadFrame(conn); err != nil || h.Type != TypeHello {
		t.Fatalf("hello reply = %+v, %v", h, err)
	}

	if err := WriteFrame(conn, Header{Type: TypeDecide, ID: 1, Meta: &stepMeta, TimeoutMs: 20}, smallBody); err != nil {
		t.Fatal(err)
	}
	eventually(t, "late decide", func() bool { return len(brain.calls()) == 1 })
	time.Sleep(100 * time.Millisecond)
	brain.slow.Store(false)
	if err := WriteFrame(conn, Header{Type: TypeDecide, ID: 2, Meta: &stepMeta, TimeoutMs: 1000}, smallBody); err != nil {
		t.Fatal(err)
	}

	h, _, err := ReadFrame(conn)
	if err != nil || h.ID != 2 || h.Type != TypeRewrite {
		t.Fatalf("first reply = %+v, %v; want only the answer to decide 2", h, err)
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

func TestListenSocketRules(t *testing.T) {
	path := sockPath(t)
	startBrain(t, path, homeA, &fakeBrain{})
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != SocketMode {
		t.Fatalf("socket mode %v, want %v", fi.Mode().Perm(), SocketMode)
	}
	if _, err := Listen(path, homeA, &fakeBrain{}, nil); err == nil {
		t.Fatal("Listen took over a live socket")
	}

	open := sockPath(t)
	if err := os.Chmod(filepath.Dir(open), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Listen(open, homeA, &fakeBrain{}, nil); err == nil {
		t.Fatal("Listen accepted a dir open to others")
	}

	file := sockPath(t)
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Listen(file, homeA, &fakeBrain{}, nil); err == nil {
		t.Fatal("Listen replaced a regular file")
	}
}

// sniff relays unix connections from front to back and records every byte
// the edge sends.
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
