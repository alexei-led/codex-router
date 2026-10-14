package proxy

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alexei-led/codex-router/internal/catalog"
	"github.com/alexei-led/codex-router/internal/config"
	"github.com/alexei-led/codex-router/internal/responses"
)

const (
	testAuth    = "Bearer sk-test-secret-token-0123456789"
	testAccount = "acct-secret-4567"
	promptText  = "PROMPT-TEXT-MARKER fix the flaky test"
	turnMeta    = `{"session_id":"s-1","thread_id":"th-1","turn_id":"turn-1","agent_name":"main"}`
	upstreamDir = "/backend-api/codex"
)

var requestBody = []byte(`{"model":"gpt-6-sol","reasoning":{"effort":"medium"},"input":[` +
	`{"type":"message","role":"user","content":[{"type":"input_text","text":"` + promptText + `"}]}],"stream":true}`)

const (
	evCreated   = "event: response.created\ndata: {\"type\":\"response.created\"}\n\n"
	evItem      = "event: response.output_item.done\ndata: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"message\",\"content\":[{\"type\":\"output_text\",\"text\":\"ANSWER-TEXT\"}]}}\n\n"
	evCompleted = "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"model\":\"gpt-6-luna\"," +
		"\"usage\":{\"input_tokens\":1200,\"input_tokens_details\":{\"cached_tokens\":800},\"output_tokens\":90," +
		"\"output_tokens_details\":{\"reasoning_tokens\":40},\"total_tokens\":1290}}}\n\n"
)

var wantUsage = responses.Usage{InputTokens: 1200, CachedInputTokens: 800, OutputTokens: 90, ReasoningTokens: 40, TotalTokens: 1290}

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

type seen struct {
	method, path, query string
	header              http.Header
	body                []byte
	framing             string
}

type upstream struct {
	srv  *httptest.Server
	hits atomic.Int32
	mu   sync.Mutex
	reqs []seen
}

func newUpstream(t *testing.T, h http.HandlerFunc) *upstream {
	t.Helper()
	u := &upstream{}
	u.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u.hits.Add(1)
		body, _ := io.ReadAll(r.Body)
		u.mu.Lock()
		framing := fmt.Sprintf("cl=%d te=%v", r.ContentLength, r.TransferEncoding)
		u.reqs = append(u.reqs, seen{r.Method, r.URL.Path, r.URL.RawQuery, r.Header.Clone(), body, framing})
		u.mu.Unlock()
		h(w, r)
	}))
	t.Cleanup(u.srv.Close)
	return u
}

func (u *upstream) url() string { return u.srv.URL + upstreamDir }

func (u *upstream) last(t *testing.T) seen {
	t.Helper()
	u.mu.Lock()
	defer u.mu.Unlock()
	if len(u.reqs) == 0 {
		t.Fatal("upstream got no request")
	}
	return u.reqs[len(u.reqs)-1]
}

func sseUpstream(t *testing.T, events ...string) *upstream {
	return newUpstream(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for _, ev := range events {
			_, _ = io.WriteString(w, ev)
			w.(http.Flusher).Flush()
		}
	})
}

type harness struct {
	base string
	logs *syncBuffer
}

// startProxy runs a proxy against upstreamURL and, on cleanup, fails the test
// if any secret, prompt or answer text reached the log.
func startProxy(t *testing.T, upstreamURL string, opts ...func(*Options)) harness {
	t.Helper()
	logs := &syncBuffer{}
	o := Options{
		Upstream: upstreamURL,
		Logger:   slog.New(slog.NewJSONHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
	}
	for _, f := range opts {
		f(&o)
	}
	s := New(o)
	port, err := s.Listen()
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Serve(ctx) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("Serve: %v", err)
		}
		out := logs.String()
		for _, secret := range []string{testAuth, "sk-test-secret", testAccount, "PROMPT-TEXT-MARKER", "ANSWER-TEXT"} {
			if strings.Contains(out, secret) {
				t.Errorf("log contains %q:\n%s", secret, out)
			}
		}
	})
	return harness{base: fmt.Sprintf("http://127.0.0.1:%d", port), logs: logs}
}

