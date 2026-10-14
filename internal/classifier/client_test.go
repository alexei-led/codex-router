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
	"unicode/utf16"

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

func jevClient(doer Doer) *Client { return jevClientAt(doer, time.Now) }

func jevClientAt(doer Doer, now func() time.Time) *Client {
	cfg := config.Classifier{API: config.APISystemOne, Endpoint: testEndpoint, Model: "jev-1.13.0", KeyEnv: "TYPESAFE_API_KEY", TimeoutMs: int(testTimeout / time.Millisecond)}
	return New(cfg, testKey, SystemOne(), doer, now)
}

func ollamaClient(doer Doer) *Client { return ollamaClientAt(doer, time.Now) }

func ollamaClientAt(doer Doer, now func() time.Time) *Client {
	cfg := config.Classifier{API: config.APIOllama, Endpoint: testEndpoint, Model: "qwen3.5:9b", TimeoutMs: int(testTimeout / time.Millisecond)}
	return New(cfg, "", Ollama(), doer, now)
}

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func clockAt(year int) *fakeClock {
	return &fakeClock{t: time.Date(year, 1, 1, 0, 0, 0, 0, time.UTC)}
}

func (f *fakeClock) now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.t
}

func (f *fakeClock) advance(d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.t = f.t.Add(d)
}

func advancing(clk *fakeClock, d time.Duration, body string) func(*http.Request) (*http.Response, error) {
	return func(*http.Request) (*http.Response, error) {
		clk.advance(d)
		return respond(http.StatusOK, body), nil
	}
}

