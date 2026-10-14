package link

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"time"

	"github.com/alexei-led/codex-router/internal/proxy"
	"github.com/alexei-led/codex-router/internal/responses"
)

// Redial, breaker and queue rules. A dial includes the hello and is bounded
// by DialTimeout, because a decide waits for it.
const (
	DialTimeout     = 200 * time.Millisecond
	BackoffMin      = 50 * time.Millisecond
	BackoffMax      = 2 * time.Second
	FailuresToOpen  = 3
	BreakerCooldown = 10 * time.Second
	ObserveQueue    = 256
	observeTimeout  = time.Second
)

var errClosed = errors.New("link: connection closed")

// ClientOptions configure a Client. Timeout bounds each Decide on top of the
// caller's context; zero means proxy.DefaultDecideTimeout.
type ClientOptions struct {
	Path    string
	HomeID  string
	Timeout time.Duration
	Logger  *slog.Logger
}

// Client is the edge's end of the link. Every failure means "no rewrite":
// the brain down (redial after a backoff), slow (the call's deadline, and a
// breaker that skips the brain for BreakerCooldown after FailuresToOpen
// timeouts in a row) or gone mid-call. Observe never blocks: it queues and
// drops when ObserveQueue is full.
type Client struct {
	path    string
	homeID  string
	timeout time.Duration
	log     *slog.Logger
	now     func() time.Time // breaker and backoff clock; tests replace it

	observes chan Header
	done     chan struct{}
	wg       sync.WaitGroup

	mu        sync.Mutex
	conn      *clientConn // nil while down
	down      bool        // outage already logged
	dialAt    time.Time   // no dial before this
	backoff   time.Duration
	failures  int
	openUntil time.Time
	nextID    uint64
	closed    bool
}

var (
	_ proxy.Decider  = (*Client)(nil)
	_ proxy.Observer = (*Client)(nil)
)

// NewClient returns a Client and starts its observe writer. It dials on the
// first call, not here, so an edge starts while its brain is down.
func NewClient(o ClientOptions) *Client {
	log := o.Logger
	if log == nil {
		log = slog.Default()
	}
	timeout := o.Timeout
	if timeout <= 0 {
		timeout = proxy.DefaultDecideTimeout
	}
	c := &Client{
		path: o.Path, homeID: o.HomeID, timeout: timeout, log: log, now: time.Now,
		observes: make(chan Header, ObserveQueue), done: make(chan struct{}),
	}
	c.wg.Go(c.writeObserves)
	return c
}

// Decide asks the brain for req's rewrite under the call's deadline.
func (c *Client) Decide(ctx context.Context, req *responses.Request, meta responses.Meta) (responses.Rewrite, bool) {
	if req == nil || !c.allowed() {
		return responses.Rewrite{}, false
	}
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	cc := c.connect(ctx)
	if cc == nil {
		return responses.Rewrite{}, false
	}
	deadline, _ := ctx.Deadline()
	h := Header{
		Type: TypeDecide, ID: c.id(), Meta: &meta,
		TimeoutMs: max(time.Until(deadline).Milliseconds(), 1),
	}
	reply := cc.expect(h.ID)
	defer cc.forget(h.ID)
	if err := cc.write(ctx, h, req.Body()); err != nil {
		c.timedOut(ctx)
		return responses.Rewrite{}, false
	}
	select {
	case r := <-reply:
		c.record(true)
		if r.Type == TypeRewrite && r.Rewrite != nil {
			return *r.Rewrite, true
		}
		return responses.Rewrite{}, false
	case <-cc.done:
		return responses.Rewrite{}, false
	case <-ctx.Done():
		if c.timedOut(ctx) {
			c.log.Warn("link-decide-timeout", "thread", meta.ThreadID, "turn", meta.TurnID)
		}
		return responses.Rewrite{}, false
	}
}

// Observe queues the observation for the brain, or drops it when the queue
// is full.
func (c *Client) Observe(meta responses.Meta, served string, usage responses.Usage, status int) {
	h := Header{Type: TypeObserve, Meta: &meta, Served: served, Usage: &usage, Status: status}
	select {
	case c.observes <- h:
	default:
		c.log.Warn("link-observe-dropped", "thread", meta.ThreadID, "turn", meta.TurnID)
	}
}

// Close drops the connection and stops the observe writer. Queued
// observations are lost.
func (c *Client) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	close(c.done)
	cc := c.conn
	c.conn = nil
	c.mu.Unlock()
	if cc != nil {
		cc.close()
	}
	c.wg.Wait()
	return nil
}

func (c *Client) writeObserves() {
	for {
		select {
		case <-c.done:
			return
		case h := <-c.observes:
			ctx, cancel := context.WithTimeout(context.Background(), observeTimeout)
			if cc := c.connect(ctx); cc != nil {
				_ = cc.write(ctx, h, nil)
			}
			cancel()
		}
	}
}

func (c *Client) id() uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.nextID++
	return c.nextID
}

// allowed is false while the breaker is open. Once the cooldown ends calls
// go through again; the failure count stays at FailuresToOpen, so one more
// timeout reopens it.
func (c *Client) allowed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return !c.now().Before(c.openUntil)
}