func newRequest(t *testing.T, ctx context.Context, method, url string, body []byte) *http.Request {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, method, url, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", testAuth)
	req.Header.Set("ChatGPT-Account-ID", testAccount)
	req.Header.Set(responses.HeaderTurnMetadata, turnMeta)
	req.Header.Set("Accept", "text/event-stream")
	return req
}

func postResponses(t *testing.T, h harness, body []byte) (*http.Response, []byte) {
	t.Helper()
	resp, err := http.DefaultClient.Do(newRequest(t, t.Context(), http.MethodPost, h.base+PathResponses, body))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	got, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	return resp, got
}

type deciderFunc func(ctx context.Context, req *responses.Request, meta responses.Meta) (responses.Rewrite, bool)

func (f deciderFunc) Decide(ctx context.Context, req *responses.Request, meta responses.Meta) (responses.Rewrite, bool) {
	return f(ctx, req, meta)
}

type observation struct {
	meta   responses.Meta
	served string
	usage  responses.Usage
	status int
}

type recorder struct {
	mu  sync.Mutex
	obs []observation
}

func (r *recorder) Observe(meta responses.Meta, served string, usage responses.Usage, status int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.obs = append(r.obs, observation{meta, served, usage, status})
}

func (r *recorder) all() []observation {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]observation(nil), r.obs...)
}

func TestRewriteForwardsHeadersAndStreams(t *testing.T) {
	up := newUpstream(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Openai-Model", "gpt-6-luna")
		w.Header().Set("X-Openai-Model", "gpt-6-luna")
		w.Header().Set("X-Request-Id", "req-9")
		_, _ = io.WriteString(w, evCreated+evItem+evCompleted)
	})
	var decided responses.Meta
	var decidedModel string
	obs := &recorder{}
	h := startProxy(t, up.url(), func(o *Options) {
		o.Decider = deciderFunc(func(_ context.Context, req *responses.Request, meta responses.Meta) (responses.Rewrite, bool) {
			decided, decidedModel = meta, req.Model
			return responses.Rewrite{Model: "gpt-6-luna", Effort: config.Effort("low")}, true
		})
		o.Observer = obs
	})

	req := newRequest(t, t.Context(), http.MethodPost, h.base+PathResponses, requestBody)
	req.Header.Set("Accept-Encoding", "zstd")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()

	if resp.StatusCode != http.StatusOK || string(got) != evCreated+evItem+evCompleted {
		t.Fatalf("client got %d %q", resp.StatusCode, got)
	}
	for _, k := range []string{"Openai-Model", "X-Openai-Model"} {
		if v := resp.Header.Get(k); v != "" {
			t.Errorf("response header %s = %q, want stripped", k, v)
		}
	}
	if resp.Header.Get("X-Request-Id") != "req-9" {
		t.Errorf("X-Request-Id not passed through: %v", resp.Header)
	}

	in := up.last(t)
	if in.method != http.MethodPost || in.path != upstreamDir+"/responses" {
		t.Errorf("upstream got %s %s", in.method, in.path)
	}
	if in.header.Get("Authorization") != testAuth || in.header.Get("Chatgpt-Account-Id") != testAccount {
		t.Errorf("auth headers not forwarded verbatim")
	}
	if in.header.Get(responses.HeaderTurnMetadata) != turnMeta {
		t.Errorf("turn metadata = %q", in.header.Get(responses.HeaderTurnMetadata))
	}
	if v := in.header.Get("Accept-Encoding"); v != "" {
		t.Errorf("upstream Accept-Encoding = %q, want none", v)
	}
	var sent struct {
		Model     string `json:"model"`
		Reasoning struct {
			Effort string `json:"effort"`
		} `json:"reasoning"`
	}
	if err := json.Unmarshal(in.body, &sent); err != nil {
		t.Fatalf("upstream body: %v", err)
	}
	if sent.Model != "gpt-6-luna" || sent.Reasoning.Effort != "low" || !bytes.Contains(in.body, []byte(promptText)) {
		t.Errorf("upstream body not rewritten as expected: %s", in.body)
	}

	if decidedModel != "gpt-6-sol" || decided.ThreadID != "th-1" || decided.TurnID != "turn-1" {
		t.Errorf("decider got model %q meta %+v", decidedModel, decided)
	}
	if strings.Contains(fmt.Sprintf("%+v", decided), "secret") {
		t.Errorf("decider meta carries a secret: %+v", decided)
	}
	want := []observation{{meta: decided, served: "gpt-6-luna", usage: wantUsage, status: http.StatusOK}}
	if got := obs.all(); len(got) != 1 || got[0] != want[0] {
		t.Errorf("observations = %+v, want %+v", got, want)
	}
}

