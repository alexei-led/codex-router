package appserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"runtime/debug"
	"sync"
	"sync/atomic"
	"time"

	"github.com/alexei-led/codex-router/internal/config"
	"github.com/alexei-led/codex-router/internal/responses"
)

// Settings are the thread settings the router writes and reads.
type Settings struct {
	Model  string        `json:"model"`
	Effort config.Effort `json:"effort"`
}

// SettingsUpdated is a thread/settings/updated notification.
type SettingsUpdated struct {
	ThreadID string
	Settings Settings
}

// Resumed is what thread/resume returns: the thread's current settings and
// cwd. An empty Effort is the model's default effort, not a missing route.
type Resumed struct {
	Settings Settings
	Cwd      string
}

// Thread is the part of a Codex thread the brain reads.
type Thread struct {
	ID           string          `json:"id"`
	Cwd          string          `json:"cwd"`
	Ephemeral    bool            `json:"ephemeral"`
	ParentID     *string         `json:"parentThreadId"`
	Source       json.RawMessage `json:"source"`
	ThreadSource *string         `json:"threadSource"`
}

// Main reports whether t is a thread the user started: not ephemeral, not a
// subagent, and of the user's thread source or none. Side threads such as
// thread_title are ephemeral and of another source.
func (t Thread) Main() bool {
	if t.Ephemeral || t.ParentID != nil && *t.ParentID != "" {
		return false
	}
	var src struct {
		SubAgent json.RawMessage `json:"subAgent"`
	}
	// A plain source such as "cli" is a JSON string and does not decode here.
	if json.Unmarshal(t.Source, &src) == nil && len(src.SubAgent) > 0 {
		return false
	}
	return t.ThreadSource == nil || *t.ThreadSource == "" || *t.ThreadSource == responses.ThreadSourceUser
}

// ThreadEvent is a thread lifecycle or turn notification. Thread is set for
// thread/started only.
type ThreadEvent struct {
	Method   string
	ThreadID string
	Thread   *Thread
}

// Conn is one message-oriented connection to the app-server. Cancelling the
// ctx of a Read or Write may close the connection.
type Conn interface {
	Read(ctx context.Context) ([]byte, error)
	Write(ctx context.Context, msg []byte) error
	Close() error
}

// Dialer opens a Conn to the app-server socket.
type Dialer interface {
	Dial(ctx context.Context, socketPath string) (Conn, error)
}

// API is what the launcher uses; Client implements it.
type API interface {
	Initialize(ctx context.Context) error
	Resume(ctx context.Context, threadID string) (Resumed, error)
	UpdateSettings(ctx context.Context, threadID string, s Settings) error
	Updates() <-chan SettingsUpdated
	Close() error
}

// JSON-RPC method names used.
const (
	MethodInitialize      = "initialize"
	MethodInitialized     = "initialized"
	MethodResume          = "thread/resume"
	MethodSettingsUpdate  = "thread/settings/update"
	MethodSettingsUpdated = "thread/settings/updated"
	MethodLoadedList      = "thread/loaded/list"
	MethodRead            = "thread/read"
	MethodUnsubscribe     = "thread/unsubscribe"
	MethodThreadStarted   = "thread/started"
	MethodThreadClosed    = "thread/closed"
	MethodTurnStarted     = "turn/started"
	MethodTurnCompleted   = "turn/completed"
)

// DaemonSocket is the control socket of the shared daemon of the Codex home
// at codexHome. Codex makes it a symlink into a short per-user tmp dir.
func DaemonSocket(codexHome string) string {
	return filepath.Join(codexHome, "app-server-control", "app-server-control.sock")
}

const (
	clientName = "codex-router"
	// updatesBuffer absorbs bursts; the read loop blocks when it is full, so
	// consumers must drain Updates independently of their own RPC calls.
	updatesBuffer = 64
	// maxMessage bounds one inbound message; app-server item notifications
	// can carry large tool output.
	maxMessage = 64 << 20
	// writeTimeout bounds one message write: a server that reads nothing for
	// this long is wedged, and the failed write closes the connection so the
	// launcher sees Updates close.
	writeTimeout = 5 * time.Second
)

// ErrClosed is returned by calls made on, or pending at, a closed connection.
var ErrClosed = errors.New("appserver: connection closed")

// RPCError is a JSON-RPC error response from the app-server.
type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *RPCError) Error() string {
	return fmt.Sprintf("appserver: rpc error %d: %s", e.Code, e.Message)
}

