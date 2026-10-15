package appserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
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
	api, err := Dial(ctx, path, d, nil)
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

func TestResume(t *testing.T) {
	cases := []struct {
		name string
		code int
	}{
		{"resumed", 0},
		{"no rollout", -32600},
	}
	each(t, func(t *testing.T, tr transport) {
		api, srv := connect(t, tr)
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				var got Resumed
				errc := async(func() (err error) {
					got, err = api.Resume(context.Background(), "t1")
					return err
				})
				req := srv.expect(MethodResume)
				if want := `{"threadId":"t1","excludeTurns":true}`; string(req.Params) != want {
					t.Errorf("params = %s, want %s", req.Params, want)
				}
				if tc.code != 0 {
					srv.fail(req.ID, tc.code, "no rollout found")
				} else {
					srv.reply(req.ID, map[string]any{
						"thread": map[string]any{"id": "t1"}, "model": "gpt-6-sol", "reasoningEffort": "low", "cwd": "/w",
					})
				}

				err := await(t, errc)
				var rpcErr *RPCError
				switch {
				case tc.code == 0 && err != nil:
					t.Fatalf("Resume: %v", err)
				case tc.code != 0 && (!errors.As(err, &rpcErr) || rpcErr.Code != tc.code):
					t.Fatalf("err = %v, want RPCError %d", err, tc.code)
				}
				want := Resumed{Settings: Settings{Model: "gpt-6-sol", Effort: "low"}, Cwd: "/w"}
				if tc.code != 0 {
					want = Resumed{}
				}
				if got != want {
					t.Errorf("Resume = %+v, want %+v", got, want)
				}
			})
		}
	})
}

