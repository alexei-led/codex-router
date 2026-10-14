package appserver

import (
	"context"
	"net"
	"net/http"

	"github.com/coder/websocket"
)

// WebSocket returns the Dialer for Codex's app-server unix socket transport:
// a WebSocket handshake over the socket, one JSON-RPC message per text frame.
func WebSocket() Dialer { return wsDialer{} }

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