func TestFailOpenForwardsOriginalBytes(t *testing.T) {
	block := make(chan struct{})
	t.Cleanup(func() { close(block) })
	tests := []struct {
		name    string
		body    []byte
		decider Decider
		wantLog string
	}{
		{
			name: "decider declines",
			body: requestBody,
			decider: deciderFunc(func(context.Context, *responses.Request, responses.Meta) (responses.Rewrite, bool) {
				return responses.Rewrite{}, false
			}),
		},
		{
			name: "decider panics",
			body: requestBody,
			decider: deciderFunc(func(context.Context, *responses.Request, responses.Meta) (responses.Rewrite, bool) {
				panic("boom " + testAuth)
			}),
			wantLog: "decide-panic",
		},
		{
			name: "decider ignores deadline",
			body: requestBody,
			decider: deciderFunc(func(context.Context, *responses.Request, responses.Meta) (responses.Rewrite, bool) {
				<-block
				return responses.Rewrite{Model: "gpt-6-luna"}, true
			}),
			wantLog: "decide-timeout",
		},
		{
			name: "unparseable body",
			body: []byte(`{"model": PROMPT-TEXT-MARKER`),
			decider: deciderFunc(func(context.Context, *responses.Request, responses.Meta) (responses.Rewrite, bool) {
				t.Error("decider called for an unparseable body")
				return responses.Rewrite{}, false
			}),
			wantLog: "parse-error",
		},
		{name: "no decider", body: requestBody},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			up := sseUpstream(t, evCreated, evCompleted)
			h := startProxy(t, up.url(), func(o *Options) {
				o.Decider = tt.decider
				o.DecideTimeout = 50 * time.Millisecond
			})

			resp, got := postResponses(t, h, tt.body)

			if resp.StatusCode != http.StatusOK || string(got) != evCreated+evCompleted {
				t.Fatalf("client got %d %q", resp.StatusCode, got)
			}
			if in := up.last(t); !bytes.Equal(in.body, tt.body) {
				t.Errorf("upstream body = %q, want original %q", in.body, tt.body)
			}
			if tt.wantLog != "" && !strings.Contains(h.logs.String(), tt.wantLog) {
				t.Errorf("log lacks %q:\n%s", tt.wantLog, h.logs.String())
			}
		})
	}
}

func TestDecideGetsDeadline(t *testing.T) {
	up := sseUpstream(t, evCompleted)
	var left time.Duration
	var hasDeadline bool
	h := startProxy(t, up.url(), func(o *Options) {
		o.DecideTimeout = 300 * time.Millisecond
		o.Decider = deciderFunc(func(ctx context.Context, _ *responses.Request, _ responses.Meta) (responses.Rewrite, bool) {
			var dl time.Time
			dl, hasDeadline = ctx.Deadline()
			left = time.Until(dl)
			return responses.Rewrite{}, false
		})
	})

	postResponses(t, h, requestBody)

	if !hasDeadline || left <= 0 || left > 300*time.Millisecond {
		t.Errorf("decide ctx deadline: has=%v left=%v, want within 300ms", hasDeadline, left)
	}
}

