package control

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alexei-led/codex-router/internal/config"
	"github.com/alexei-led/codex-router/internal/home"
	"github.com/alexei-led/codex-router/internal/route"
)

const testHome = "3fa9c0d1e2b4"

var testHello = Hello{EndpointID: "ep-1", Kind: "session", Home: home.Home{ID: testHome, Path: "/h/.codex-work", Name: "work"}}

func sid(thread string) string { return SessionID(testHome, thread) }

type fakeHandler struct {
	mu       sync.Mutex
	sessions []route.SessionInfo
	replies  map[string]int // per thread
	applied  []route.Command
	err      error
	onApply  func(route.Command)
}

func newHandler() *fakeHandler { return &fakeHandler{replies: map[string]int{}} }

func (h *fakeHandler) Hello() Hello { return testHello }

func (h *fakeHandler) Apply(c route.Command) error {
	if h.onApply != nil {
		h.onApply(c)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.err != nil {
		return h.err
	}
	if c.Tier == "bad" {
		return errors.New("pin: unknown tier \"bad\"")
	}
	h.applied = append(h.applied, c)
	return nil
}

func (h *fakeHandler) Sessions() []route.SessionInfo {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]route.SessionInfo(nil), h.sessions...)
}

func (h *fakeHandler) SnapshotFor(thread string) route.Snapshot {
	h.mu.Lock()
	defer h.mu.Unlock()
	return route.Snapshot{ThreadID: thread, Stats: route.Stats{Replies: h.replies[thread]}}
}

func (h *fakeHandler) set(f func(h *fakeHandler)) {
	h.mu.Lock()
	defer h.mu.Unlock()
	f(h)
}

func (h *fakeHandler) appliedCmds() []route.Command {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]route.Command(nil), h.applied...)
}

func sockPath(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "cr")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return filepath.Join(dir, SocketName)
}

func startServer(t *testing.T, h Handler) (*Server, string) {
	t.Helper()
	path := sockPath(t)
	s, err := Listen(path, h)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s, path
}

