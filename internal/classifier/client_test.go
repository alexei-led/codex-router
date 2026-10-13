package classifier

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/alexei-led/codex-router/internal/config"
)

const (
	testEndpoint = "https://classifier.test/v1/answer"
	testKey      = "synthetic-test-key"
	testTimeout  = 1500 * time.Millisecond
)

func respond(status int, body string, header ...string) *http.Response {
	h := http.Header{}
	for i := 0; i+1 < len(header); i += 2 {
		h.Set(header[i], header[i+1])
	}
	return &http.Response{StatusCode: status, Header: h, Body: io.NopCloser(strings.NewReader(body))}
}

// recorder answers each request with the next scripted step and records what
// was sent and when.
type recorder struct {
	mu       sync.Mutex
	steps    []func(*http.Request) (*http.Response, error)
	requests []*http.Request
	bodies   []string
	times    []time.Time
}

func script(steps ...func(*http.Request) (*http.Response, error)) *recorder {
	return &recorder{steps: steps}
}

func (r *recorder) Do(req *http.Request) (*http.Response, error) {
	body, _ := io.ReadAll(req.Body)
	r.mu.Lock()
	n := len(r.requests)
	r.requests = append(r.requests, req)
	r.bodies = append(r.bodies, string(body))
	r.times = append(r.times, time.Now())
	step := r.steps[min(n, len(r.steps)-1)]
	r.mu.Unlock()
	return step(req)
}

func (r *recorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.requests)
}

func reply200(body string) func(*http.Request) (*http.Response, error) {
	return func(*http.Request) (*http.Response, error) { return respond(http.StatusOK, body), nil }
}

func status(code int, header ...string) func(*http.Request) (*http.Response, error) {
	return func(*http.Request) (*http.Response, error) { return respond(code, "", header...), nil }
}

func refused(*http.Request) (*http.Response, error) {
	return nil, errors.New("connect: connection refused")
}

func untilCancelled(r *http.Request) (*http.Response, error) {
	<-r.Context().Done()
	return nil, r.Context().Err()
}

func after(d time.Duration, body string) func(*http.Request) (*http.Response, error) {
	return func(*http.Request) (*http.Response, error) {
		time.Sleep(d)
		return respond(http.StatusOK, body), nil
	}
}

const jevAnswer = `{"answers":{"route":{"type":"choice","choice":"medium","confidence":0.9,` +
	`"probabilities":{"micro":0,"low":0,"medium":0.9,"high":0.1,"uncertain":0}},` +
	`"continuation":{"type":"noul","noul":0}}}`

func jevClient(doer Doer) *Client {
	cfg := config.Classifier{API: config.APISystemOne, Endpoint: testEndpoint, Model: "jev-1.13.0", KeyEnv: "TYPESAFE_API_KEY", TimeoutMs: int(testTimeout / time.Millisecond)}
	return New(cfg, testKey, SystemOne(), doer, time.Now)
}

func ollamaClient(doer Doer) *Client {
	cfg := config.Classifier{API: config.APIOllama, Endpoint: testEndpoint, Model: "qwen3.5:9b", TimeoutMs: int(testTimeout / time.Millisecond)}
	return New(cfg, "", Ollama(), doer, time.Now)
}

var smallInput = Input{
	Prompt:   "One small edit.",
	Dialogue: []Message{{Role: "user", Text: "rename the helper"}},
	Routes:   map[config.Tier]config.Route{config.TierHigh: {Model: "gpt-6.1-sol", Effort: config.EffortHigh}},
}

func TestSystemOneSendsOnlyItsBearerAndTheInputFields(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		rec := script(reply200(jevAnswer))
		advice, err := jevClient(rec).Ask(t.Context(), smallInput)
		if err != nil || advice.Choice != "medium" {
			t.Fatalf("advice = %+v, err = %v", advice, err)
		}
		req := rec.requests[0]
		if req.Method != http.MethodPost || req.URL.String() != testEndpoint {
			t.Errorf("request = %s %s", req.Method, req.URL)
		}
		if got := req.Header.Get("Authorization"); got != "Bearer "+testKey {
			t.Errorf("authorization header carries the wrong credential")
		}
		for name := range req.Header {
			if name != "Authorization" && name != "Content-Type" {
				t.Errorf("unexpected header %s", name)
			}
		}
		body := strings.ToLower(rec.bodies[0])
		for _, forbidden := range []string{"authorization", "chatgpt", "bearer", strings.ToLower(testKey)} {
			if strings.Contains(body, forbidden) {
				t.Errorf("body contains %q", forbidden)
			}
		}
		if !strings.Contains(rec.bodies[0], `"text":"One small edit."`) {
			t.Errorf("body lacks the prompt: %s", rec.bodies[0])
		}
	})
}