// message is any inbound or outbound JSON-RPC 2.0 message. Codex omits the
// "jsonrpc" member and ignores it on input, so it is sent but never required.
type message struct {
	JSONRPC string          `json:"jsonrpc,omitempty"`
	ID      *int64          `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  any             `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
}

type inbound struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
	Result json.RawMessage `json:"result"`
	Error  *RPCError       `json:"error"`
}

type reply struct {
	result json.RawMessage
	err    error
}

// Client is a JSON-RPC client over one Conn. A caller's ctx that ends while
// its call waits for the write slot or the reply abandons only that call; it
// does not interrupt a write in progress. A write that does not finish within
// writeTimeout closes the connection, because a partly written message leaves
// the stream unusable.
type Client struct {
	conn         Conn
	log          *slog.Logger
	ctx          context.Context // lifetime: reads and writes
	cancel       context.CancelFunc
	nextID       atomic.Int64
	writeSem     chan struct{} // one writer at a time; waiting honours ctx
	writeTimeout time.Duration

	mu      sync.Mutex
	pending map[int64]chan reply

	updates    chan SettingsUpdated
	threads    chan ThreadEvent // nil unless DialDaemon
	done       chan struct{}
	connOnce   sync.Once
	connClosed atomic.Bool
	connErr    error
}

// Dial connects to the app-server socket through d and starts reading. The
// client logs to log; nil means slog.Default(). Call Initialize before any
// other request.
func Dial(ctx context.Context, socketPath string, d Dialer, log *slog.Logger) (API, error) {
	c, err := dialClient(ctx, socketPath, d, log, false)
	if err != nil {
		return nil, err
	}
	return c, nil
}

// DialDaemon is Dial for a home's shared daemon: the client also yields
// thread lifecycle and turn notifications on Threads.
func DialDaemon(ctx context.Context, socketPath string, d Dialer, log *slog.Logger) (*Client, error) {
	return dialClient(ctx, socketPath, d, log, true)
}

func dialClient(ctx context.Context, socketPath string, d Dialer, log *slog.Logger, threads bool) (*Client, error) {
	conn, err := d.Dial(ctx, socketPath)
	if err != nil {
		return nil, fmt.Errorf("appserver: dial: %w", err)
	}
	if log == nil {
		log = slog.Default()
	}
	lifetime, cancel := context.WithCancel(context.Background())
	c := &Client{
		conn:         conn,
		log:          log,
		ctx:          lifetime,
		cancel:       cancel,
		writeSem:     make(chan struct{}, 1),
		writeTimeout: writeTimeout,
		pending:      make(map[int64]chan reply),
		updates:      make(chan SettingsUpdated, updatesBuffer),
		done:         make(chan struct{}),
	}
	if threads {
		c.threads = make(chan ThreadEvent, updatesBuffer)
	}
	go c.readLoop()
	return c, nil
}

// Initialize performs the initialize handshake with experimentalApi enabled
// (thread/settings/update is experimental) and sends initialized.
func (c *Client) Initialize(ctx context.Context) error {
	params := map[string]any{
		"clientInfo":   map[string]string{"name": clientName, "version": clientVersion()},
		"capabilities": map[string]bool{"experimentalApi": true},
	}
	var result json.RawMessage
	if err := c.call(ctx, MethodInitialize, params, &result); err != nil {
		return err
	}
	return c.write(ctx, message{Method: MethodInitialized})
}

// Resume subscribes this client to a thread's turn and settings
// notifications with thread/resume {threadId, excludeTurns: true}; the
// app-server sends them only to clients that resumed the thread. A thread
// that cannot be resumed, such as one without a rollout (-32600 "no rollout
// found"), comes back as an *RPCError the caller may ignore: settings writes
// to it still work.
func (c *Client) Resume(ctx context.Context, threadID string) (Resumed, error) {
	params := struct {
		ThreadID     string `json:"threadId"`
		ExcludeTurns bool   `json:"excludeTurns"`
	}{threadID, true}
	var r struct {
		Model  string        `json:"model"`
		Effort config.Effort `json:"reasoningEffort"`
		Cwd    string        `json:"cwd"`
	}
	if err := c.call(ctx, MethodResume, params, &r); err != nil {
		return Resumed{}, err
	}
	return Resumed{Settings: Settings{Model: r.Model, Effort: r.Effort}, Cwd: r.Cwd}, nil
}

