package appserver

import (
	"bufio"
	"context"
	"io"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// WebSocket returns the Dialer for Codex's app-server unix socket transport:
// a WebSocket handshake over the socket, one JSON-RPC message per text frame.
func WebSocket() Dialer { return wsDialer{} }

// JSONLines returns the fallback Dialer: newline-delimited JSON-RPC over the
// raw unix socket, as `codex app-server --listen stdio://` speaks.
func JSONLines() Dialer { return linesDialer{} }

type wsDialer struct{}

func (wsDialer) Dial(ctx context.Context, socketPath string) (Conn, error) {
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", socketPath)
		},
	}
	// The host is a placeholder: the transport always dials socketPath.
	ws, resp, err := websocket.Dial(ctx, "ws://localhost/", &websocket.DialOptions{
		HTTPClient: &http.Client{Transport: transport},
	})
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	if err != nil {
		return nil, err
	}
	return newWSConn(ws), nil
}

type wsConn struct{ ws *websocket.Conn }

func newWSConn(ws *websocket.Conn) wsConn {
	ws.SetReadLimit(maxMessage)
	return wsConn{ws}
}

func (c wsConn) Read(ctx context.Context) ([]byte, error) {
	_, b, err := c.ws.Read(ctx)
	return b, err
}

func (c wsConn) Write(ctx context.Context, msg []byte) error {
	return c.ws.Write(ctx, websocket.MessageText, msg)
}

// Close skips the close handshake: the client closes at exit or after a
// failed write, and the handshake would wait up to 10 s on a server that has
// stopped reading.
func (c wsConn) Close() error { return c.ws.CloseNow() }

type linesDialer struct{}

func (linesDialer) Dial(ctx context.Context, socketPath string) (Conn, error) {
	var d net.Dialer
	nc, err := d.DialContext(ctx, "unix", socketPath)
	if err != nil {
		return nil, err
	}
	return newLinesConn(nc), nil
}

type linesConn struct {
	nc      net.Conn
	scanner *bufio.Scanner
	writeMu sync.Mutex
}

func newLinesConn(nc net.Conn) *linesConn {
	s := bufio.NewScanner(nc)
	s.Buffer(make([]byte, 0, 64<<10), maxMessage)
	return &linesConn{nc: nc, scanner: s}
}

// Read returns the next non-empty line. Cancelling ctx unblocks it by
// expiring the read deadline, which leaves the connection unusable, matching
// the WebSocket Conn's close-on-cancel behaviour.
func (c *linesConn) Read(ctx context.Context) ([]byte, error) {
	stop := context.AfterFunc(ctx, func() { _ = c.nc.SetReadDeadline(time.Now()) })
	defer stop()
	for c.scanner.Scan() {
		if line := c.scanner.Bytes(); len(line) > 0 {
			return append([]byte(nil), line...), nil
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := c.scanner.Err(); err != nil {
		return nil, err
	}
	return nil, io.EOF
}

func (c *linesConn) Write(ctx context.Context, msg []byte) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	stop := context.AfterFunc(ctx, func() { _ = c.nc.SetWriteDeadline(time.Now()) })
	defer stop()
	_, err := c.nc.Write(append(msg[:len(msg):len(msg)], '\n'))
	return err
}

func (c *linesConn) Close() error { return c.nc.Close() }