func TestOllamaSendsNoCredentialAndAsksTheActivitySecond(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		rec := script(
			reply200(string(fixture(t, "ollama-route-response.json"))),
			reply200(string(fixture(t, "ollama-activity-response.json"))),
		)
		advice, err := ollamaClient(rec).Ask(t.Context(), smallInput)
		if err != nil {
			t.Fatal(err)
		}
		if advice.Choice != "medium" || advice.Activity == nil || advice.Activity.Choice != config.ActivityCode {
			t.Errorf("advice = %+v, activity = %+v", advice, advice.Activity)
		}
		if rec.count() != 2 {
			t.Fatalf("requests = %d, want 2", rec.count())
		}
		for _, req := range rec.requests {
			if _, ok := req.Header["Authorization"]; ok {
				t.Error("keyless classifier sent an Authorization header")
			}
		}
		if !strings.Contains(rec.bodies[1], "Which activity") {
			t.Errorf("second request is not the activity question")
		}
	})
}

func TestMissingKeySendsNothingAndCountsNoFailure(t *testing.T) {
	rec := script(reply200(jevAnswer))
	c := jevClient(rec)
	c.key = ""
	if _, err := c.Ask(t.Context(), smallInput); !errors.Is(err, ErrMissingKey) {
		t.Errorf("err = %v, want %v", err, ErrMissingKey)
	}
	if rec.count() != 0 || c.Health().Failures != 0 {
		t.Errorf("requests = %d, health = %+v", rec.count(), c.Health())
	}
}

func TestTheDeadlineIsTheClassifierTimeout(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := jevClient(script(untilCancelled))
		start := time.Now()
		_, err := c.Ask(t.Context(), smallInput)
		if !errors.Is(err, ErrTimeout) {
			t.Errorf("err = %v, want %v", err, ErrTimeout)
		}
		if elapsed := time.Since(start); elapsed != testTimeout {
			t.Errorf("returned after %v, want %v", elapsed, testTimeout)
		}
		if c.Health().Failures != 1 {
			t.Errorf("failures = %d, want 1", c.Health().Failures)
		}
	})
}

func TestATimedOutRequestHoldsTheSlotUntilItSettles(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		release := make(chan struct{})
		rec := script(func(*http.Request) (*http.Response, error) {
			<-release
			return respond(http.StatusOK, jevAnswer), nil
		})
		c := jevClient(rec)
		if _, err := c.Ask(t.Context(), smallInput); !errors.Is(err, ErrTimeout) {
			t.Fatalf("err = %v, want %v", err, ErrTimeout)
		}
		for range 3 {
			if _, err := c.Ask(t.Context(), smallInput); !errors.Is(err, ErrBusy) {
				t.Errorf("err = %v, want %v", err, ErrBusy)
			}
		}
		if rec.count() != 1 {
			t.Errorf("requests = %d, want 1", rec.count())
		}
		close(release)
		synctest.Wait()
		if c.Health().Failures != 1 {
			t.Errorf("a late answer changed health: %+v", c.Health())
		}
		if _, err := c.Ask(t.Context(), smallInput); err != nil {
			t.Errorf("after settlement err = %v", err)
		}
		if c.Health().Failures != 0 {
			t.Errorf("failures = %d, want 0 after a success", c.Health().Failures)
		}
	})
}

func TestRetryWaitsForRetryAfterInsideTheDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		rec := script(status(http.StatusServiceUnavailable, "Retry-After", "0.2"), reply200(jevAnswer))
		if _, err := jevClient(rec).Ask(t.Context(), smallInput); err != nil {
			t.Fatal(err)
		}
		if rec.count() != 2 {
			t.Fatalf("requests = %d, want 2", rec.count())
		}
		if gap := rec.times[1].Sub(rec.times[0]); gap != 200*time.Millisecond {
			t.Errorf("retry after %v, want 200ms", gap)
		}
	})
}

func TestARetryBeyondTheBudgetIsNotSent(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		rec := script(status(http.StatusServiceUnavailable, "Retry-After", "10"))
		start := time.Now()
		if _, err := jevClient(rec).Ask(t.Context(), smallInput); !errors.Is(err, ErrTimeout) {
			t.Errorf("err = %v, want %v", err, ErrTimeout)
		}
		if waited := time.Since(start); waited != 0 {
			t.Errorf("waited %v for a retry it could not send", waited)
		}
		if rec.count() != 1 {
			t.Errorf("requests = %d, want 1", rec.count())
		}
	})
}