// timedOut records a failure when ctx ran out of time. A cancelled caller
// or a lost connection says nothing about the brain's speed.
func (c *Client) timedOut(ctx context.Context) bool {
	if !errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return false
	}
	c.record(false)
	return true
}

// record resets the breaker's count on an answer and counts a timeout.
func (c *Client) record(answered bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if answered {
		c.failures = 0
		return
	}
	c.failures = min(c.failures+1, FailuresToOpen)
	if c.failures >= FailuresToOpen {
		c.openUntil = c.now().Add(BreakerCooldown)
		c.log.Warn("link-breaker-open", "for", BreakerCooldown)
	}
}

// connect returns the live connection, or dials one unless a failed dial's
// backoff is still running. Callers wait for one dial at a time; a dial
// takes at most DialTimeout.
func (c *Client) connect(ctx context.Context) *clientConn {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil
	}
	if c.conn != nil {
		return c.conn
	}
	if c.now().Before(c.dialAt) {
		return nil
	}
	cc, err := c.dial(ctx)
	if err != nil {
		c.backoff = min(max(c.backoff*2, BackoffMin), BackoffMax)
		c.dialAt = c.now().Add(c.backoff)
		if !c.down {
			c.log.Warn("link-down", "err", err)
			c.down = true
		}
		return nil
	}
	c.backoff, c.dialAt, c.conn, c.down = 0, time.Time{}, cc, false
	c.log.Info("link-up")
	c.wg.Go(func() { c.read(cc) })
	return cc
}

func (c *Client) dial(ctx context.Context) (*clientConn, error) {
	ctx, cancel := context.WithTimeout(ctx, DialTimeout)
	defer cancel()
	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", c.path)
	if err != nil {
		return nil, err
	}
	deadline, _ := ctx.Deadline()
	_ = conn.SetDeadline(deadline)
	br := bufio.NewReader(conn)
	if err := c.hello(conn, br); err != nil {
		_ = conn.Close()
		return nil, err
	}
	_ = conn.SetDeadline(time.Time{})
	return &clientConn{
		conn: conn, br: br, wsem: make(chan struct{}, 1),
		pending: map[uint64]chan Header{}, done: make(chan struct{}),
	}, nil
}

func (c *Client) hello(conn net.Conn, br *bufio.Reader) error {
	if err := WriteFrame(conn, Header{Type: TypeHello, HomeID: c.homeID}, nil); err != nil {
		return err
	}
	h, _, err := ReadFrame(br)
	switch {
	case err != nil:
		return err
	case h.Type == TypeError:
		return fmt.Errorf("brain refused: %s", h.Error)
	case h.Type != TypeHello || h.HomeID != c.homeID:
		return fmt.Errorf("brain serves home %q, want %q", h.HomeID, c.homeID)
	}
	return nil
}

// read delivers replies until the connection breaks, then marks the link
// down so the next call redials.
func (c *Client) read(cc *clientConn) {
	defer func() {
		cc.close()
		c.mu.Lock()
		if c.conn == cc {
			c.conn = nil
			if !c.closed {
				c.log.Warn("link-down", "err", errClosed)
				c.down = true
			}
		}
		c.mu.Unlock()
	}()
	for {
		h, _, err := ReadFrame(cc.br)
		if err != nil {
			return
		}
		if h.Type == TypeRewrite || h.Type == TypeNone {
			cc.deliver(h)
		}
	}
}

// clientConn is one connection to the brain. Writers take wsem, so a call
// waiting behind a large body still honors its deadline.
type clientConn struct {
	conn net.Conn
	br   *bufio.Reader
	wsem chan struct{}
	done chan struct{}
	once sync.Once

	mu      sync.Mutex
	pending map[uint64]chan Header
}

func (cc *clientConn) expect(id uint64) <-chan Header {
	ch := make(chan Header, 1)
	cc.mu.Lock()
	cc.pending[id] = ch
	cc.mu.Unlock()
	return ch
}

func (cc *clientConn) forget(id uint64) {
	cc.mu.Lock()
	delete(cc.pending, id)
	cc.mu.Unlock()
}

// deliver hands a reply to its waiting call; a reply to a call that already
// gave up is dropped.
func (cc *clientConn) deliver(h Header) {
	cc.mu.Lock()
	ch := cc.pending[h.ID]
	delete(cc.pending, h.ID)
	cc.mu.Unlock()
	if ch != nil {
		ch <- h
	}
}

// write sends one frame under ctx's deadline. A failed write may have sent
// part of a frame, so it closes the connection.
func (cc *clientConn) write(ctx context.Context, h Header, body []byte) error {
	select {
	case cc.wsem <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	case <-cc.done:
		return errClosed
	}
	defer func() { <-cc.wsem }()
	if d, ok := ctx.Deadline(); ok {
		_ = cc.conn.SetWriteDeadline(d)
	}
	if err := WriteFrame(cc.conn, h, body); err != nil {
		cc.close()
		return err
	}
	return nil
}

func (cc *clientConn) close() {
	cc.once.Do(func() {
		close(cc.done)
		_ = cc.conn.Close()
	})
}