// LoadedThreads returns the ids of the threads the app-server holds in
// memory, following thread/loaded/list's cursor to the last page.
func (c *Client) LoadedThreads(ctx context.Context) ([]string, error) {
	var ids []string
	var cursor *string
	for {
		params := struct {
			Cursor *string `json:"cursor,omitempty"`
		}{cursor}
		var page struct {
			Data       []string `json:"data"`
			NextCursor *string  `json:"nextCursor"`
		}
		if err := c.call(ctx, MethodLoadedList, params, &page); err != nil {
			return nil, err
		}
		ids = append(ids, page.Data...)
		if page.NextCursor == nil || *page.NextCursor == "" {
			return ids, nil
		}
		cursor = page.NextCursor
	}
}

// ReadThread returns a thread's metadata without its turns.
func (c *Client) ReadThread(ctx context.Context, threadID string) (Thread, error) {
	params := struct {
		ThreadID     string `json:"threadId"`
		IncludeTurns bool   `json:"includeTurns"`
	}{threadID, false}
	var r struct {
		Thread Thread `json:"thread"`
	}
	if err := c.call(ctx, MethodRead, params, &r); err != nil {
		return Thread{}, err
	}
	return r.Thread, nil
}

// Unsubscribe ends this client's subscription to a thread. The daemon
// unloads an idle thread only once no client is subscribed.
func (c *Client) Unsubscribe(ctx context.Context, threadID string) error {
	params := struct {
		ThreadID string `json:"threadId"`
	}{threadID}
	return c.call(ctx, MethodUnsubscribe, params, nil)
}

// UpdateSettings sets the thread's model and effort for subsequent turns. An
// empty field is omitted, which leaves that setting unchanged.
func (c *Client) UpdateSettings(ctx context.Context, threadID string, s Settings) error {
	params := struct {
		ThreadID string        `json:"threadId"`
		Model    string        `json:"model,omitempty"`
		Effort   config.Effort `json:"effort,omitempty"`
	}{threadID, s.Model, s.Effort}
	return c.call(ctx, MethodSettingsUpdate, params, nil)
}

// Updates yields thread/settings/updated notifications in arrival order. It
// is closed when the connection ends.
func (c *Client) Updates() <-chan SettingsUpdated { return c.updates }

// Threads yields thread/started, thread/closed, turn/started and
// turn/completed in arrival order, and is closed when the connection ends.
// It is nil unless the client came from DialDaemon. Like Updates it must be
// drained independently of the consumer's own RPC calls.
func (c *Client) Threads() <-chan ThreadEvent { return c.threads }

// Close closes the connection, fails pending calls with ErrClosed and waits
// for the client's goroutines to stop. It returns the first error from
// closing the connection.
func (c *Client) Close() error {
	err := c.closeConn()
	c.cancel()
	<-c.done
	return err
}

// closeConn closes the connection once, whether from Close or a failed write.
func (c *Client) closeConn() error {
	c.connOnce.Do(func() {
		c.connClosed.Store(true)
		c.connErr = c.conn.Close()
	})
	return c.connErr
}

func (c *Client) call(ctx context.Context, method string, params, result any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	id := c.nextID.Add(1)
	ch := make(chan reply, 1)
	c.mu.Lock()
	if c.pending == nil {
		c.mu.Unlock()
		return ErrClosed
	}
	c.pending[id] = ch
	c.mu.Unlock()

	if err := c.write(ctx, message{ID: &id, Method: method, Params: params}); err != nil {
		c.forget(id)
		return err
	}
	select {
	case r := <-ch:
		if r.err != nil {
			return r.err
		}
		if result == nil {
			return nil
		}
		if err := json.Unmarshal(r.result, result); err != nil {
			return fmt.Errorf("appserver: %s result: %w", method, err)
		}
		return nil
	case <-ctx.Done():
		c.forget(id)
		return ctx.Err()
	}
}

func (c *Client) forget(id int64) {
	c.mu.Lock()
	delete(c.pending, id)
	c.mu.Unlock()
}