func dial(t *testing.T, path string) *Client {
	t.Helper()
	c, err := Dial(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func subscribed(t *testing.T, path, thread string) *Client {
	t.Helper()
	c := dial(t, path)
	if err := c.Subscribe(sid(thread)); err != nil {
		t.Fatal(err)
	}
	next(t, c)
	return c
}

func next(t *testing.T, c *Client) route.Snapshot {
	t.Helper()
	select {
	case s, ok := <-c.Snapshots():
		if !ok {
			t.Fatal("snapshots closed")
		}
		return s
	case <-time.After(3 * time.Second):
		t.Fatal("no snapshot")
		return route.Snapshot{}
	}
}

func nextList(t *testing.T, c *Client) []route.SessionInfo {
	t.Helper()
	select {
	case l, ok := <-c.Sessions():
		if !ok {
			t.Fatal("sessions closed")
		}
		return l
	case <-time.After(3 * time.Second):
		t.Fatal("no sessions list")
		return nil
	}
}

func quiet[T any](t *testing.T, ch <-chan T, what string) {
	t.Helper()
	select {
	case v := <-ch:
		t.Fatalf("unexpected %s: %+v", what, v)
	case <-time.After(4 * PublishInterval):
	}
}

func closes[T any](t *testing.T, ch <-chan T, what string) {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		select {
		case _, ok := <-ch:
			if !ok {
				return
			}
		case <-deadline:
			t.Fatalf("%s not closed", what)
		}
	}
}

// raw is a protocol-level client for checking server lines.
type raw struct {
	conn net.Conn
	r    *bufio.Reader
}

func rawDial(t *testing.T, path string) *raw {
	t.Helper()
	conn, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return &raw{conn: conn, r: bufio.NewReader(conn)}
}

func (r *raw) send(t *testing.T, line string) {
	t.Helper()
	if _, err := r.conn.Write([]byte(line + "\n")); err != nil {
		t.Fatal(err)
	}
}

func (r *raw) read(t *testing.T) Message {
	t.Helper()
	_ = r.conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	line, err := r.r.ReadBytes('\n')
	if err != nil {
		t.Fatal(err)
	}
	var m Message
	if err := json.Unmarshal(line, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

// readReply skips pushed lines up to the next ack or error.
func (r *raw) readReply(t *testing.T) Message {
	t.Helper()
	for {
		if m := r.read(t); m.Type == MessageAck || m.Type == MessageError {
			return m
		}
	}
}

func (r *raw) hello(t *testing.T) {
	t.Helper()
	r.send(t, `{"op":"hello","protocol":2}`)
	if m := r.read(t); m.Type != MessageHello {
		t.Fatalf("reply = %+v, want hello", m)
	}
}

func TestListenSocketMode(t *testing.T) {
	old := setUmask(0)
	defer setUmask(old)
	_, path := startServer(t, newHandler())
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := fi.Mode().Perm(); got != SocketMode {
		t.Fatalf("mode = %o, want %o", got, SocketMode)
	}
}

func TestListenRejectsLiveSocket(t *testing.T) {
	_, path := startServer(t, newHandler())
	if s, err := Listen(path, newHandler()); err == nil {
		_ = s.Close()
		t.Fatal("second Listen on a live socket succeeded")
	}
	dial(t, path)
}

func TestListenReplacesStaleSocket(t *testing.T) {
	path := sockPath(t)
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	ln.(*net.UnixListener).SetUnlinkOnClose(false)
	_ = ln.Close()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("stale socket file missing: %v", err)
	}
	s, err := Listen(path, newHandler())
	if err != nil {
		t.Fatalf("Listen over stale socket: %v", err)
	}
	_ = s.Close()
}

func TestCloseRemovesSocket(t *testing.T) {
	s, path := startServer(t, newHandler())
	_ = s.Close()
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("socket still present: %v", err)
	}
}

func TestHandshake(t *testing.T) {
	h := newHandler()
	h.sessions = []route.SessionInfo{{ID: sid("a"), ThreadID: "a"}}
	_, path := startServer(t, h)
	c := dial(t, path)
	if c.Hello() != testHello {
		t.Fatalf("hello = %+v, want %+v", c.Hello(), testHello)
	}
	if l := nextList(t, c); len(l) != 1 || l[0].ID != sid("a") {
		t.Fatalf("first sessions list = %+v", l)
	}
	quiet(t, c.Snapshots(), "snapshot before subscribe")
}

func TestHandshakeRejected(t *testing.T) {
	tests := []struct{ name, line, want string }{
		{"command first", `{"op":"pin","tier":"high"}`, "expected hello"},
		{"not json", `hello`, "expected hello"},
		{"old protocol", `{"op":"hello","protocol":1}`, "unsupported protocol 1, want 2"},
		{"no protocol", `{"op":"hello"}`, "unsupported protocol 0, want 2"},
		{"newer protocol", `{"op":"hello","protocol":3}`, "unsupported protocol 3, want 2"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHandler()
			_, path := startServer(t, h)
			r := rawDial(t, path)
			r.send(t, tt.line)
			if m := r.read(t); m.Type != MessageError || m.Error != tt.want {
				t.Fatalf("reply = %+v, want error %q", m, tt.want)
			}
			if _, err := r.r.ReadBytes('\n'); !errors.Is(err, io.EOF) {
				t.Fatalf("connection still open after a rejected hello: %v", err)
			}
			if len(h.appliedCmds()) != 0 {
				t.Fatal("a command before hello reached the handler")
			}
		})
	}
}

func TestDialReportsRejectedHello(t *testing.T) {
	path := sockPath(t)
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		_, _ = bufio.NewReader(conn).ReadBytes('\n')
		_, _ = conn.Write([]byte(`{"type":"error","error":"unsupported protocol 2, want 3"}` + "\n"))
	}()
	c, err := Dial(path)
	if err == nil {
		_ = c.Close()
		t.Fatal("Dial accepted a rejected hello")
	}
	if !strings.Contains(err.Error(), "unsupported protocol 2, want 3") {
		t.Fatalf("err = %v", err)
	}
}

func TestDialMissingSocketFails(t *testing.T) {
	if c, err := Dial(sockPath(t)); err == nil {
		_ = c.Close()
		t.Fatal("Dial to a missing socket succeeded")
	}
}