func TestTheSlotStaysTakenDuringBackoff(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		rec := script(status(http.StatusServiceUnavailable), reply200(jevAnswer))
		c := jevClient(rec)
		done := make(chan error)
		go func() {
			_, err := c.Ask(t.Context(), smallInput)
			done <- err
		}()
		synctest.Wait()
		if _, err := c.Ask(t.Context(), smallInput); !errors.Is(err, ErrBusy) {
			t.Errorf("err during backoff = %v, want %v", err, ErrBusy)
		}
		if err := <-done; err != nil {
			t.Errorf("first ask err = %v", err)
		}
		if rec.count() != 2 {
			t.Errorf("requests = %d, want 2", rec.count())
		}
		if gap := rec.times[1].Sub(rec.times[0]); gap != RetryDelay {
			t.Errorf("retry after %v, want %v", gap, RetryDelay)
		}
	})
}

func TestStatusCodes(t *testing.T) {
	for _, tc := range []struct {
		name     string
		steps    []func(*http.Request) (*http.Response, error)
		err      error
		requests int
	}{
		{"unauthorized", steps(status(http.StatusUnauthorized)), ErrAuth, 1},
		{"forbidden", steps(status(http.StatusForbidden)), ErrAuth, 1},
		{"not found", steps(status(http.StatusNotFound)), ErrHTTP, 1},
		{"transient twice", steps(status(http.StatusBadGateway), status(http.StatusTooManyRequests)), ErrHTTP, 2},
		{"transient then ok", steps(status(http.StatusInternalServerError), reply200(jevAnswer)), nil, 2},
		{"refused twice", steps(refused), ErrUnreachable, 2},
		{"refused then ok", steps(refused, reply200(jevAnswer)), nil, 2},
		{"not json", steps(reply200("PRIVATE_PROMPT")), ErrMalformed, 1},
		{"unknown choice", steps(reply200(`{"answers":{"route":{"type":"choice","choice":"PRIVATE","probabilities":{}}}}`)), ErrMalformed, 1},
		{"oversized answer", steps(reply200(jevAnswer + strings.Repeat(" ", MaxAnswerBytes))), ErrMalformed, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				rec := script(tc.steps...)
				c := jevClient(rec)
				_, err := c.Ask(t.Context(), smallInput)
				if !errors.Is(err, tc.err) {
					t.Errorf("err = %v, want %v", err, tc.err)
				}
				if rec.count() != tc.requests {
					t.Errorf("requests = %d, want %d", rec.count(), tc.requests)
				}
				wantFailures := 0
				if tc.err != nil {
					wantFailures = 1
				}
				if c.Health().Failures != wantFailures {
					t.Errorf("failures = %d, want %d", c.Health().Failures, wantFailures)
				}
			})
		})
	}
}

func steps(s ...func(*http.Request) (*http.Response, error)) []func(*http.Request) (*http.Response, error) {
	return s
}

func TestCancellationIsNotAnOutage(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := jevClient(script(untilCancelled))
		ctx, cancel := context.WithCancel(t.Context())
		go func() {
			time.Sleep(100 * time.Millisecond)
			cancel()
		}()
		if _, err := c.Ask(ctx, smallInput); !errors.Is(err, ErrCancelled) {
			t.Errorf("err = %v, want %v", err, ErrCancelled)
		}
		if c.Health().Failures != 0 {
			t.Errorf("failures = %d, want 0", c.Health().Failures)
		}
		if _, err := c.Ask(ctx, smallInput); !errors.Is(err, ErrCancelled) {
			t.Errorf("ask on a done context err = %v, want %v", err, ErrCancelled)
		}
	})
}

func TestThreeFailuresPauseThenOneSuccessResumes(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		denied := status(http.StatusUnauthorized)
		rec := script(denied, denied, denied, reply200(jevAnswer))
		c := jevClient(rec)
		for range FailuresToPause {
			if _, err := c.Ask(t.Context(), smallInput); !errors.Is(err, ErrAuth) {
				t.Fatalf("err = %v, want %v", err, ErrAuth)
			}
		}
		if _, err := c.Ask(t.Context(), smallInput); !errors.Is(err, ErrPaused) {
			t.Errorf("err = %v, want %v", err, ErrPaused)
		}
		if rec.count() != FailuresToPause {
			t.Errorf("requests = %d, want %d", rec.count(), FailuresToPause)
		}
		if h := c.Health(); h.Failures != FailuresToPause || !h.PausedUntil.Equal(time.Now().Add(PauseDuration)) {
			t.Errorf("health = %+v", h)
		}

		time.Sleep(PauseDuration)
		if _, err := c.Ask(t.Context(), smallInput); err != nil {
			t.Fatalf("after the pause err = %v", err)
		}
		if h := c.Health(); h.Failures != 0 || !h.PausedUntil.IsZero() {
			t.Errorf("health = %+v, want reset", h)
		}
	})
}