func TestSettingsUpdatedWithNullEffort(t *testing.T) {
	each(t, func(t *testing.T, tr transport) {
		api, srv := connect(t, tr)
		srv.send(map[string]any{"method": MethodSettingsUpdated, "params": map[string]any{
			"threadId":       "t1",
			"threadSettings": map[string]any{"model": "gpt-6-sol", "effort": nil, "cwd": "/w", "modelProvider": "router"},
		}})

		want := SettingsUpdated{ThreadID: "t1", Settings: Settings{Model: "gpt-6-sol"}}
		if got := nextUpdate(t, api); got != want {
			t.Errorf("update = %+v, want %+v", got, want)
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

func TestServerRequestIsLeftToTheTUI(t *testing.T) {
	each(t, func(t *testing.T, tr transport) {
		api, srv := connect(t, tr)
		srv.send(map[string]any{"id": 7, "method": "item/commandExecution/requestApproval", "params": map[string]any{}})

		errc := async(func() error { return api.UpdateSettings(context.Background(), "t1", Settings{Model: "m"}) })
		req := srv.expect(MethodSettingsUpdate)
		srv.reply(req.ID, map[string]any{})
		if err := await(t, errc); err != nil {
			t.Fatalf("call after a server request: %v", err)
		}
		errc = async(func() error { return api.UpdateSettings(context.Background(), "t2", Settings{Model: "m"}) })
		next := srv.expect(MethodSettingsUpdate)
		srv.reply(next.ID, map[string]any{})
		if err := await(t, errc); err != nil {
			t.Fatalf("second call: %v", err)
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
		errc := async(func() error { return api.UpdateSettings(context.Background(), "t2", Settings{Model: "m"}) })
		first := srv.expect(MethodSettingsUpdate)
		srv.reply(first.ID, map[string]any{})
		if err := await(t, errc); err != nil {
			t.Fatalf("call after the cancelled one: %v", err)
		}
		if !strings.Contains(string(first.Params), `"t2"`) {
			t.Errorf("first message the server got = %s, want the t2 call", first.Params)
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

func TestStuckWriteClosesConnectionAfterWriteTimeout(t *testing.T) {
	each(t, func(t *testing.T, tr transport) {
		api := dial(t, startDeafServer(t, tr), tr.dialer)
		api.(*Client).writeTimeout = 50 * time.Millisecond
		beyondSocketBuffers := strings.Repeat("x", 4<<20)

		err := await(t, async(func() error {
			return api.UpdateSettings(context.Background(), beyondSocketBuffers, Settings{Model: "m"})
		}))
		if err == nil || errors.Is(err, ErrClosed) {
			t.Fatalf("err = %v, want a write error", err)
		}
		expectUpdatesClosed(t, api)
	})
}

type recordingConn struct {
	*stuckConn
	ctxs chan context.Context
}

func (c recordingConn) Write(ctx context.Context, msg []byte) error {
	c.ctxs <- ctx
	return c.stuckConn.Write(ctx, msg)
}

func TestCallerCancelDoesNotReachTheWrite(t *testing.T) {
	conn := recordingConn{stuckConn: newStuckConn(), ctxs: make(chan context.Context, 1)}
	api := dial(t, "unused", dialerFunc(func(context.Context, string) (Conn, error) { return conn, nil }))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errc := async(func() error { return api.UpdateSettings(ctx, "a", Settings{Model: "m"}) })
	var wctx context.Context
	select {
	case wctx = <-conn.ctxs:
	case <-time.After(waitFor):
		t.Fatal("no write started")
	}
	cancel()
	if err := wctx.Err(); err != nil {
		t.Fatalf("write context ended with the caller's: %v", err)
	}

	if err := await(t, async(api.Close)); err != nil {
		t.Errorf("Close: %v", err)
	}
	if err := await(t, errc); !errors.Is(err, ErrClosed) {
		t.Errorf("call err = %v, want ErrClosed", err)
	}
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
		if _, err := Dial(ctx, socketPath(t), tr.dialer, nil); err == nil {
			t.Fatal("Dial to a missing socket succeeded")
		}
	})
}

func TestClientLogsToItsLogger(t *testing.T) {
	conn := newStuckConn()
	var logged bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), waitFor)
	defer cancel()
	api, err := Dial(ctx, "unused", dialerFunc(func(context.Context, string) (Conn, error) { return conn, nil }),
		slog.New(slog.NewTextHandler(&logged, nil)))
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	conn.in <- []byte("not json")
	conn.in <- []byte(`{"method":"` + MethodSettingsUpdated + `","params":{"threadId":"t1","threadSettings":{"model":"m"}}}`)
	nextUpdate(t, api)
	_ = api.Close()

	if !strings.Contains(logged.String(), "msg=appserver-bad-message") {
		t.Errorf("log = %q, want appserver-bad-message", logged.String())
	}
}

func connectDaemon(t *testing.T, tr transport) (*Client, *fakeServer) {
	t.Helper()
	srv := startServer(t, tr)
	ctx, cancel := context.WithTimeout(context.Background(), waitFor)
	defer cancel()
	c, err := DialDaemon(ctx, srv.path, tr.dialer, nil)
	if err != nil {
		t.Fatalf("DialDaemon: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	srv.accept()
	return c, srv
}

func TestDaemonSocket(t *testing.T) {
	if got, want := DaemonSocket("/h/.codex"), "/h/.codex/app-server-control/app-server-control.sock"; got != want {
		t.Errorf("DaemonSocket = %q, want %q", got, want)
	}
}

func TestLoadedThreadsFollowsTheCursor(t *testing.T) {
	each(t, func(t *testing.T, tr transport) {
		c, srv := connectDaemon(t, tr)
		var got []string
		errc := async(func() (err error) {
			got, err = c.LoadedThreads(context.Background())
			return err
		})
		req := srv.expect(MethodLoadedList)
		if string(req.Params) != `{}` {
			t.Errorf("first page params = %s, want {}", req.Params)
		}
		srv.reply(req.ID, map[string]any{"data": []string{"a", "b"}, "nextCursor": "c1"})
		req = srv.expect(MethodLoadedList)
		if want := `{"cursor":"c1"}`; string(req.Params) != want {
			t.Errorf("second page params = %s, want %s", req.Params, want)
		}
		srv.reply(req.ID, map[string]any{"data": []string{"c"}, "nextCursor": nil})
		if err := await(t, errc); err != nil {
			t.Fatalf("LoadedThreads: %v", err)
		}
		if want := []string{"a", "b", "c"}; !slices.Equal(got, want) {
			t.Errorf("LoadedThreads = %q, want %q", got, want)
		}
	})
}

func TestReadThreadAndUnsubscribe(t *testing.T) {
	each(t, func(t *testing.T, tr transport) {
		c, srv := connectDaemon(t, tr)
		var th Thread
		errc := async(func() (err error) {
			th, err = c.ReadThread(context.Background(), "t1")
			return err
		})
		req := srv.expect(MethodRead)
		if want := `{"threadId":"t1","includeTurns":false}`; string(req.Params) != want {
			t.Errorf("read params = %s, want %s", req.Params, want)
		}
		srv.reply(req.ID, map[string]any{"thread": map[string]any{
			"id": "t1", "cwd": "/w1", "ephemeral": false, "source": "cli", "threadSource": "user", "parentThreadId": nil,
		}})
		if err := await(t, errc); err != nil {
			t.Fatalf("ReadThread: %v", err)
		}
		if th.ID != "t1" || th.Cwd != "/w1" || !th.Main() {
			t.Errorf("ReadThread = %+v (main %t), want t1 in /w1, main", th, th.Main())
		}

		errc = async(func() error { return c.Unsubscribe(context.Background(), "t1") })
		req = srv.expect(MethodUnsubscribe)
		if want := `{"threadId":"t1"}`; string(req.Params) != want {
			t.Errorf("unsubscribe params = %s, want %s", req.Params, want)
		}
		srv.reply(req.ID, map[string]any{"status": "unsubscribed"})
		if err := await(t, errc); err != nil {
			t.Fatalf("Unsubscribe: %v", err)
		}
	})
}

func TestThreadMain(t *testing.T) {
	parent := "p1"
	cases := []struct {
		name   string
		thread string
		want   bool
	}{
		{"user thread", `{"id":"t","source":"vscode","threadSource":"user"}`, true},
		{"no thread source", `{"id":"t","source":"cli","threadSource":null}`, true},
		{"ephemeral title thread", `{"id":"t","ephemeral":true,"source":"vscode","threadSource":"thread_title"}`, false},
		{"other thread source", `{"id":"t","source":"cli","threadSource":"guardian_review"}`, false},
		{"subagent source", `{"id":"t","source":{"subAgent":"review"},"threadSource":"user"}`, false},
		{"parent thread", `{"id":"t","source":"cli","parentThreadId":"` + parent + `"}`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var th Thread
			if err := json.Unmarshal([]byte(tc.thread), &th); err != nil {
				t.Fatal(err)
			}
			if got := th.Main(); got != tc.want {
				t.Errorf("Main = %t, want %t", got, tc.want)
			}
		})
	}
}

func nextThreadEvent(t *testing.T, c *Client) ThreadEvent {
	t.Helper()
	select {
	case ev, ok := <-c.Threads():
		if !ok {
			t.Fatal("Threads closed")
		}
		return ev
	case <-time.After(waitFor):
		t.Fatal("no thread event")
	}
	return ThreadEvent{}
}

func TestDaemonClientYieldsThreadEvents(t *testing.T) {
	each(t, func(t *testing.T, tr transport) {
		c, srv := connectDaemon(t, tr)
		srv.send(map[string]any{"method": MethodThreadStarted, "params": map[string]any{"thread": map[string]any{}}})
		srv.send(map[string]any{"method": MethodThreadStarted, "params": map[string]any{
			"thread": map[string]any{"id": "t1", "cwd": "/w1", "source": "cli", "threadSource": "user"},
		}})
		srv.send(map[string]any{"method": "thread/name/updated", "params": map[string]any{"threadId": "t1"}})
		srv.send(map[string]any{"method": MethodTurnStarted, "params": map[string]any{"threadId": "t1", "turn": map[string]any{}}})
		srv.send(map[string]any{"method": MethodTurnCompleted, "params": map[string]any{"threadId": "t1", "turn": map[string]any{}}})
		srv.send(map[string]any{"method": MethodThreadClosed, "params": map[string]any{"threadId": "t1"}})

		ev := nextThreadEvent(t, c)
		if ev.Method != MethodThreadStarted || ev.ThreadID != "t1" || ev.Thread == nil || ev.Thread.Cwd != "/w1" {
			t.Errorf("first event = %+v, want thread/started t1 in /w1", ev)
		}
		for _, want := range []string{MethodTurnStarted, MethodTurnCompleted, MethodThreadClosed} {
			if ev := nextThreadEvent(t, c); ev.Method != want || ev.ThreadID != "t1" || ev.Thread != nil {
				t.Errorf("event = %+v, want %s t1", ev, want)
			}
		}

		_ = srv.conn.Close()
		select {
		case _, ok := <-c.Threads():
			if ok {
				t.Error("unexpected thread event")
			}
		case <-time.After(waitFor):
			t.Error("Threads not closed after disconnect")
		}
	})
}

func TestPlainClientHasNoThreadEvents(t *testing.T) {
	each(t, func(t *testing.T, tr transport) {
		api, _ := connect(t, tr)
		if api.(*Client).Threads() != nil {
			t.Error("Dial client yields thread events; only DialDaemon should")
		}
	})
}