func TestSessionsPushedOnChangeOnly(t *testing.T) {
	h := newHandler()
	s, path := startServer(t, h)
	c := dial(t, path)
	if l := nextList(t, c); len(l) != 0 {
		t.Fatalf("first list = %+v, want empty", l)
	}
	h.set(func(h *fakeHandler) { h.sessions = []route.SessionInfo{{ID: sid("a")}, {ID: sid("b")}} })
	s.Publish(route.Snapshot{ThreadID: "a"})
	if l := nextList(t, c); len(l) != 2 || l[1].ID != sid("b") {
		t.Fatalf("list = %+v, want a and b", l)
	}
	s.Publish(route.Snapshot{ThreadID: "a"})
	quiet(t, c.Sessions(), "unchanged sessions list")
}

func TestSubscriptionSwitch(t *testing.T) {
	h := newHandler()
	h.replies["a"], h.replies["b"] = 1, 2
	s, path := startServer(t, h)
	c := dial(t, path)

	if err := c.Subscribe(sid("a")); err != nil {
		t.Fatal(err)
	}
	if snap := next(t, c); snap.ThreadID != "a" || snap.Stats.Replies != 1 {
		t.Fatalf("after subscribe a: %+v", snap)
	}
	s.Publish(route.Snapshot{ThreadID: "b", Stats: route.Stats{Replies: 20}})
	quiet(t, c.Snapshots(), "snapshot of an unsubscribed session")

	s.Publish(route.Snapshot{ThreadID: "a", Stats: route.Stats{Replies: 10}})
	if err := c.Subscribe(sid("b")); err != nil {
		t.Fatal(err)
	}
	if snap := next(t, c); snap.ThreadID != "b" || snap.Stats.Replies != 2 {
		t.Fatalf("after subscribe b: %+v", snap)
	}
	s.Publish(route.Snapshot{ThreadID: "a", Stats: route.Stats{Replies: 11}})
	quiet(t, c.Snapshots(), "snapshot of the old session")
	s.Publish(route.Snapshot{ThreadID: "b", Stats: route.Stats{Replies: 21}})
	if snap := next(t, c); snap.Stats.Replies != 21 {
		t.Fatalf("pushed b: %+v", snap)
	}

	if err := c.Subscribe(""); err != nil {
		t.Fatal(err)
	}
	s.Publish(route.Snapshot{ThreadID: "b", Stats: route.Stats{Replies: 22}})
	quiet(t, c.Snapshots(), "snapshot after unsubscribe")
}

func TestSubscribeRejectsForeignSession(t *testing.T) {
	_, path := startServer(t, newHandler())
	c := dial(t, path)
	for _, session := range []string{"otherhome/a", "a", testHome + "/"} {
		if err := c.Subscribe(session); err == nil || !strings.Contains(err.Error(), "unknown session") {
			t.Errorf("Subscribe(%q) err = %v, want unknown session", session, err)
		}
	}
}

func TestTwoClientsOnOneSessionBothGetSnapshots(t *testing.T) {
	s, path := startServer(t, newHandler())
	a, b := subscribed(t, path, "th"), subscribed(t, path, "th")
	s.Publish(route.Snapshot{ThreadID: "th", Stats: route.Stats{Replies: 3}})
	for _, c := range []*Client{a, b} {
		if got := next(t, c).Stats.Replies; got != 3 {
			t.Fatalf("replies = %d, want 3", got)
		}
	}
}

func TestPublishCoalescesBursts(t *testing.T) {
	s, path := startServer(t, newHandler())
	c := subscribed(t, path, "th")
	const burst = 100
	for i := 1; i <= burst; i++ {
		s.Publish(route.Snapshot{ThreadID: "th", Seq: uint64(i), Stats: route.Stats{Replies: i}})
	}
	got, last := 0, 0
	deadline := time.After(3 * time.Second)
	for last != burst {
		select {
		case snap := <-c.Snapshots():
			got++
			last = snap.Stats.Replies
		case <-deadline:
			t.Fatalf("last snapshot not delivered, got %d updates, last %d", got, last)
		}
	}
	if got >= burst/2 {
		t.Fatalf("%d updates for a burst of %d; not coalesced", got, burst)
	}
}