func TestUpstreamFailureBeforeResponseIsNotRetried(t *testing.T) {
	dead := httptest.NewServer(http.NotFoundHandler())
	deadURL := dead.URL + upstreamDir
	dead.Close()

	tests := []struct {
		name       string
		upstream   func(t *testing.T) (url string, hits func() int32)
		wantStatus int
		wantHits   int32
	}{
		{
			name:       "listener dead",
			upstream:   func(*testing.T) (string, func() int32) { return deadURL, nil },
			wantStatus: http.StatusBadGateway,
		},
		{
			name: "connection dropped before headers",
			upstream: func(t *testing.T) (string, func() int32) {
				up := newUpstream(t, func(http.ResponseWriter, *http.Request) { panic(http.ErrAbortHandler) })
				return up.url(), up.hits.Load
			},
			wantStatus: http.StatusBadGateway,
			wantHits:   1,
		},
		{
			name: "upstream 500",
			upstream: func(t *testing.T) (string, func() int32) {
				up := newUpstream(t, func(w http.ResponseWriter, _ *http.Request) {
					http.Error(w, `{"error":"overloaded"}`, http.StatusInternalServerError)
				})
				return up.url(), up.hits.Load
			},
			wantStatus: http.StatusInternalServerError,
			wantHits:   1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			url, hits := tt.upstream(t)
			obs := &recorder{}
			h := startProxy(t, url, func(o *Options) { o.Observer = obs })

			resp, _ := postResponses(t, h, requestBody)

			if resp.StatusCode != tt.wantStatus {
				t.Errorf("status = %d, want %d", resp.StatusCode, tt.wantStatus)
			}
			if hits != nil {
				if got := hits(); got != tt.wantHits {
					t.Errorf("upstream hits = %d, want %d", got, tt.wantHits)
				}
			}
			wantObs := 0
			if tt.wantStatus != http.StatusBadGateway {
				wantObs = 1
			}
			if got := obs.all(); len(got) != wantObs || (wantObs == 1 && got[0].status != tt.wantStatus) {
				t.Errorf("observations = %+v", got)
			}
		})
	}
}

// dyingUpstream answers the first request on each connection and closes the
// connection after reading the second one, so the second request reached
// upstream but got no response.
type dyingUpstream struct {
	ln    net.Listener
	reqs  atomic.Int32
	mu    sync.Mutex
	conns []net.Conn
}

func newDyingUpstream(t *testing.T) *dyingUpstream {
	t.Helper()
	ln, err := net.Listen("tcp", DefaultAddr)
	if err != nil {
		t.Fatal(err)
	}
	u := &dyingUpstream{ln: ln}
	t.Cleanup(func() {
		_ = ln.Close()
		u.mu.Lock()
		defer u.mu.Unlock()
		for _, c := range u.conns {
			_ = c.Close()
		}
	})
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			u.mu.Lock()
			u.conns = append(u.conns, c)
			u.mu.Unlock()
			go u.serve(c)
		}
	}()
	return u
}

func (u *dyingUpstream) serve(c net.Conn) {
	defer func() { _ = c.Close() }()
	br := bufio.NewReader(c)
	for n := 1; ; n++ {
		req, err := http.ReadRequest(br)
		if err != nil {
			return
		}
		_, _ = io.Copy(io.Discard, req.Body)
		u.reqs.Add(1)
		if n > 1 {
			return
		}
		_, _ = fmt.Fprintf(c, "HTTP/1.1 200 OK\r\nContent-Type: text/event-stream\r\nContent-Length: %d\r\n\r\n%s",
			len(evCompleted), evCompleted)
	}
}

func (u *dyingUpstream) url() string { return "http://" + u.ln.Addr().String() + upstreamDir }

func TestNoResendWhenReusedConnectionDies(t *testing.T) {
	tests := []struct {
		name           string
		method, path   string
		body           []byte
		idempotencyKey string
	}{
		{name: "models", method: http.MethodGet, path: PathModels + "?client_version=1"},
		{name: "responses", method: http.MethodPost, path: PathResponses, body: requestBody},
		{name: "empty responses with idempotency key", method: http.MethodPost, path: PathResponses, body: []byte{}, idempotencyKey: "k-1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			up := newDyingUpstream(t)
			h := startProxy(t, up.url())
			send := func() int {
				req := newRequest(t, t.Context(), tt.method, h.base+tt.path, tt.body)
				if tt.idempotencyKey != "" {
					req.Header.Set("Idempotency-Key", tt.idempotencyKey)
				}
				resp, err := http.DefaultClient.Do(req)
				if err != nil {
					t.Fatal(err)
				}
				_, _ = io.Copy(io.Discard, resp.Body)
				_ = resp.Body.Close()
				return resp.StatusCode
			}

			if got := send(); got != http.StatusOK {
				t.Fatalf("first request: status = %d, want 200", got)
			}
			if got := send(); got != http.StatusBadGateway {
				t.Errorf("second request: status = %d, want 502", got)
			}
			if n := up.reqs.Load(); n != 2 {
				t.Errorf("upstream read %d requests, want 2 (the second must not be re-sent)", n)
			}
		})
	}
}

