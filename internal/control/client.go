package control

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/alexei-led/codex-router/internal/route"
)

// ErrDisconnected is returned by Send while the socket is down.
var ErrDisconnected = errors.New("control socket disconnected")

// Client talks to a Server. It never redials: when the socket is lost,
// Updates closes and Send returns ErrDisconnected, so the owner redials.
type Client struct {
	updates chan route.Snapshot
	replies chan Message
	done    chan struct{}
	once    sync.Once

	mu   sync.Mutex
	conn net.Conn
	lost chan struct{}
	send sync.Mutex
}

// Dial connects to the socket at path. The first connect must succeed.
func Dial(path string) (*Client, error) {
	conn, err := net.DialTimeout("unix", path, time.Second)
	if err != nil {
		return nil, fmt.Errorf("dial control socket: %w", err)
	}
	c := &Client{
		updates: make(chan route.Snapshot, 8),
		replies: make(chan Message, 1),
		done:    make(chan struct{}),
		conn:    conn,
		lost:    make(chan struct{}),
	}
	go c.run(conn)
	return c, nil
}

// Updates yields every pushed snapshot. When the consumer lags, the oldest
// queued snapshot is dropped. The channel closes on Close or when the
// connection is lost.
func (c *Client) Updates() <-chan route.Snapshot { return c.updates }

// Send issues a command and waits for the server's ack. A server-side
// rejection comes back as an error.
func (c *Client) Send(cmd route.Command) error {
	c.send.Lock()
	defer c.send.Unlock()
	c.mu.Lock()
	conn := c.conn
	c.mu.Unlock()
	if conn == nil {
		return ErrDisconnected
	}
	select {
	case <-c.replies:
	default:
	}
	b, err := json.Marshal(cmd)
	if err != nil {
		return err
	}
	_ = conn.SetWriteDeadline(time.Now().Add(writeTimeout))
	if _, err := conn.Write(append(b, '\n')); err != nil {
		return fmt.Errorf("%w: %v", ErrDisconnected, err)
	}
	select {
	case m := <-c.replies:
		if m.Type == MessageError {
			return errors.New(m.Error)
		}
		return nil
	case <-c.lost:
		return ErrDisconnected
	case <-time.After(sendTimeout):
		// A late reply must not answer the next Send; drop the connection.
		_ = conn.Close()
		return errors.New("control reply timeout")
	case <-c.done:
		return ErrDisconnected
	}
}

// Close stops the client and closes Updates.
func (c *Client) Close() error {
	c.once.Do(func() {
		close(c.done)
		c.mu.Lock()
		if c.conn != nil {
			_ = c.conn.Close()
		}
		c.mu.Unlock()
	})
	return nil
}

func (c *Client) run(conn net.Conn) {
	defer close(c.updates)
	c.read(conn)
	c.mu.Lock()
	c.conn = nil
	close(c.lost)
	c.mu.Unlock()
}

func (c *Client) read(conn net.Conn) {
	defer func() { _ = conn.Close() }()
	sc := bufio.NewScanner(conn)
	sc.Buffer(make([]byte, 0, 64<<10), maxLine)
	for sc.Scan() {
		var m Message
		if json.Unmarshal(sc.Bytes(), &m) != nil {
			continue
		}
		switch m.Type {
		case MessageSnapshot:
			if m.Snapshot != nil {
				c.deliver(*m.Snapshot)
			}
		case MessageAck, MessageError:
			select {
			case c.replies <- m:
			default:
			}
		}
	}
}

func (c *Client) deliver(s route.Snapshot) {
	for {
		select {
		case c.updates <- s:
			return
		default:
		}
		select {
		case <-c.updates:
		default:
		}
	}
}