func TestPublishDropsAnOlderSnapshot(t *testing.T) {
	s, path := startServer(t, newHandler())
	c := subscribed(t, path, "th")
	s.Publish(route.Snapshot{ThreadID: "th", Seq: 2})
	s.Publish(route.Snapshot{ThreadID: "th", Seq: 1})
	if snap := next(t, c); snap.Seq != 2 {
		t.Fatalf("snapshot seq = %d, want 2", snap.Seq)
	}
	time.Sleep(2 * PublishInterval)
	s.Publish(route.Snapshot{ThreadID: "th", Seq: 1})
	quiet(t, c.Snapshots(), "snapshot older than the last delivered")
}

func TestCommandRefreshesEverySubscription(t *testing.T) {
	h := newHandler()
	h.onApply = func(route.Command) { h.set(func(h *fakeHandler) { h.replies["b"] = 5 }) }
	_, path := startServer(t, h)
	watcher := subscribed(t, path, "b")
	sender := dial(t, path)
	if err := sender.Send(route.Command{Op: route.OpPause, Scope: route.ScopeHome}); err != nil {
		t.Fatal(err)
	}
	if got := next(t, watcher).Stats.Replies; got != 5 {
		t.Fatalf("replies = %d, want the refreshed 5", got)
	}
}

func TestSendTranslatesSession(t *testing.T) {
	h := newHandler()
	_, path := startServer(t, h)
	c := dial(t, path)
	pin := route.Command{Op: route.OpPin, Session: sid("th"), Tier: config.TierHigh}
	if err := c.Send(pin); err != nil {
		t.Fatal(err)
	}
	if err := c.Send(route.Command{Op: route.OpPause, Scope: route.ScopeHome}); err != nil {
		t.Fatal(err)
	}
	got := h.appliedCmds()
	if len(got) != 2 || got[0].Session != "th" || got[0].Tier != config.TierHigh || got[0].ID == "" {
		t.Fatalf("applied pin = %+v, want thread th with an id", got)
	}
	if got[1].Op != route.OpPause || got[1].Session != "" || got[1].Scope != route.ScopeHome {
		t.Fatalf("applied pause = %+v", got[1])
	}
}

func TestRepliesCarryTheCommandID(t *testing.T) {
	_, path := startServer(t, newHandler())
	r := rawDial(t, path)
	r.hello(t)
	r.send(t, `{"op":"pin","session":"`+sid("th")+`","tier":"high","id":"x"}`+"\n"+
		`{"op":"pin","session":"`+sid("th")+`","tier":"bad","id":"y"}`)
	first, second := r.readReply(t), r.readReply(t)
	if first.Type != MessageAck || first.ID != "x" {
		t.Errorf("first reply = %+v, want ack x", first)
	}
	if second.Type != MessageError || second.ID != "y" || second.Error == "" {
		t.Errorf("second reply = %+v, want error y", second)
	}
}

func TestSendMatchesAckByIDWithTwoInFlight(t *testing.T) {
	h := newHandler()
	entered, release := make(chan struct{}), make(chan struct{})
	h.onApply = func(c route.Command) {
		if c.Tier == config.TierHigh {
			close(entered)
			<-release
		}
	}
	_, path := startServer(t, h)
	c := dial(t, path)

	slow := make(chan error, 1)
	go func() { slow <- c.Send(route.Command{Op: route.OpPin, Session: sid("th"), Tier: config.TierHigh}) }()
	<-entered
	fast := make(chan error, 1)
	go func() { fast <- c.Send(route.Command{Op: route.OpPin, Session: sid("th"), Tier: "bad"}) }()
	time.Sleep(20 * time.Millisecond) // let the second command reach the wire
	close(release)

	if err := <-slow; err != nil {
		t.Errorf("first Send = %v, want its own ack", err)
	}
	if err := <-fast; err == nil || !strings.Contains(err.Error(), "unknown tier") {
		t.Errorf("second Send = %v, want its own error", err)
	}
}