type recordingTransport struct {
	mu   sync.Mutex
	urls []string
}

func (rt *recordingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	rt.mu.Lock()
	rt.urls = append(rt.urls, r.URL.String())
	rt.mu.Unlock()
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": {"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(evCompleted)),
		Request:    r,
	}, nil
}

func TestDefaultUpstreamIsChatGPTCodexBackend(t *testing.T) {
	rt := &recordingTransport{}
	h := startProxy(t, "", func(o *Options) { o.Client = &http.Client{Transport: rt} })

	postResponses(t, h, requestBody)
	resp, err := http.DefaultClient.Do(newRequest(t, t.Context(), http.MethodGet, h.base+PathModels+"?client_version=1", nil))
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()

	want := []string{
		"https://chatgpt.com/backend-api/codex/responses",
		"https://chatgpt.com/backend-api/codex/models?client_version=1",
	}
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if fmt.Sprint(rt.urls) != fmt.Sprint(want) {
		t.Errorf("upstream URLs = %v, want %v", rt.urls, want)
	}
}

func TestUpstreamClosesAfterFirstItem(t *testing.T) {
	up := newUpstream(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, evCreated)
		w.(http.Flusher).Flush()
		panic(http.ErrAbortHandler)
	})
	obs := &recorder{}
	h := startProxy(t, up.url(), func(o *Options) { o.Observer = obs })

	resp, err := http.DefaultClient.Do(newRequest(t, t.Context(), http.MethodPost, h.base+PathResponses, requestBody))
	if err != nil {
		t.Fatal(err)
	}
	got, readErr := io.ReadAll(resp.Body)
	_ = resp.Body.Close()

	if resp.StatusCode != http.StatusOK || string(got) != evCreated {
		t.Errorf("client got %d %q, want 200 and the first event", resp.StatusCode, got)
	}
	if readErr == nil {
		t.Error("stream ended cleanly, want a broken stream")
	}
	if n := up.hits.Load(); n != 1 {
		t.Errorf("upstream hits = %d, want 1", n)
	}
	if len(obs.all()) != 0 {
		t.Errorf("observed an incomplete stream: %+v", obs.all())
	}
	if !strings.Contains(h.logs.String(), `"stage":"upstream"`) {
		t.Errorf("log lacks stream-aborted stage=upstream:\n%s", h.logs.String())
	}
}

func TestStreamsEachChunkBeforeUpstreamFinishes(t *testing.T) {
	release := make(chan struct{})
	up := newUpstream(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, evCreated)
		w.(http.Flusher).Flush()
		<-release
		_, _ = io.WriteString(w, evCompleted)
	})
	h := startProxy(t, up.url())

	resp, err := http.DefaultClient.Do(newRequest(t, t.Context(), http.MethodPost, h.base+PathResponses, requestBody))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	first := make(chan string, 1)
	go func() {
		buf := make([]byte, len(evCreated))
		_, _ = io.ReadFull(resp.Body, buf)
		first <- string(buf)
	}()
	select {
	case got := <-first:
		if got != evCreated {
			t.Errorf("first chunk = %q", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("first event not delivered while upstream is still streaming")
	}
	close(release)
	rest, _ := io.ReadAll(resp.Body)
	if string(rest) != evCompleted {
		t.Errorf("rest = %q", rest)
	}
}

func TestClientDisconnectCancelsUpstream(t *testing.T) {
	cancelled := make(chan struct{})
	up := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, evCreated)
		w.(http.Flusher).Flush()
		select {
		case <-r.Context().Done():
			close(cancelled)
		case <-time.After(5 * time.Second):
		}
	})
	h := startProxy(t, up.url())

	ctx, cancel := context.WithCancel(t.Context())
	resp, err := http.DefaultClient.Do(newRequest(t, ctx, http.MethodPost, h.base+PathResponses, requestBody))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bufio.NewReader(resp.Body).ReadString('\n'); err != nil {
		t.Fatalf("first line: %v", err)
	}
	cancel()
	_ = resp.Body.Close()

	select {
	case <-cancelled:
	case <-time.After(3 * time.Second):
		t.Fatal("upstream request not cancelled after client disconnect")
	}
	if n := up.hits.Load(); n != 1 {
		t.Errorf("upstream hits = %d, want 1", n)
	}
}

