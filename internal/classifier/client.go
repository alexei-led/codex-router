package classifier

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"sync"
	"time"
	"unicode/utf16"

	"github.com/alexei-led/codex-router/internal/config"
)

// Client asks one classifier endpoint. It allows one request at a time: a
// call waits, under its context, while another is in flight, and a request
// abandoned at the deadline holds the slot until it settles, so a late answer
// never overlaps the next turn's. now measures the budget, the breaker and the
// service latency, which excludes time waiting for the slot; the context carries
// the same timeout as a duration, so an injected clock never makes the request
// expire early or late.
type Client struct {
	cfg     config.Classifier
	key     string
	adapter Adapter
	doer    Doer
	now     func() time.Time

	slot      chan struct{}
	abandoned <-chan reply // written only by the slot holder
	status    int          // the last HTTP error status, written only by the slot holder

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

// Ask returns the classifier's advice for in, or a Code error. Cancellation
// and pause answers do not count toward the breaker.
func (c *Client) Ask(ctx context.Context, in Input) (Advice, error) {
	if ctx.Err() != nil {
		return Advice{}, ErrCancelled
	}
	c.mu.Lock()
	paused := c.now().Before(c.health.PausedUntil)
	c.mu.Unlock()
	if paused {
		return Advice{}, ErrPaused
	}
	// Wait rather than fail fast: a review the next turn cancelled frees the
	// slot only once its abandoned post settles, moments later.
	queuedAt := c.now()
	select {
	case c.slot <- struct{}{}:
		c.recordQueueWait(max(c.now().Sub(queuedAt), 0), false)
	case <-ctx.Done():
		wait := max(c.now().Sub(queuedAt), 0)
		deadline := errors.Is(ctx.Err(), context.DeadlineExceeded)
		c.recordQueueWait(wait, deadline)
		if deadline {
			slog.Default().Warn("classifier-queue-timeout", "wait", wait)
		}
		return Advice{}, ErrCancelled
	}
	defer c.release()

	timeout := time.Duration(c.cfg.TimeoutMs) * time.Millisecond
	start := c.now()
	deadline, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	c.status = 0
	advice, err := c.complete(budget{ctx: deadline, outer: ctx, until: start.Add(timeout)}, in)
	latency := c.now().Sub(start)
	c.record(err, latency)
	if err != nil {
		return Advice{}, err
	}
	advice.Latency = latency
	return advice, nil
}

func (c *Client) recordQueueWait(wait time.Duration, timeout bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.health.QueueWait = wait
	if timeout {
		c.health.QueueTimeouts++
	}
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
		c.health.Failures = 0
		c.health.PausedUntil = time.Time{}
		c.health.Latency = latency
		c.health.LastError = ""
	case errors.Is(err, ErrCancelled):
	default:
		c.health.Failures = min(c.health.Failures+1, FailuresToPause)
		c.health.LastError = c.reason(err)
		if c.health.Failures >= FailuresToPause {
			c.health.PausedUntil = c.now().Add(PauseDuration)
		}
	}
}

// reason is err as a short, key-free code for Health.LastError.
func (c *Client) reason(err error) string {
	switch {
	case errors.Is(err, ErrMalformed):
		return "bad response"
	case errors.Is(err, ErrAuth), errors.Is(err, ErrHTTP):
		return "http " + strconv.Itoa(c.status)
	}
	return err.Error()
}

// budget bounds one Ask. ctx ends at the timeout; outer is the caller's
// context, whose end is a cancellation rather than a timeout; until is the
// deadline on the client's clock.
type budget struct {
	ctx, outer context.Context
	until      time.Time
}

// complete runs up to MaxAttempts route requests inside b.
func (c *Client) complete(b budget, in Input) (Advice, error) {
	body, err := c.adapter.Build(c.cfg.Model, in)
	if err != nil {
		return Advice{}, ErrMalformed
	}
	for attempt := 1; ; attempt++ {
		r, err := c.send(b.ctx, body)
		if err != nil {
			return Advice{}, stopped(b.outer)
		}
		if r.err != nil {
			if attempt == MaxAttempts {
				return Advice{}, ErrUnreachable
			}
			if err := c.wait(b, RetryDelay); err != nil {
				return Advice{}, err
			}
			continue
		}
		switch {
		case r.ok():
			return c.answer(b, in, r.body)
		case r.status == http.StatusUnauthorized || r.status == http.StatusForbidden:
			c.status = r.status
			return Advice{}, ErrAuth
		case !transient(r.status) || attempt == MaxAttempts:
			c.status = r.status
			return Advice{}, ErrHTTP
		}
		delay, ok := retryAfter(r.header.Get("Retry-After"), c.now())
		if !ok {
			delay = RetryDelay
		}
		if err := c.wait(b, delay); err != nil {
			return Advice{}, err
		}
	}
}

func (c *Client) answer(b budget, in Input, body []byte) (Advice, error) {
	if oversized(body) {
		return Advice{}, ErrMalformed
	}
	advice, err := c.adapter.Parse(body)
	if err != nil {
		return Advice{}, ErrMalformed
	}
	if b.ctx.Err() != nil || c.remaining(b) <= 0 {
		return Advice{}, stopped(b.outer)
	}
	step, ok := c.adapter.(ActivityStep)
	if !ok || c.remaining(b) < ActivityStepMin {
		return advice, nil
	}
	advice.Activity = c.activity(b, step, in)
	if b.outer.Err() != nil {
		return Advice{}, ErrCancelled
	}
	return advice, nil
}

// activity sends the optional second request: one attempt, no retry. Any
// failure reads as no activity and leaves the route advice and health alone.
func (c *Client) activity(b budget, step ActivityStep, in Input) *ActivityAdvice {
	body, err := step.BuildActivity(c.cfg.Model, in)
	if err != nil {
		return nil
	}
	r, err := c.send(b.ctx, body)
	if err != nil || r.err != nil || !r.ok() || oversized(r.body) || c.remaining(b) <= 0 {
		return nil
	}
	return step.ParseActivity(r.body)
}

func (c *Client) remaining(b budget) time.Duration {
	return b.until.Sub(c.now())
}

// wait sleeps d unless the budget is shorter: a retry that cannot finish in
// time is not sent.
func (c *Client) wait(b budget, d time.Duration) error {
	if b.outer.Err() != nil {
		return ErrCancelled
	}
	if d >= c.remaining(b) {
		return ErrTimeout
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-b.ctx.Done():
		return stopped(b.outer)
	}
}

// oversized reports an answer longer than MaxAnswerChars, counted in UTF-16
// code units as the reference client's string length is. Invalid UTF-8 bytes
// count one each, as the replacement character they decode to.
func oversized(body []byte) bool {
	if len(body) <= MaxAnswerChars {
		return false
	}
	n := 0
	for _, r := range string(body) {
		if n += utf16.RuneLen(r); n > MaxAnswerChars {
			return true
		}
	}
	return false
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
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxAnswerBytes+1))
	return reply{status: resp.StatusCode, header: resp.Header, body: data, err: err}
}