func TestSnapshotOp(t *testing.T) {
	h := newHandler()
	h.replies["th"] = 1
	_, path := startServer(t, h)
	c := subscribed(t, path, "th")
	h.set(func(h *fakeHandler) { h.replies["th"] = 9 })
	if err := c.Send(route.Command{Op: route.OpSnapshot}); err != nil {
		t.Fatal(err)
	}
	if got := next(t, c).Stats.Replies; got != 9 {
		t.Fatalf("replies = %d, want 9", got)
	}
	if len(h.appliedCmds()) != 0 {
		t.Fatal("snapshot op reached the handler")
	}

	idle := dial(t, path)
	if err := idle.Send(route.Command{Op: route.OpSnapshot}); err != nil {
		t.Fatalf("snapshot op without a subscription: %v", err)
	}
	quiet(t, idle.Snapshots(), "snapshot without a subscription")
}

func TestSendErrors(t *testing.T) {
	tests := []struct {
		name    string
		cmd     route.Command
		handler error
		want    string
	}{
		{"unknown op", route.Command{Op: "explode"}, nil, `unknown op "explode"`},
		{"empty op", route.Command{}, nil, `unknown op ""`},
		{"hello again", route.Command{Op: OpHello}, nil, `unknown op "hello"`},
		{"foreign session", route.Command{Op: route.OpPause, Session: "otherhome/th"}, nil, `unknown session "otherhome/th"`},
		{"bare thread id", route.Command{Op: route.OpPause, Session: "th"}, nil, `unknown session "th"`},
		{"handler error", route.Command{Op: route.OpPin, Session: sid("th")}, errors.New("tier not in group"), "tier not in group"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHandler()
			h.err = tt.handler
			_, path := startServer(t, h)
			c := dial(t, path)
			err := c.Send(tt.cmd)
			if err == nil || err.Error() != tt.want {
				t.Fatalf("err = %v, want %q", err, tt.want)
			}
			if err := c.Send(route.Command{Op: route.OpSnapshot}); err != nil {
				t.Fatalf("connection unusable after error: %v", err)
			}
		})
	}
}

func TestMalformedLineGetsErrorMessage(t *testing.T) {
	_, path := startServer(t, newHandler())
	r := rawDial(t, path)
	r.hello(t)
	r.send(t, "{not json")
	if m := r.readReply(t); m.Type != MessageError || m.Error != "malformed command" {
		t.Fatalf("reply = %+v, want malformed command error", m)
	}
}

func TestSlowClientDoesNotBlockOthers(t *testing.T) {
	s, path := startServer(t, newHandler())
	slow := rawDial(t, path)
	slow.hello(t)
	slow.send(t, `{"op":"subscribe","session":"`+sid("th")+`","id":"s"}`)
	fast := subscribed(t, path, "th")

	big := route.Snapshot{ThreadID: "th"}
	for i := range 1000 {
		big.Recent = append(big.Recent, route.Decision{ThreadID: "th", Why: strings.Repeat("x", 100), Step: i})
	}
	const rounds = 5
	start := time.Now()
	for i := 1; i <= rounds; i++ {
		big.Seq, big.Stats.Replies = uint64(i), i
		s.Publish(big)
		got := next(t, fast)
		for got.Stats.Replies != i {
			got = next(t, fast)
		}
	}
	if d := time.Since(start); d >= writeTimeout {
		t.Fatalf("fast client took %v for %d pushes; the slow one held it up", d, rounds)
	}
}

func TestChannelsCloseAfterServerCloses(t *testing.T) {
	s, path := startServer(t, newHandler())
	c := subscribed(t, path, "th")
	_ = s.Close()
	closes(t, c.Snapshots(), "Snapshots")
	closes(t, c.Sessions(), "Sessions")
}

func TestSendWhileDisconnected(t *testing.T) {
	s, path := startServer(t, newHandler())
	c := dial(t, path)
	_ = s.Close()
	deadline := time.Now().Add(3 * time.Second)
	for {
		err := c.Send(route.Command{Op: route.OpPause})
		if errors.Is(err, ErrDisconnected) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("Send error = %v, want ErrDisconnected", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := c.Subscribe(sid("th")); !errors.Is(err, ErrDisconnected) {
		t.Fatalf("Subscribe error = %v, want ErrDisconnected", err)
	}
}

func TestCloseClosesChannels(t *testing.T) {
	_, path := startServer(t, newHandler())
	c := dial(t, path)
	_ = c.Close()
	closes(t, c.Snapshots(), "Snapshots")
	closes(t, c.Sessions(), "Sessions")
}