func TestClientCloseAfterCompletedIsNotAnAbort(t *testing.T) {
	cancelled := make(chan struct{})
	up := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, evCreated+evCompleted)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		close(cancelled)
	})
	h := startProxy(t, up.url())

	ctx, cancel := context.WithCancel(t.Context())
	resp, err := http.DefaultClient.Do(newRequest(t, ctx, http.MethodPost, h.base+PathResponses, requestBody))
	if err != nil {
		t.Fatal(err)
	}
	rd := bufio.NewReader(resp.Body)
	for {
		line, err := rd.ReadString('\n')
		if err != nil {
			t.Fatalf("stream ended before response.completed: %v", err)
		}
		if strings.Contains(line, `"response.completed"`) {
			break
		}
	}
	cancel()
	_ = resp.Body.Close()

	select {
	case <-cancelled:
	case <-time.After(3 * time.Second):
		t.Fatal("upstream request not cancelled after client close")
	}
	waitFor(t, func() bool { return strings.Contains(h.logs.String(), "stream-closed-after-completed") })
	if strings.Contains(h.logs.String(), "stream-aborted") {
		t.Errorf("normal close logged as abort:\n%s", h.logs.String())
	}
}

func TestRedirectRefused(t *testing.T) {
	target := newUpstream(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "{}") })
	up := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.srv.URL+"/elsewhere?sig=REDIRECT-SECRET", http.StatusTemporaryRedirect)
	})
	h := startProxy(t, up.url())
	noFollow := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

	for _, tc := range []struct{ method, path string }{
		{http.MethodPost, PathResponses},
		{http.MethodGet, PathModels},
	} {
		resp, err := noFollow.Do(newRequest(t, t.Context(), tc.method, h.base+tc.path, requestBody))
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusBadGateway {
			t.Errorf("%s %s: status = %d, want 502", tc.method, tc.path, resp.StatusCode)
		}
	}
	if n := target.hits.Load(); n != 0 {
		t.Errorf("redirect target hits = %d, want 0", n)
	}
	if strings.Contains(h.logs.String(), "REDIRECT-SECRET") {
		t.Errorf("log contains the redirect URL:\n%s", h.logs.String())
	}
}

func TestModelsPassThroughAndCatalog(t *testing.T) {
	const query = "client_version=0.162.1&x=a%2Fb"
	const etag = `W/"models-v7"`
	body := `{"models":[{"slug":"gpt-6-luna","context_window":272000,"effective_context_window_percent":95,"comp_hash":"3000"}]}`
	up := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", etag)
		if r.Header.Get("If-None-Match") == etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		_, _ = io.WriteString(w, body)
	})
	var store catalog.Store
	var updates atomic.Int32
	h := startProxy(t, up.url(), func(o *Options) {
		o.OnCatalog = func(c catalog.Catalog) {
			updates.Add(1)
			store.Set(c)
		}
	})

	get := func(ifNoneMatch string) (*http.Response, string) {
		req := newRequest(t, t.Context(), http.MethodGet, h.base+PathModels+"?"+query, nil)
		if ifNoneMatch != "" {
			req.Header.Set("If-None-Match", ifNoneMatch)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		got, _ := io.ReadAll(resp.Body)
		return resp, string(got)
	}

	resp, got := get("")
	if resp.StatusCode != http.StatusOK || got != body || resp.Header.Get("ETag") != etag {
		t.Fatalf("client got %d etag=%q body=%q", resp.StatusCode, resp.Header.Get("ETag"), got)
	}
	in := up.last(t)
	if in.path != upstreamDir+"/models" || in.query != query {
		t.Errorf("upstream got path %q query %q", in.path, in.query)
	}
	if in.header.Get("Authorization") != testAuth {
		t.Error("Authorization not forwarded on /models")
	}
	if in.framing != "cl=0 te=[]" || len(in.body) != 0 {
		t.Errorf("upstream /models carries a body: %s %q", in.framing, in.body)
	}
	cur := store.Current()
	if w, ok := cur.Window("gpt-6-luna"); !ok || w != 258400 || cur.ETag != etag {
		t.Errorf("catalog = %+v", cur)
	}

	resp, got = get(etag)
	if resp.StatusCode != http.StatusNotModified || got != "" {
		t.Errorf("conditional GET: %d %q", resp.StatusCode, got)
	}
	if in := up.last(t); in.header.Get("If-None-Match") != etag {
		t.Errorf("If-None-Match = %q", in.header.Get("If-None-Match"))
	}
	if n := updates.Load(); n != 1 {
		t.Errorf("catalog updates = %d, want 1 (304 must not update)", n)
	}
}

