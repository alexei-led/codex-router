package appserver

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
)

const waitFor = 2 * time.Second

type rpcMsg struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      *int64          `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
	Error   *RPCError       `json:"error"`
}

type transport struct {
	name   string
	dialer Dialer
	serve  func(ln net.Listener, accept func(Conn)) (stop func())
}

var transports = []transport{
	{name: "websocket", dialer: WebSocket(), serve: serveWebSocket},
	{name: "json-lines", dialer: JSONLines(), serve: serveLines},
}

func serveWebSocket(ln net.Listener, accept func(Conn)) func() {
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		accept(newWSConn(ws))
	})}
	go func() { _ = srv.Serve(ln) }()
	return func() { _ = srv.Close() }
}

func serveLines(ln net.Listener, accept func(Conn)) func() {
	go func() {
		for {
			nc, err := ln.Accept()
			if err != nil {
				return
			}
			go accept(newLinesConn(nc))
		}
	}()
	return func() { _ = ln.Close() }
}

// fakeServer scripts the app-server side from the test goroutine: inbound
// messages arrive on in, replies go out through conn.
type fakeServer struct {
	t      *testing.T
	path   string
	conn   Conn
	in     chan rpcMsg
	accept func()
}

func socketPath(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "appsrv")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return filepath.Join(dir, "app.sock")
}

func startServer(t *testing.T, tr transport) *fakeServer {
	t.Helper()
	path := socketPath(t)
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	s := &fakeServer{t: t, path: path, in: make(chan rpcMsg, 16)}
	conns := make(chan Conn, 1)
	stop := tr.serve(ln, func(c Conn) {
		conns <- c
		defer close(s.in)
		for {
			b, err := c.Read(context.Background())
			if err != nil {
				return
			}
			var m rpcMsg
			if err := json.Unmarshal(b, &m); err != nil {
				t.Errorf("server got invalid JSON: %v", err)
				return
			}
			s.in <- m
		}
	})
	t.Cleanup(stop)
	s.accept = func() {
		select {
		case s.conn = <-conns:
		case <-time.After(waitFor):
			t.Fatal("client never connected")
		}
	}
	return s
}

func (s *fakeServer) expect(method string) rpcMsg {
	s.t.Helper()
	select {
	case m, ok := <-s.in:
		if !ok {
			s.t.Fatalf("connection closed while waiting for %s", method)
		}
		if m.Method != method {
			s.t.Fatalf("got method %q, want %q", m.Method, method)
		}
		if m.JSONRPC != "2.0" {
			s.t.Fatalf("jsonrpc = %q, want 2.0", m.JSONRPC)
		}
		return m
	case <-time.After(waitFor):
		s.t.Fatalf("timed out waiting for %s", method)
	}
	return rpcMsg{}
}

func (s *fakeServer) send(v any) {
	s.t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		s.t.Fatal(err)
	}
	if err := s.conn.Write(context.Background(), b); err != nil {
		s.t.Fatalf("server write: %v", err)
	}
}

func (s *fakeServer) reply(id *int64, result any) {
	s.t.Helper()
	s.send(map[string]any{"id": *id, "result": result})
}

func (s *fakeServer) fail(id *int64, code int, msg string) {
	s.t.Helper()
	s.send(map[string]any{"id": *id, "error": map[string]any{"code": code, "message": msg}})
}

func (s *fakeServer) notifySettings(thread, model, effort string) {
	s.t.Helper()
	s.send(map[string]any{"method": MethodSettingsUpdated, "params": map[string]any{
		"threadId":       thread,
		"threadSettings": map[string]any{"model": model, "effort": effort, "modelProvider": "router"},
	}})
}

func startDeafServer(t *testing.T, tr transport) string {
	t.Helper()
	path := socketPath(t)
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	release := make(chan struct{})
	stop := tr.serve(ln, func(c Conn) {
		<-release
		_ = c.Close()
	})
	t.Cleanup(func() {
		close(release)
		stop()
	})
	return path
}

func dial(t *testing.T, path string, d Dialer) API {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), waitFor)
	defer cancel()
	api, err := Dial(ctx, path, d)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	t.Cleanup(func() { _ = api.Close() })
	return api
}

func connect(t *testing.T, tr transport) (API, *fakeServer) {
	t.Helper()
	srv := startServer(t, tr)
	api := dial(t, srv.path, tr.dialer)
	srv.accept()
	return api, srv
}

// stuckConn is a Conn whose writes never complete; reads come from in.
type stuckConn struct {
	in        chan []byte
	writing   chan struct{}
	closed    chan struct{}
	closeOnce sync.Once
}

func newStuckConn() *stuckConn {
	return &stuckConn{in: make(chan []byte, 4), writing: make(chan struct{}, 4), closed: make(chan struct{})}
}

func (c *stuckConn) Read(ctx context.Context) ([]byte, error) {
	select {
	case b := <-c.in:
		return b, nil
	case <-c.closed:
		return nil, net.ErrClosed
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (c *stuckConn) Write(ctx context.Context, _ []byte) error {
	c.writing <- struct{}{}
	select {
	case <-c.closed:
		return net.ErrClosed
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *stuckConn) Close() error {
	c.closeOnce.Do(func() { close(c.closed) })
	return nil
}

// signalingConn reports on writing each time a write begins.
type signalingConn struct {
	Conn
	writing chan<- struct{}
}

func (c signalingConn) Write(ctx context.Context, msg []byte) error {
	c.writing <- struct{}{}
	return c.Conn.Write(ctx, msg)
}

type dialerFunc func(ctx context.Context, socketPath string) (Conn, error)

func (f dialerFunc) Dial(ctx context.Context, socketPath string) (Conn, error) {
	return f(ctx, socketPath)
}

func dialStuck(t *testing.T) (API, *stuckConn) {
	t.Helper()
	conn := newStuckConn()
	api := dial(t, "unused", dialerFunc(func(context.Context, string) (Conn, error) { return conn, nil }))
	return api, conn
}

func awaitWrite(t *testing.T, writing <-chan struct{}) {
	t.Helper()
	select {
	case <-writing:
	case <-time.After(waitFor):
		t.Fatal("no write started")
	}
}

func async(f func() error) <-chan error {
	errc := make(chan error, 1)
	go func() { errc <- f() }()
	return errc
}

func await(t *testing.T, errc <-chan error) error {
	t.Helper()
	select {
	case err := <-errc:
		return err
	case <-time.After(waitFor):
		t.Fatal("call did not return")
	}
	return nil
}

func expectUpdatesClosed(t *testing.T, api API) {
	t.Helper()
	select {
	case _, ok := <-api.Updates():
		if ok {
			t.Error("unexpected update")
		}
	case <-time.After(waitFor):
		t.Error("Updates not closed")
	}
}

func nextUpdate(t *testing.T, api API) SettingsUpdated {
	t.Helper()
	select {
	case u, ok := <-api.Updates():
		if !ok {
			t.Fatal("Updates closed")
		}
		return u
	case <-time.After(waitFor):
		t.Fatal("no update")
	}
	return SettingsUpdated{}
}

func each(t *testing.T, test func(t *testing.T, tr transport)) {
	for _, tr := range transports {
		t.Run(tr.name, func(t *testing.T) { test(t, tr) })
	}
}

func TestInitializeSendsExperimentalAPIThenInitialized(t *testing.T) {
	each(t, func(t *testing.T, tr transport) {
		api, srv := connect(t, tr)
		errc := async(func() error { return api.Initialize(context.Background()) })

		req := srv.expect(MethodInitialize)
		var params struct {
			ClientInfo   struct{ Name, Version string } `json:"clientInfo"`
			Capabilities struct {
				ExperimentalAPI bool `json:"experimentalApi"`
			} `json:"capabilities"`
		}
		if err := json.Unmarshal(req.Params, &params); err != nil {
			t.Fatal(err)
		}
		if !params.Capabilities.ExperimentalAPI {
			t.Error("experimentalApi not set")
		}
		if params.ClientInfo.Name == "" || params.ClientInfo.Version == "" {
			t.Errorf("clientInfo incomplete: %+v", params.ClientInfo)
		}
		srv.reply(req.ID, map[string]any{"userAgent": "codex/0.162.1", "codexHome": "/x", "platformFamily": "unix", "platformOs": "macos"})

		note := srv.expect(MethodInitialized)
		if note.ID != nil {
			t.Error("initialized must be a notification without id")
		}
		if err := await(t, errc); err != nil {
			t.Fatalf("Initialize: %v", err)
		}
	})
}

func TestInitializeReturnsRPCError(t *testing.T) {
	each(t, func(t *testing.T, tr transport) {
		api, srv := connect(t, tr)
		errc := async(func() error { return api.Initialize(context.Background()) })
		srv.fail(srv.expect(MethodInitialize).ID, -32600, "Already initialized")

		var rpcErr *RPCError
		if err := await(t, errc); !errors.As(err, &rpcErr) || rpcErr.Code != -32600 {
			t.Fatalf("err = %v, want RPCError -32600", err)
		}
	})
}

func TestUpdateSettings(t *testing.T) {
	cases := []struct {
		name     string
		settings Settings
		wantJSON string
		code     int
	}{
		{"model and effort", Settings{Model: "gpt-6-luna", Effort: "low"}, `{"threadId":"t1","model":"gpt-6-luna","effort":"low"}`, 0},
		{"model only", Settings{Model: "gpt-6-sol"}, `{"threadId":"t1","model":"gpt-6-sol"}`, 0},
		{"rpc error", Settings{Model: "gpt-6-sol", Effort: "high"}, `{"threadId":"t1","model":"gpt-6-sol","effort":"high"}`, -32602},
	}
	each(t, func(t *testing.T, tr transport) {
		api, srv := connect(t, tr)
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				errc := async(func() error { return api.UpdateSettings(context.Background(), "t1", tc.settings) })
				req := srv.expect(MethodSettingsUpdate)
				if string(req.Params) != tc.wantJSON {
					t.Errorf("params = %s, want %s", req.Params, tc.wantJSON)
				}
				if tc.code != 0 {
					srv.fail(req.ID, tc.code, "thread not found")
				} else {
					srv.reply(req.ID, map[string]any{})
				}

				err := await(t, errc)
				var rpcErr *RPCError
				switch {
				case tc.code == 0 && err != nil:
					t.Fatalf("UpdateSettings: %v", err)
				case tc.code != 0 && (!errors.As(err, &rpcErr) || rpcErr.Code != tc.code || rpcErr.Message != "thread not found"):
					t.Fatalf("err = %v, want RPCError %d", err, tc.code)
				}
			})
		}
	})
}

func TestUpdatesYieldsOnlySettingsUpdated(t *testing.T) {
	each(t, func(t *testing.T, tr transport) {
		api, srv := connect(t, tr)
		srv.send(map[string]any{"method": "turn/started", "params": map[string]any{"threadId": "t1"}})
		srv.send(map[string]any{"method": MethodSettingsUpdated, "params": map[string]any{"bogus": true}})
		srv.notifySettings("t1", "gpt-6-luna", "low")
		srv.notifySettings("t2", "gpt-6-sol", "high")

		want := []SettingsUpdated{
			{ThreadID: "t1", Settings: Settings{Model: "gpt-6-luna", Effort: "low"}},
			{ThreadID: "t2", Settings: Settings{Model: "gpt-6-sol", Effort: "high"}},
		}
		for _, w := range want {
			if got := nextUpdate(t, api); got != w {
				t.Errorf("update = %+v, want %+v", got, w)
			}
		}
	})
}

func TestConcurrentCallsAreMatchedByID(t *testing.T) {
	each(t, func(t *testing.T, tr transport) {
		api, srv := connect(t, tr)
		first := async(func() error {
			return api.UpdateSettings(context.Background(), "a", Settings{Model: "gpt-6-luna"})
		})
		reqA := srv.expect(MethodSettingsUpdate)
		second := async(func() error {
			return api.UpdateSettings(context.Background(), "b", Settings{Model: "gpt-6-sol"})
		})
		reqB := srv.expect(MethodSettingsUpdate)
		if *reqA.ID == *reqB.ID {
			t.Fatalf("both requests used id %d", *reqA.ID)
		}

		srv.fail(reqB.ID, -32000, "busy")
		srv.notifySettings("a", "gpt-6-luna", "medium")
		srv.reply(reqA.ID, map[string]any{})

		if err := await(t, first); err != nil {
			t.Errorf("first call: %v", err)
		}
		var rpcErr *RPCError
		if err := await(t, second); !errors.As(err, &rpcErr) || rpcErr.Message != "busy" {
			t.Errorf("second call err = %v, want busy", err)
		}
		if got := nextUpdate(t, api); got.ThreadID != "a" {
			t.Errorf("update thread = %q, want a", got.ThreadID)
		}
	})
}

func TestServerRequestIsRejected(t *testing.T) {
	each(t, func(t *testing.T, tr transport) {
		_, srv := connect(t, tr)
		srv.send(map[string]any{"id": 7, "method": "item/commandExecution/requestApproval", "params": map[string]any{}})

		select {
		case m := <-srv.in:
			if m.ID == nil || *m.ID != 7 || m.Error == nil || m.Error.Code != codeMethodNotFound {
				t.Fatalf("reply = %+v, want method-not-found for id 7", m)
			}
		case <-time.After(waitFor):
			t.Fatal("server request never answered")
		}
	})
}

func TestCancelledCallLeavesConnectionUsable(t *testing.T) {
	each(t, func(t *testing.T, tr transport) {
		api, srv := connect(t, tr)
		ctx, cancel := context.WithCancel(context.Background())
		errc := async(func() error { return api.UpdateSettings(ctx, "t1", Settings{Model: "m"}) })
		abandoned := srv.expect(MethodSettingsUpdate)
		cancel()
		if err := await(t, errc); !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
		srv.reply(abandoned.ID, map[string]any{})

		errc = async(func() error { return api.UpdateSettings(context.Background(), "t1", Settings{Model: "m"}) })
		srv.reply(srv.expect(MethodSettingsUpdate).ID, map[string]any{})
		if err := await(t, errc); err != nil {
			t.Fatalf("call after cancel: %v", err)
		}
	})
}

func TestAlreadyCancelledCallSendsNothing(t *testing.T) {
	each(t, func(t *testing.T, tr transport) {
		api, srv := connect(t, tr)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if err := api.UpdateSettings(ctx, "t1", Settings{Model: "m"}); !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
		select {
		case m := <-srv.in:
			t.Fatalf("server received %q", m.Method)
		case <-time.After(50 * time.Millisecond):
		}
	})
}

func TestCloseFailsPendingCallsAndClosesUpdates(t *testing.T) {
	each(t, func(t *testing.T, tr transport) {
		api, srv := connect(t, tr)
		errc := async(func() error { return api.UpdateSettings(context.Background(), "t1", Settings{Model: "m"}) })
		srv.expect(MethodSettingsUpdate)

		closed := async(api.Close)
		if err := await(t, errc); !errors.Is(err, ErrClosed) {
			t.Errorf("pending err = %v, want ErrClosed", err)
		}
		if err := await(t, closed); err != nil {
			t.Errorf("Close: %v", err)
		}
		if _, ok := <-api.Updates(); ok {
			t.Error("Updates still open after Close")
		}
		if err := api.UpdateSettings(context.Background(), "t1", Settings{Model: "m"}); !errors.Is(err, ErrClosed) {
			t.Errorf("call after Close err = %v, want ErrClosed", err)
		}
		if err := api.Close(); err != nil {
			t.Errorf("second Close: %v", err)
		}
	})
}

func TestServerDisconnectFailsPendingCalls(t *testing.T) {
	each(t, func(t *testing.T, tr transport) {
		api, srv := connect(t, tr)
		errc := async(func() error { return api.UpdateSettings(context.Background(), "t1", Settings{Model: "m"}) })
		srv.expect(MethodSettingsUpdate)
		_ = srv.conn.Close()

		if err := await(t, errc); !errors.Is(err, ErrClosed) {
			t.Errorf("pending err = %v, want ErrClosed", err)
		}
		expectUpdatesClosed(t, api)
	})
}

func TestCancelDuringStuckWriteClosesConnection(t *testing.T) {
	each(t, func(t *testing.T, tr transport) {
		writing := make(chan struct{}, 1)
		d := dialerFunc(func(ctx context.Context, path string) (Conn, error) {
			c, err := tr.dialer.Dial(ctx, path)
			return signalingConn{Conn: c, writing: writing}, err
		})
		api := dial(t, startDeafServer(t, tr), d)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		beyondSocketBuffers := strings.Repeat("x", 4<<20)

		errc := async(func() error { return api.UpdateSettings(ctx, beyondSocketBuffers, Settings{Model: "m"}) })
		awaitWrite(t, writing)
		cancel()
		if err := await(t, errc); !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
		expectUpdatesClosed(t, api)
	})
}

func TestCallWaitingBehindStuckWriteHonoursDeadline(t *testing.T) {
	api, conn := dialStuck(t)
	stuck := async(func() error { return api.UpdateSettings(context.Background(), "a", Settings{Model: "m"}) })
	awaitWrite(t, conn.writing)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := api.UpdateSettings(ctx, "b", Settings{Model: "m"}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("waiting call err = %v, want context.DeadlineExceeded", err)
	}

	closed := async(api.Close)
	if err := await(t, stuck); !errors.Is(err, ErrClosed) {
		t.Errorf("stuck call err = %v, want ErrClosed", err)
	}
	if err := await(t, closed); err != nil {
		t.Errorf("Close: %v", err)
	}
}

func TestServerRequestDuringStuckWriteDoesNotBlockUpdates(t *testing.T) {
	api, conn := dialStuck(t)
	stuck := async(func() error { return api.UpdateSettings(context.Background(), "a", Settings{Model: "m"}) })
	awaitWrite(t, conn.writing)

	conn.in <- []byte(`{"id":7,"method":"item/commandExecution/requestApproval","params":{}}`)
	conn.in <- []byte(`{"method":"thread/settings/updated","params":{"threadId":"t1","threadSettings":{"model":"gpt-6-luna","effort":"low"}}}`)
	if got := nextUpdate(t, api); got.ThreadID != "t1" {
		t.Errorf("update thread = %q, want t1", got.ThreadID)
	}

	if err := await(t, async(api.Close)); err != nil {
		t.Errorf("Close: %v", err)
	}
	if err := await(t, stuck); !errors.Is(err, ErrClosed) {
		t.Errorf("stuck call err = %v, want ErrClosed", err)
	}
}

func TestDialMissingSocketFails(t *testing.T) {
	each(t, func(t *testing.T, tr transport) {
		ctx, cancel := context.WithTimeout(context.Background(), waitFor)
		defer cancel()
		if _, err := Dial(ctx, socketPath(t), tr.dialer); err == nil {
			t.Fatal("Dial to a missing socket succeeded")
		}
	})
}
