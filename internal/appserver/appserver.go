package appserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
	"sync"
	"sync/atomic"

	"github.com/alexei-led/codex-router/internal/config"
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
	UpdateSettings(ctx context.Context, threadID string, s Settings) error
	Updates() <-chan SettingsUpdated
	Close() error
}

// JSON-RPC method names used.
const (
	MethodInitialize      = "initialize"
	MethodInitialized     = "initialized"
	MethodSettingsUpdate  = "thread/settings/update"
	MethodSettingsUpdated = "thread/settings/updated"
	MethodTurnStarted     = "turn/started"
	MethodTurnCompleted   = "turn/completed"
)

const (
	clientName = "codex-router"
	// updatesBuffer absorbs bursts; the read loop blocks when it is full, so
	// consumers must drain Updates independently of their own RPC calls.
	updatesBuffer = 64
	// maxMessage bounds one inbound message; app-server item notifications
	// can carry large tool output.
	maxMessage        = 64 << 20
	codeMethodMissing = -32601
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

// Client is a JSON-RPC client over one Conn. Writes and reads use the
// client's lifetime context, so a caller's cancelled ctx abandons only its
// own wait and never tears down the shared connection.
type Client struct {
	conn    Conn
	ctx     context.Context
	cancel  context.CancelFunc
	nextID  atomic.Int64
	writeMu sync.Mutex

	mu      sync.Mutex
	pending map[int64]chan reply

	updates   chan SettingsUpdated
	done      chan struct{}
	closing   atomic.Bool
	closeOnce sync.Once
	closeErr  error
}

// Dial connects to the app-server socket through d and starts reading.
// Call Initialize before any other request.
func Dial(ctx context.Context, socketPath string, d Dialer) (API, error) {
	conn, err := d.Dial(ctx, socketPath)
	if err != nil {
		return nil, fmt.Errorf("appserver: dial: %w", err)
	}
	lifetime, cancel := context.WithCancel(context.Background())
	c := &Client{
		conn:    conn,
		ctx:     lifetime,
		cancel:  cancel,
		pending: make(map[int64]chan reply),
		updates: make(chan SettingsUpdated, updatesBuffer),
		done:    make(chan struct{}),
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
	return c.write(message{Method: MethodInitialized})
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

// Close closes the connection, fails pending calls with ErrClosed and waits
// for the read loop to stop.
func (c *Client) Close() error {
	c.closeOnce.Do(func() {
		c.closing.Store(true)
		c.closeErr = c.conn.Close()
		c.cancel()
		<-c.done
	})
	return c.closeErr
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

	if err := c.write(message{ID: &id, Method: method, Params: params}); err != nil {
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

func (c *Client) write(m message) error {
	m.JSONRPC = "2.0"
	b, err := json.Marshal(m)
	if err != nil {
		return fmt.Errorf("appserver: encode %s: %w", m.Method, err)
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if err := c.conn.Write(c.ctx, b); err != nil {
		if c.closing.Load() {
			return ErrClosed
		}
		return fmt.Errorf("appserver: write: %w", err)
	}
	return nil
}

func (c *Client) readLoop() {
	defer c.shutdown()
	for {
		b, err := c.conn.Read(c.ctx)
		if err != nil {
			if !c.closing.Load() {
				slog.Warn("appserver-read-failed", "err", err)
			}
			return
		}
		var in inbound
		if err := json.Unmarshal(b, &in); err != nil {
			slog.Warn("appserver-bad-message", "bytes", len(b))
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
		c.rejectRequest(in)
	case in.Method == MethodSettingsUpdated:
		u, ok := parseSettingsUpdated(in.Params)
		if !ok {
			slog.Warn("appserver-bad-notification", "method", in.Method)
			return true
		}
		select {
		case c.updates <- u:
		case <-c.ctx.Done():
			return false
		}
	case in.Method == "" && hasID:
		c.resolve(in)
	}
	return true
}

// rejectRequest answers a server-to-client request so the server never waits
// on this client; the router serves no client-side methods.
func (c *Client) rejectRequest(in inbound) {
	var id int64
	if err := json.Unmarshal(in.ID, &id); err != nil {
		slog.Warn("appserver-unanswerable-request", "method", in.Method)
		return
	}
	err := &RPCError{Code: codeMethodMissing, Message: "method not supported by codex-router"}
	if werr := c.write(message{ID: &id, Error: err}); werr != nil {
		slog.Warn("appserver-reject-failed", "method", in.Method, "err", werr)
	}
}

func (c *Client) resolve(in inbound) {
	var id int64
	if err := json.Unmarshal(in.ID, &id); err != nil {
		slog.Warn("appserver-unknown-response-id")
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

func clientVersion() string {
	if bi, ok := debug.ReadBuildInfo(); ok && bi.Main.Version != "" {
		return bi.Main.Version
	}
	return "dev"
}
