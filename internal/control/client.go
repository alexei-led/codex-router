package control

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/alexei-led/codex-router/internal/route"
)

// ErrDisconnected is returned by Send and Subscribe while the socket is down.
var ErrDisconnected = errors.New("control socket disconnected")

// Client talks to a Server. It never redials: when the socket is lost,
// Sessions and Snapshots close and Send returns ErrDisconnected, so the
// owner redials.
type Client struct {
	hello    Hello
	conn     net.Conn
	sessions chan []route.SessionInfo
	snaps    chan route.Snapshot
	done     chan struct{}
	lost     chan struct{}
	once     sync.Once
	wmu      sync.Mutex
	ids      atomic.Uint64

	mu      sync.Mutex
	session string // subscribed session
	snapSeq uint64 // newest snapshot delivered for it
	listSeq uint64 // newest sessions list delivered
	pending map[string]chan Message
}

// Dial connects to the socket at path and does the hello handshake. The
// first connect must succeed.
func Dial(path string) (*Client, error) {
	conn, err := net.DialTimeout("unix", path, time.Second)
	if err != nil {
		return nil, fmt.Errorf("dial control socket: %w", err)
	}
	hello, sc, err := handshake(conn)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	c := &Client{
		hello:    hello,
		conn:     conn,
		sessions: make(chan []route.SessionInfo, 1),
		snaps:    make(chan route.Snapshot, 8),
		done:     make(chan struct{}),
		lost:     make(chan struct{}),
		pending:  map[string]chan Message{},
	}
	go c.run(sc)
	return c, nil
}

func handshake(conn net.Conn) (Hello, *bufio.Scanner, error) {
	_ = conn.SetDeadline(time.Now().Add(helloTimeout))
	b, err := json.Marshal(Request{Command: route.Command{Op: OpHello}, Protocol: Protocol})
	if err != nil {
		return Hello{}, nil, err
	}
	if _, err := conn.Write(append(b, '\n')); err != nil {
		return Hello{}, nil, fmt.Errorf("control hello: %w", err)
	}
	sc := bufio.NewScanner(conn)
	sc.Buffer(make([]byte, 0, 64<<10), maxLine)
	if !sc.Scan() {
		err := sc.Err()
		if err == nil {
			err = io.EOF
		}
		return Hello{}, nil, fmt.Errorf("control hello: %w", err)
	}
	var m Message
	if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
		return Hello{}, nil, fmt.Errorf("control hello: %w", err)
	}
	switch {
	case m.Type == MessageError:
		return Hello{}, nil, errors.New("control hello: " + m.Error)
	case m.Type != MessageHello || m.Hello == nil:
		return Hello{}, nil, fmt.Errorf("control hello: unexpected %q reply", m.Type)
	}
	_ = conn.SetDeadline(time.Time{})
	return *m.Hello, sc, nil
}

// Hello is the endpoint's identity from the handshake.
func (c *Client) Hello() Hello { return c.hello }

// Sessions yields each new sessions list. When the consumer lags, it gets
// the newest list only. The channel closes on Close or when the connection
// is lost.
func (c *Client) Sessions() <-chan []route.SessionInfo { return c.sessions }

// Snapshots yields the subscribed session's snapshots, never another
// session's and never one older than the last delivered. When the consumer
// lags, the oldest queued snapshot is dropped. The channel closes on Close
// or when the connection is lost.
func (c *Client) Snapshots() <-chan route.Snapshot { return c.snaps }

// Subscribe switches the connection to session's snapshots ("" for none)
// and waits for the ack. Snapshots queued for the old session are dropped;
// the new session's current snapshot is queued before Subscribe returns.
func (c *Client) Subscribe(session string) error {
	c.mu.Lock()
	c.session, c.snapSeq = session, 0
	drain(c.snaps)
	c.mu.Unlock()
	return c.request(Request{Command: route.Command{Op: OpSubscribe, Session: session}})
}

// Send issues a command and waits for the reply with its id; Send sets
// cmd.ID. Session is a session id (SessionID). A server-side rejection comes
// back as an error.
func (c *Client) Send(cmd route.Command) error {
	return c.request(Request{Command: cmd})
}

func (c *Client) request(r Request) error {
	select {
	case <-c.lost:
		return ErrDisconnected
	default:
	}
	r.ID = "c-" + strconv.FormatUint(c.ids.Add(1), 10)
	b, err := json.Marshal(r)
	if err != nil {
		return err
	}
	reply := make(chan Message, 1)
	c.mu.Lock()
	c.pending[r.ID] = reply
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.pending, r.ID)
		c.mu.Unlock()
	}()
	c.wmu.Lock()
	_ = c.conn.SetWriteDeadline(time.Now().Add(writeTimeout))
	_, err = c.conn.Write(append(b, '\n'))
	c.wmu.Unlock()
	if err != nil {
		return fmt.Errorf("%w: %w", ErrDisconnected, err)
	}
	select {
	case m := <-reply:
		if m.Type == MessageError {
			return errors.New(m.Error)
		}
		return nil
	case <-c.lost:
		return ErrDisconnected
	case <-c.done:
		return ErrDisconnected
	case <-time.After(sendTimeout):
		return errors.New("control reply timeout")
	}
}

// Close stops the client and closes Sessions and Snapshots.
func (c *Client) Close() error {
	c.once.Do(func() {
		close(c.done)
		_ = c.conn.Close()
	})
	return nil
}

func (c *Client) run(sc *bufio.Scanner) {
	defer func() {
		_ = c.conn.Close()
		c.mu.Lock()
		close(c.lost)
		close(c.snaps)
		close(c.sessions)
		c.mu.Unlock()
	}()
	for sc.Scan() {
		var m Message
		if json.Unmarshal(sc.Bytes(), &m) == nil {
			c.receive(m)
		}
	}
}

// receive routes one line. It holds c.mu while delivering, so Subscribe's
// drain cannot interleave with an old session's snapshot.
func (c *Client) receive(m Message) {
	c.mu.Lock()
	defer c.mu.Unlock()
	switch m.Type {
	case MessageSessions:
		if m.Seq > c.listSeq {
			c.listSeq = m.Seq
			deliver(c.sessions, m.Items)
		}
	case MessageSnapshot:
		if m.Snapshot != nil && m.Session == c.session && m.Snapshot.Seq >= c.snapSeq {
			c.snapSeq = m.Snapshot.Seq
			deliver(c.snaps, *m.Snapshot)
		}
	case MessageAck, MessageError:
		if ch := c.pending[m.ID]; ch != nil {
			select {
			case ch <- m:
			default:
			}
		}
	}
}

// deliver queues v without blocking, dropping the oldest queued value.
func deliver[T any](ch chan T, v T) {
	for {
		select {
		case ch <- v:
			return
		default:
		}
		select {
		case <-ch:
		default:
		}
	}
}

func drain[T any](ch chan T) {
	for {
		select {
		case _, ok := <-ch:
			if !ok {
				return
			}
		default:
			return
		}
	}
}