func TestAllowList(t *testing.T) {
	up := sseUpstream(t, evCompleted)
	h := startProxy(t, up.url())
	tests := []struct {
		method, path string
		want         int
	}{
		{http.MethodGet, PathHealth, http.StatusOK},
		{http.MethodGet, PathResponses, http.StatusMethodNotAllowed},
		{http.MethodPost, PathModels, http.StatusMethodNotAllowed},
		{http.MethodGet, "/", http.StatusNotFound},
		{http.MethodPost, "/v1/chat/completions", http.StatusNotFound},
		{http.MethodPost, PathResponses + "/compact", http.StatusNotFound},
		{http.MethodGet, "/backend-api/codex/models", http.StatusNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.method+" "+tt.path, func(t *testing.T) {
			resp, err := http.DefaultClient.Do(newRequest(t, t.Context(), tt.method, h.base+tt.path, nil))
			if err != nil {
				t.Fatal(err)
			}
			_ = resp.Body.Close()
			if resp.StatusCode != tt.want {
				t.Errorf("status = %d, want %d", resp.StatusCode, tt.want)
			}
		})
	}
	if n := up.hits.Load(); n != 0 {
		t.Errorf("upstream hits = %d, want 0", n)
	}
}

func writeChunked(w io.Writer, n int) {
	chunk := bytes.Repeat([]byte("a"), 1<<20)
	for n > 0 {
		c := chunk[:min(n, len(chunk))]
		if _, err := fmt.Fprintf(w, "%x\r\n%s\r\n", len(c), c); err != nil {
			return
		}
		n -= len(c)
	}
	_, _ = io.WriteString(w, "0\r\n\r\n")
}

func TestBodyOverCapIs413(t *testing.T) {
	tests := []struct {
		name  string
		frame string
		body  func(io.Writer)
	}{
		{
			name:  "declared content length",
			frame: fmt.Sprintf("Content-Length: %d", MaxBodyBytes+1),
			body:  func(io.Writer) {},
		},
		{
			name:  "chunked without content length",
			frame: "Transfer-Encoding: chunked",
			body:  func(w io.Writer) { writeChunked(w, MaxBodyBytes+1) },
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			up := sseUpstream(t, evCompleted)
			h := startProxy(t, up.url())

			conn, err := net.Dial("tcp", strings.TrimPrefix(h.base, "http://"))
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = conn.Close() }()
			go func() {
				_, _ = fmt.Fprintf(conn, "POST %s HTTP/1.1\r\nHost: x\r\nAuthorization: %s\r\n%s\r\n\r\n",
					PathResponses, testAuth, tt.frame)
				tt.body(conn)
			}()
			resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
			if err != nil {
				t.Fatal(err)
			}
			_ = resp.Body.Close()

			if resp.StatusCode != http.StatusRequestEntityTooLarge {
				t.Errorf("status = %d, want 413", resp.StatusCode)
			}
			if n := up.hits.Load(); n != 0 {
				t.Errorf("upstream hits = %d, want 0", n)
			}
		})
	}
}

func TestBodyAtCapIsForwarded(t *testing.T) {
	up := sseUpstream(t, evCompleted)
	h := startProxy(t, up.url())
	body := bytes.Repeat([]byte("a"), MaxBodyBytes)

	resp, _ := postResponses(t, h, body)

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
	if n := len(up.last(t).body); n != MaxBodyBytes {
		t.Errorf("upstream got %d bytes, want %d", n, MaxBodyBytes)
	}
}