func (c *Client) write(ctx context.Context, m message) error {
	m.JSONRPC = "2.0"
	b, err := json.Marshal(m)
	if err != nil {
		return fmt.Errorf("appserver: encode %s: %w", m.Method, err)
	}
	select {
	case c.writeSem <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	case <-c.ctx.Done():
		return ErrClosed
	}
	defer func() { <-c.writeSem }()
	// The write gets its own deadline instead of ctx: coder/websocket closes
	// the connection when a write's context ends, even just after the message
	// went out, so a caller's cancel would cost every later call.
	wctx, cancel := context.WithTimeout(c.ctx, c.writeTimeout)
	defer cancel()
	err = c.conn.Write(wctx, b)
	switch {
	case err == nil:
		return nil
	case c.connClosed.Load():
		return ErrClosed
	default:
		_ = c.closeConn()
		return fmt.Errorf("appserver: write: %w", err)
	}
}

func (c *Client) readLoop() {
	defer c.shutdown()
	for {
		b, err := c.conn.Read(c.ctx)
		if err != nil {
			if !c.connClosed.Load() {
				c.log.Warn("appserver-read-failed", "err", err)
			}
			return
		}
		var in inbound
		if err := json.Unmarshal(b, &in); err != nil {
			c.log.Warn("appserver-bad-message", "bytes", len(b))
			continue
		}
		if !c.dispatch(in) {
			return
		}
	}
}

// dispatch routes one message; it reports false when the client is closing.
func (c *Client) dispatch(in inbound) bool {
	hasID := len(in.ID) > 0 && string(in.ID) != "null"
	switch {
	case in.Method != "" && hasID:
		// A server request (an approval, a token refresh) goes to every
		// client of the thread, and the first answer wins. The router has
		// nothing to say, so it stays silent and the Codex TUI answers: an
		// error reply here failed every approval in a routed thread.
		c.log.Debug("appserver-server-request-ignored", "method", in.Method)
	case in.Method == MethodSettingsUpdated:
		u, ok := parseSettingsUpdated(in.Params)
		if !ok {
			c.log.Warn("appserver-bad-notification", "method", in.Method)
			return true
		}
		select {
		case c.updates <- u:
		case <-c.ctx.Done():
			return false
		}
	case c.threads != nil && threadMethods[in.Method]:
		ev, ok := parseThreadEvent(in.Method, in.Params)
		if !ok {
			c.log.Warn("appserver-bad-notification", "method", in.Method)
			return true
		}
		select {
		case c.threads <- ev:
		case <-c.ctx.Done():
			return false
		}
	case in.Method == "" && hasID:
		c.resolve(in)
	}
	return true
}

func (c *Client) resolve(in inbound) {
	var id int64
	if err := json.Unmarshal(in.ID, &id); err != nil {
		c.log.Warn("appserver-unknown-response-id")
		return
	}
	c.mu.Lock()
	ch, ok := c.pending[id]
	delete(c.pending, id)
	c.mu.Unlock()
	if !ok {
		return
	}
	if in.Error != nil {
		ch <- reply{err: in.Error}
		return
	}
	ch <- reply{result: in.Result}
}

func (c *Client) shutdown() {
	c.mu.Lock()
	for id, ch := range c.pending {
		ch <- reply{err: ErrClosed}
		delete(c.pending, id)
	}
	c.pending = nil
	c.mu.Unlock()
	close(c.updates)
	if c.threads != nil {
		close(c.threads)
	}
	close(c.done)
}

func parseSettingsUpdated(params json.RawMessage) (SettingsUpdated, bool) {
	var p struct {
		ThreadID       string   `json:"threadId"`
		ThreadSettings Settings `json:"threadSettings"`
	}
	if err := json.Unmarshal(params, &p); err != nil || p.ThreadID == "" {
		return SettingsUpdated{}, false
	}
	return SettingsUpdated{ThreadID: p.ThreadID, Settings: p.ThreadSettings}, true
}

var threadMethods = map[string]bool{
	MethodThreadStarted: true, MethodThreadClosed: true,
	MethodTurnStarted: true, MethodTurnCompleted: true,
}

func parseThreadEvent(method string, params json.RawMessage) (ThreadEvent, bool) {
	var p struct {
		ThreadID string  `json:"threadId"`
		Thread   *Thread `json:"thread"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return ThreadEvent{}, false
	}
	ev := ThreadEvent{Method: method, ThreadID: p.ThreadID}
	if method == MethodThreadStarted {
		if p.Thread == nil || p.Thread.ID == "" {
			return ThreadEvent{}, false
		}
		ev.ThreadID, ev.Thread = p.Thread.ID, p.Thread
	}
	return ev, ev.ThreadID != ""
}

func clientVersion() string {
	if bi, ok := debug.ReadBuildInfo(); ok && bi.Main.Version != "" {
		return bi.Main.Version
	}
	return "dev"
}