func assertNoForeignCredential(t *testing.T, body string) {
	t.Helper()
	lower := strings.ToLower(body)
	for _, forbidden := range []string{"authorization", "chatgpt", "bearer", strings.ToLower(testKey)} {
		if strings.Contains(lower, forbidden) {
			t.Errorf("body contains %q", forbidden)
		}
	}
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
		assertNoForeignCredential(t, rec.bodies[0])
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
		for i, req := range rec.requests {
			if _, ok := req.Header["Authorization"]; ok {
				t.Error("keyless classifier sent an Authorization header")
			}
			assertNoForeignCredential(t, rec.bodies[i])
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
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()
		if _, err := c.Ask(ctx, smallInput); !errors.Is(err, ErrCancelled) {
			t.Errorf("err = %v, want %v", err, ErrCancelled)
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

func TestAnAskWaitsForTheSlot(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		release := make(chan struct{})
		rec := script(func(*http.Request) (*http.Response, error) {
			<-release
			return respond(http.StatusOK, jevAnswer), nil
		})
		c := jevClient(rec)
		review, cancel := context.WithCancel(t.Context())
		reviewed := make(chan error)
		go func() {
			_, err := c.Ask(review, smallInput)
			reviewed <- err
		}()
		synctest.Wait()
		cancel()
		if err := <-reviewed; !errors.Is(err, ErrCancelled) {
			t.Fatalf("cancelled ask err = %v, want %v", err, ErrCancelled)
		}

		asked := make(chan error)
		go func() {
			_, err := c.Ask(t.Context(), smallInput)
			asked <- err
		}()
		synctest.Wait()
		if rec.count() != 1 {
			t.Errorf("requests while the slot is held = %d, want 1", rec.count())
		}
		close(release)
		if err := <-asked; err != nil {
			t.Errorf("waiting ask err = %v", err)
		}
		if rec.count() != 2 {
			t.Errorf("requests = %d, want 2", rec.count())
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
		ctx, cancel := context.WithTimeout(t.Context(), RetryDelay/2)
		defer cancel()
		if _, err := c.Ask(ctx, smallInput); !errors.Is(err, ErrCancelled) {
			t.Errorf("err during backoff = %v, want %v", err, ErrCancelled)
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
		{"oversized answer", steps(reply200(jevAnswer + strings.Repeat(" ", MaxAnswerChars))), ErrMalformed, 1},
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

func TestTheBudgetRunsOnTheInjectedClock(t *testing.T) {
	for _, tc := range []struct {
		left     time.Duration
		err      error
		requests int
	}{
		{0, ErrTimeout, 1},
		{ActivityStepMin - time.Millisecond, nil, 1},
		{ActivityStepMin, nil, 2},
	} {
		t.Run(tc.left.String(), func(t *testing.T) {
			clk := clockAt(2001)
			rec := script(
				advancing(clk, testTimeout-tc.left, string(fixture(t, "ollama-route-response.json"))),
				reply200(string(fixture(t, "ollama-activity-response.json"))),
			)
			advice, err := ollamaClientAt(rec, clk.now).Ask(t.Context(), smallInput)
			if !errors.Is(err, tc.err) {
				t.Fatalf("err = %v, want %v", err, tc.err)
			}
			if rec.count() != tc.requests {
				t.Errorf("requests = %d, want %d", rec.count(), tc.requests)
			}
			if tc.err == nil && (advice.Activity != nil) != (tc.requests == 2) {
				t.Errorf("activity = %+v", advice.Activity)
			}
		})
	}
}

func TestThePauseRunsOnTheInjectedClock(t *testing.T) {
	clk := clockAt(2001)
	denied := status(http.StatusUnauthorized)
	rec := script(denied, denied, denied, reply200(jevAnswer))
	c := jevClientAt(rec, clk.now)
	for range FailuresToPause {
		if _, err := c.Ask(t.Context(), smallInput); !errors.Is(err, ErrAuth) {
			t.Fatalf("err = %v, want %v", err, ErrAuth)
		}
	}
	if h := c.Health(); !h.PausedUntil.Equal(clk.now().Add(PauseDuration)) {
		t.Errorf("paused until %v, want %v", h.PausedUntil, clk.now().Add(PauseDuration))
	}
	clk.advance(PauseDuration - time.Nanosecond)
	if _, err := c.Ask(t.Context(), smallInput); !errors.Is(err, ErrPaused) {
		t.Errorf("err = %v, want %v", err, ErrPaused)
	}
	clk.advance(time.Nanosecond)
	if _, err := c.Ask(t.Context(), smallInput); err != nil {
		t.Fatalf("after the pause err = %v", err)
	}
	if h := c.Health(); h.Failures != 0 || !h.PausedUntil.IsZero() {
		t.Errorf("health = %+v, want reset", h)
	}
	if rec.count() != FailuresToPause+1 {
		t.Errorf("requests = %d, want %d", rec.count(), FailuresToPause+1)
	}
}

func TestAFrozenFutureClockStillTimesOut(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := jevClientAt(script(untilCancelled), clockAt(2100).now)
		start := time.Now()
		if _, err := c.Ask(t.Context(), smallInput); !errors.Is(err, ErrTimeout) {
			t.Errorf("err = %v, want %v", err, ErrTimeout)
		}
		if elapsed := time.Since(start); elapsed != testTimeout {
			t.Errorf("returned after %v, want %v", elapsed, testTimeout)
		}
	})
}

func paddedAnswer(r rune, units int) string {
	prefix, suffix := `{"note":"`, `",`+jevAnswer[1:]
	free := units - len(prefix) - len(suffix)
	n := utf16.RuneLen(r)
	return prefix + strings.Repeat(string(r), free/n) + strings.Repeat("a", free%n) + suffix
}

func TestAnswerLengthCountsUTF16Units(t *testing.T) {
	for _, tc := range []struct {
		name   string
		answer string
		err    error
	}{
		{"two-byte runes at the limit", paddedAnswer('é', MaxAnswerChars), nil},
		{"two-byte runes over the limit", paddedAnswer('é', MaxAnswerChars+1), ErrMalformed},
		{"three-byte runes at the limit", paddedAnswer('€', MaxAnswerChars), nil},
		{"astral runes at the limit", paddedAnswer('😀', MaxAnswerChars), nil},
		{"astral runes over the limit", paddedAnswer('😀', MaxAnswerChars+1), ErrMalformed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := jevClient(script(reply200(tc.answer))).Ask(t.Context(), smallInput)
			if !errors.Is(err, tc.err) {
				t.Errorf("err = %v, want %v", err, tc.err)
			}
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
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()
		if _, err := c.Ask(ctx, smallInput); !errors.Is(err, ErrCancelled) {
			t.Errorf("err = %v, want %v", err, ErrCancelled)
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
