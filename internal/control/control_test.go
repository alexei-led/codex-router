package control

import (
	"bufio"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/alexei-led/codex-router/internal/config"
	"github.com/alexei-led/codex-router/internal/route"
)

type fakeHandler struct {
	mu      sync.Mutex
	replies int
	applied []route.Command
	err     error
}

func (h *fakeHandler) Apply(c route.Command) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.err != nil {
		return h.err
	}
	h.applied = append(h.applied, c)
	return nil
}

func (h *fakeHandler) Snapshot() route.Snapshot {
	h.mu.Lock()
	defer h.mu.Unlock()
	return route.Snapshot{Stats: route.Stats{Replies: h.replies}}
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

func next(t *testing.T, c *Client) route.Snapshot {
	t.Helper()
	select {
	case s, ok := <-c.Updates():
		if !ok {
			t.Fatal("updates closed")
		}
		return s
	case <-time.After(3 * time.Second):
		t.Fatal("no snapshot")
		return route.Snapshot{}
	}
}

func TestListenSocketMode(t *testing.T) {
	old := setUmask(0)
	defer setUmask(old)
	_, path := startServer(t, &fakeHandler{})
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := fi.Mode().Perm(); got != SocketMode {
		t.Fatalf("mode = %o, want %o", got, SocketMode)
	}
}

func TestListenRejectsLiveSocket(t *testing.T) {
	_, path := startServer(t, &fakeHandler{})
	if s, err := Listen(path, &fakeHandler{}); err == nil {
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
	s, err := Listen(path, &fakeHandler{})
	if err != nil {
		t.Fatalf("Listen over stale socket: %v", err)
	}
	_ = s.Close()
}

func TestCloseRemovesSocket(t *testing.T) {
	s, path := startServer(t, &fakeHandler{})
	_ = s.Close()
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("socket still present: %v", err)
	}
}

func TestSnapshotOnConnect(t *testing.T) {
	_, path := startServer(t, &fakeHandler{replies: 7})
	c := dial(t, path)
	if got := next(t, c).Stats.Replies; got != 7 {
		t.Fatalf("replies = %d, want 7", got)
	}
}

func TestPublishReachesAllClients(t *testing.T) {
	s, path := startServer(t, &fakeHandler{})
	a, b := dial(t, path), dial(t, path)
	next(t, a)
	next(t, b)
	s.Publish(route.Snapshot{Stats: route.Stats{Replies: 3}})
	for _, c := range []*Client{a, b} {
		if got := next(t, c).Stats.Replies; got != 3 {
			t.Fatalf("replies = %d, want 3", got)
		}
	}
}

func TestPublishCoalescesBursts(t *testing.T) {
	s, path := startServer(t, &fakeHandler{})
	c := dial(t, path)
	next(t, c)
	const burst = 100
	for i := 1; i <= burst; i++ {
		s.Publish(route.Snapshot{Stats: route.Stats{Replies: i}})
	}
	got, last := 0, 0
	deadline := time.After(3 * time.Second)
	for last != burst {
		select {
		case snap := <-c.Updates():
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

func TestSendRoundTrip(t *testing.T) {
	h := &fakeHandler{}
	_, path := startServer(t, h)
	c := dial(t, path)
	next(t, c)
	pin := route.Command{Op: route.OpPin, Tier: config.Tier("high")}
	if err := c.Send(pin); err != nil {
		t.Fatal(err)
	}
	if err := c.Send(route.Command{Op: route.OpPause}); err != nil {
		t.Fatal(err)
	}
	got := h.appliedCmds()
	if len(got) != 2 || got[0] != pin || got[1].Op != route.OpPause {
		t.Fatalf("applied = %+v", got)
	}
}

func TestSendSnapshotOpReturnsState(t *testing.T) {
	h := &fakeHandler{replies: 1}
	_, path := startServer(t, h)
	c := dial(t, path)
	next(t, c)
	h.mu.Lock()
	h.replies = 9
	h.mu.Unlock()
	if err := c.Send(route.Command{Op: route.OpSnapshot}); err != nil {
		t.Fatal(err)
	}
	if got := next(t, c).Stats.Replies; got != 9 {
		t.Fatalf("replies = %d, want 9", got)
	}
	if len(h.appliedCmds()) != 0 {
		t.Fatal("snapshot op reached the handler")
	}
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
		{"handler error", route.Command{Op: route.OpPin}, errors.New("tier not in group"), "tier not in group"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, path := startServer(t, &fakeHandler{err: tt.handler})
			c := dial(t, path)
			next(t, c)
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
	_, path := startServer(t, &fakeHandler{})
	conn, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	r := bufio.NewReader(conn)
	if _, err := r.ReadBytes('\n'); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Write([]byte("{not json\n")); err != nil {
		t.Fatal(err)
	}
	line, err := r.ReadBytes('\n')
	if err != nil {
		t.Fatal(err)
	}
	var m Message
	if err := json.Unmarshal(line, &m); err != nil {
		t.Fatal(err)
	}
	if m.Type != MessageError {
		t.Fatalf("type = %q, want error", m.Type)
	}
}

func TestClientReconnectsAfterServerRestart(t *testing.T) {
	path := sockPath(t)
	s1, err := Listen(path, &fakeHandler{replies: 1})
	if err != nil {
		t.Fatal(err)
	}
	c := dial(t, path)
	if got := next(t, c).Stats.Replies; got != 1 {
		t.Fatalf("replies = %d, want 1", got)
	}
	_ = s1.Close()
	s2, err := Listen(path, &fakeHandler{replies: 2})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s2.Close() }()
	if got := next(t, c).Stats.Replies; got != 2 {
		t.Fatalf("replies after reconnect = %d, want 2", got)
	}
	if err := c.Send(route.Command{Op: route.OpPause}); err != nil {
		t.Fatalf("Send after reconnect: %v", err)
	}
}

func TestSendWhileDisconnected(t *testing.T) {
	s, path := startServer(t, &fakeHandler{})
	c := dial(t, path)
	next(t, c)
	_ = s.Close()
	deadline := time.Now().Add(3 * time.Second)
	for {
		err := c.Send(route.Command{Op: route.OpPause})
		if errors.Is(err, ErrDisconnected) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("Send error = %v, want ErrDisconnected", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestDialMissingSocketFails(t *testing.T) {
	if c, err := Dial(sockPath(t)); err == nil {
		_ = c.Close()
		t.Fatal("Dial to a missing socket succeeded")
	}
}

func TestCloseClosesUpdates(t *testing.T) {
	_, path := startServer(t, &fakeHandler{})
	c := dial(t, path)
	_ = c.Close()
	deadline := time.After(3 * time.Second)
	for {
		select {
		case _, ok := <-c.Updates():
			if !ok {
				return
			}
		case <-deadline:
			t.Fatal("Updates not closed")
		}
	}
}
