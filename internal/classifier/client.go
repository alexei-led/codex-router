package classifier

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/alexei-led/codex-router/internal/config"
)

// Client asks one classifier endpoint. It allows one request at a time: a
// call answers ErrBusy while another is in flight, and a request abandoned at
// the deadline holds the slot until it settles, so a late answer never
// overlaps the next turn's. Deadlines and waits use the context and timers;
// now stamps the breaker and the latency.
type Client struct {
	cfg     config.Classifier
	key     string
	adapter Adapter
	doer    Doer
	now     func() time.Time

	slot      chan struct{}
	abandoned <-chan reply // written only by the slot holder

	mu     sync.Mutex
	health Health
}

var _ Asker = (*Client)(nil)

// New returns a client for cfg. key is the bearer key read from cfg.KeyEnv;
// it is the only credential the client ever sends.
func New(cfg config.Classifier, key string, adapter Adapter, doer Doer, now func() time.Time) *Client {
	return &Client{cfg: cfg, key: key, adapter: adapter, doer: doer, now: now, slot: make(chan struct{}, 1)}
}

// Health returns the breaker state.
func (c *Client) Health() Health {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.health
}

// Ask returns the classifier's advice for in, or a Code error. Missing key,
// cancellation, pause and busy answers do not count toward the breaker.
func (c *Client) Ask(ctx context.Context, in Input) (Advice, error) {
	if c.cfg.KeyEnv != "" && c.key == "" {
		return Advice{}, ErrMissingKey
	}
	if ctx.Err() != nil {
		return Advice{}, ErrCancelled
	}
	c.mu.Lock()
	paused := c.now().Before(c.health.PausedUntil)
	c.mu.Unlock()
	if paused {
		return Advice{}, ErrPaused
	}
	select {
	case c.slot <- struct{}{}:
	default:
		return Advice{}, ErrBusy
	}
	defer c.release()

	start := c.now()
	deadline, cancel := context.WithDeadline(ctx, start.Add(time.Duration(c.cfg.TimeoutMs)*time.Millisecond))
	defer cancel()
	advice, err := c.complete(deadline, ctx, in)
	latency := c.now().Sub(start)
	c.record(err, latency)
	if err != nil {
		return Advice{}, err
	}
	advice.Latency = latency
	return advice, nil
}

func (c *Client) release() {
	out := c.abandoned
	if out == nil {
		<-c.slot
		return
	}
	c.abandoned = nil
	go func() {
		<-out
		<-c.slot
	}()
}

func (c *Client) record(err error, latency time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	switch {
	case err == nil:
		c.health = Health{Latency: latency}
	case errors.Is(err, ErrCancelled):
	default:
		c.health.Failures = min(c.health.Failures+1, FailuresToPause)
		if c.health.Failures >= FailuresToPause {
			c.health.PausedUntil = c.now().Add(PauseDuration)
		}
	}
}

// complete runs up to MaxAttempts route requests inside ctx's deadline. outer
// is the caller's context: its end is a cancellation, the deadline's a timeout.
func (c *Client) complete(ctx, outer context.Context, in Input) (Advice, error) {
	body, err := c.adapter.Build(c.cfg.Model, in)
	if err != nil {
		return Advice{}, ErrMalformed
	}
	for attempt := 1; ; attempt++ {
		r, err := c.send(ctx, body)
		if err != nil {
			return Advice{}, stopped(outer)
		}
		if r.err != nil {
			if attempt == MaxAttempts {
				return Advice{}, ErrUnreachable
			}
			if err := c.wait(ctx, outer, RetryDelay); err != nil {
				return Advice{}, err
			}
			continue
		}
		switch {
		case r.ok():
			return c.answer(ctx, outer, in, r.body)
		case r.status == http.StatusUnauthorized || r.status == http.StatusForbidden:
			return Advice{}, ErrAuth
		case !transient(r.status) || attempt == MaxAttempts:
			return Advice{}, ErrHTTP
		}
		delay, ok := retryAfter(r.header.Get("Retry-After"), c.now())
		if !ok {
			delay = RetryDelay
		}
		if err := c.wait(ctx, outer, delay); err != nil {
			return Advice{}, err
		}
	}
}

func (c *Client) answer(ctx, outer context.Context, in Input, body []byte) (Advice, error) {
	if len(body) > MaxAnswerBytes {
		return Advice{}, ErrMalformed
	}
	advice, err := c.adapter.Parse(body)
	if err != nil {
		return Advice{}, ErrMalformed
	}
	if ctx.Err() != nil {
		return Advice{}, stopped(outer)
	}
	step, ok := c.adapter.(ActivityStep)
	if !ok || c.remaining(ctx) < ActivityStepMin {
		return advice, nil
	}
	advice.Activity = c.activity(ctx, step, in)
	if outer.Err() != nil {
		return Advice{}, ErrCancelled
	}
	return advice, nil
}

// activity sends the optional second request: one attempt, no retry. Any
// failure reads as no activity and leaves the route advice and health alone.
func (c *Client) activity(ctx context.Context, step ActivityStep, in Input) *ActivityAdvice {
	body, err := step.BuildActivity(c.cfg.Model, in)
	if err != nil {
		return nil
	}
	r, err := c.send(ctx, body)
	if err != nil || r.err != nil || !r.ok() || len(r.body) > MaxAnswerBytes {
		return nil
	}
	return step.ParseActivity(r.body)
}

func (c *Client) remaining(ctx context.Context) time.Duration {
	deadline, _ := ctx.Deadline()
	return deadline.Sub(c.now())
}

// wait sleeps d unless the budget is shorter: a retry that cannot finish in
// time is not sent.
func (c *Client) wait(ctx, outer context.Context, d time.Duration) error {
	if outer.Err() != nil {
		return ErrCancelled
	}
	if d >= c.remaining(ctx) {
		return ErrTimeout
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return stopped(outer)
	}
}

func stopped(outer context.Context) error {
	if outer.Err() != nil {
		return ErrCancelled
	}
	return ErrTimeout
}

type reply struct {
	status int
	header http.Header
	body   []byte
	err    error
}

func (r reply) ok() bool { return r.status >= 200 && r.status < 300 }

// send posts body and waits for the reply or the end of ctx. A post still in
// flight at the end of ctx is recorded as abandoned and keeps the slot.
func (c *Client) send(ctx context.Context, body []byte) (reply, error) {
	out := make(chan reply, 1)
	go func() { out <- c.post(ctx, body) }()
	select {
	case r := <-out:
		return r, nil
	case <-ctx.Done():
		c.abandoned = out
		return reply{}, ctx.Err()
	}
}

func (c *Client) post(ctx context.Context, body []byte) reply {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.Endpoint, bytes.NewReader(body))
	if err != nil {
		return reply{err: err}
	}
	req.Header.Set("Content-Type", "application/json")
	if c.key != "" {
		req.Header.Set("Authorization", "Bearer "+c.key)
	}
	resp, err := c.doer.Do(req)
	if err != nil {
		return reply{err: err}
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, MaxAnswerBytes+1))
	return reply{status: resp.StatusCode, header: resp.Header, body: data, err: err}
}