func TestListenBindsLoopbackOnly(t *testing.T) {
	tests := []struct {
		name     string
		addr     string
		upstream string
		wantErr  bool
	}{
		{name: "default", addr: ""},
		{name: "loopback ephemeral", addr: "127.0.0.1:0"},
		{name: "all interfaces", addr: "0.0.0.0:0", wantErr: true},
		{name: "empty host", addr: ":0", wantErr: true},
		{name: "ipv6 loopback", addr: "[::1]:0", wantErr: true},
		{name: "hostname", addr: "localhost:0", wantErr: true},
		{name: "lan address", addr: "192.168.1.10:0", wantErr: true},
		{name: "no port", addr: "127.0.0.1", wantErr: true},
		{name: "bad upstream scheme", addr: "127.0.0.1:0", upstream: "ftp://example.com/x", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := New(Options{Addr: tt.addr, Upstream: tt.upstream, Logger: slog.New(slog.DiscardHandler)})
			port, err := s.Listen()
			if tt.wantErr {
				if err == nil {
					t.Fatalf("Listen(%q) bound port %d, want error", tt.addr, port)
				}
				return
			}
			if err != nil {
				t.Fatalf("Listen(%q): %v", tt.addr, err)
			}
			defer func() { _ = s.ln.Close() }()
			if got := s.ln.Addr().(*net.TCPAddr).IP.String(); got != LoopbackHost || port == 0 {
				t.Errorf("bound %s port %d", got, port)
			}
		})
	}
}

func TestServeStopsOnCancel(t *testing.T) {
	s := New(Options{Logger: slog.New(slog.DiscardHandler)})
	if err := s.Serve(t.Context()); err == nil {
		t.Error("Serve before Listen succeeded")
	}
	port, err := s.Listen()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- s.Serve(ctx) }()
	resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d%s", port, PathHealth))
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Serve: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Serve did not return after cancel")
	}
}

type panicObserver struct{}

func (panicObserver) Observe(responses.Meta, string, responses.Usage, int) {
	panic("observer " + testAuth)
}

func TestObserverPanicKeepsStream(t *testing.T) {
	up := sseUpstream(t, evCreated, evCompleted)
	h := startProxy(t, up.url(), func(o *Options) { o.Observer = panicObserver{} })

	resp, got := postResponses(t, h, requestBody)

	if resp.StatusCode != http.StatusOK || string(got) != evCreated+evCompleted {
		t.Errorf("client got %d %q", resp.StatusCode, got)
	}
	if !strings.Contains(h.logs.String(), "observe-panic") {
		t.Errorf("log lacks observe-panic:\n%s", h.logs.String())
	}
}

func TestModelsUnparseableBodyStillRelayed(t *testing.T) {
	const body = "<html>not a catalog</html>"
	up := newUpstream(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, body) })
	var updates atomic.Int32
	h := startProxy(t, up.url(), func(o *Options) {
		o.OnCatalog = func(catalog.Catalog) { updates.Add(1) }
	})

	resp, err := http.DefaultClient.Do(newRequest(t, t.Context(), http.MethodGet, h.base+PathModels, nil))
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()

	if resp.StatusCode != http.StatusOK || string(got) != body {
		t.Errorf("client got %d %q", resp.StatusCode, got)
	}
	if n := updates.Load(); n != 0 {
		t.Errorf("catalog updates = %d, want 0", n)
	}
}

func TestWithoutHop(t *testing.T) {
	in := http.Header{
		"Authorization":       {testAuth},
		"Chatgpt-Account-Id":  {testAccount},
		"Connection":          {"keep-alive, X-Conn-Scoped"},
		"X-Conn-Scoped":       {"1"},
		"Keep-Alive":          {"timeout=5"},
		"Proxy-Authorization": {"Basic cHJveHk="},
		"Te":                  {"trailers"},
		"Transfer-Encoding":   {"chunked"},
		"Upgrade":             {"websocket"},
		"X-Codex-Window-Id":   {"w-1"},
	}

	got := withoutHop(in)

	want := http.Header{
		"Authorization":      {testAuth},
		"Chatgpt-Account-Id": {testAccount},
		"X-Codex-Window-Id":  {"w-1"},
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("withoutHop = %v, want %v", got, want)
	}
	if in.Get("Connection") == "" {
		t.Error("withoutHop modified its input")
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met within 3s")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