func TestLatencyIsReported(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := jevClient(script(after(300*time.Millisecond, jevAnswer)))
		advice, err := c.Ask(t.Context(), smallInput)
		if err != nil {
			t.Fatal(err)
		}
		if advice.Latency != 300*time.Millisecond || c.Health().Latency != 300*time.Millisecond {
			t.Errorf("latency = %v, health = %+v", advice.Latency, c.Health())
		}
	})
}

func TestAFailedOllamaActivityStepKeepsTheRouteAdvice(t *testing.T) {
	for name, step := range map[string]func(*http.Request) (*http.Response, error){
		"transport error": refused,
		"http error":      status(http.StatusInternalServerError),
		"not json":        reply200("A"),
		"no letters":      reply200(`{"logprobs":[{"top_logprobs":[]}]}`),
	} {
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				rec := script(reply200(string(fixture(t, "ollama-route-response.json"))), step)
				c := ollamaClient(rec)
				advice, err := c.Ask(t.Context(), smallInput)
				if err != nil || advice.Choice != "medium" || advice.Activity != nil {
					t.Errorf("advice = %+v, err = %v", advice, err)
				}
				if rec.count() != 2 || c.Health().Failures != 0 {
					t.Errorf("requests = %d, health = %+v", rec.count(), c.Health())
				}
			})
		})
	}
}

func TestOllamaActivityStepNeedsItsMinimumTimeLeft(t *testing.T) {
	for _, tc := range []struct {
		left time.Duration
		sent bool
	}{
		{ActivityStepMin - time.Millisecond, false},
		{ActivityStepMin, true},
	} {
		t.Run(tc.left.String(), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				rec := script(
					after(testTimeout-tc.left, string(fixture(t, "ollama-route-response.json"))),
					reply200(string(fixture(t, "ollama-activity-response.json"))),
				)
				advice, err := ollamaClient(rec).Ask(t.Context(), smallInput)
				if err != nil {
					t.Fatal(err)
				}
				wantRequests := 1
				if tc.sent {
					wantRequests = 2
				}
				if rec.count() != wantRequests || (advice.Activity != nil) != tc.sent {
					t.Errorf("requests = %d, activity = %+v", rec.count(), advice.Activity)
				}
			})
		})
	}
}

func TestATimedOutOllamaActivityStepKeepsTheRouteAndHoldsTheSlot(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		release := make(chan struct{})
		rec := script(
			reply200(string(fixture(t, "ollama-route-response.json"))),
			func(*http.Request) (*http.Response, error) {
				<-release
				return respond(http.StatusOK, string(fixture(t, "ollama-activity-response.json"))), nil
			},
		)
		c := ollamaClient(rec)
		advice, err := c.Ask(t.Context(), smallInput)
		if err != nil || advice.Choice != "medium" || advice.Activity != nil {
			t.Errorf("advice = %+v, err = %v", advice, err)
		}
		if c.Health().Failures != 0 {
			t.Errorf("failures = %d, want 0", c.Health().Failures)
		}
		if _, err := c.Ask(t.Context(), smallInput); !errors.Is(err, ErrBusy) {
			t.Errorf("err = %v, want %v", err, ErrBusy)
		}
		close(release)
		synctest.Wait()
		if _, err := c.Ask(t.Context(), smallInput); err != nil {
			t.Errorf("after settlement err = %v", err)
		}
	})
}

func TestCancellationDuringTheOllamaActivityStep(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		rec := script(
			reply200(string(fixture(t, "ollama-route-response.json"))),
			func(r *http.Request) (*http.Response, error) {
				cancel()
				return untilCancelled(r)
			},
		)
		c := ollamaClient(rec)
		if _, err := c.Ask(ctx, smallInput); !errors.Is(err, ErrCancelled) {
			t.Errorf("err = %v, want %v", err, ErrCancelled)
		}
		if c.Health().Failures != 0 {
			t.Errorf("failures = %d, want 0", c.Health().Failures)
		}
	})
}

func TestRetryAfter(t *testing.T) {
	now := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		value string
		want  time.Duration
		ok    bool
	}{
		{"", 0, false},
		{"   ", 0, false},
		{"0", 0, true},
		{"0.2", 200 * time.Millisecond, true},
		{"3", 3 * time.Second, true},
		{"-1", 0, false},
		{"soon", 0, false},
		{"Inf", 0, false},
		{now.Add(2 * time.Second).Format(http.TimeFormat), 2 * time.Second, true},
		{now.Add(-time.Minute).Format(http.TimeFormat), 0, true},
	} {
		got, ok := retryAfter(tc.value, now)
		if got != tc.want || ok != tc.ok {
			t.Errorf("retryAfter(%q) = %v, %v; want %v, %v", tc.value, got, ok, tc.want, tc.ok)
		}
	}
}
