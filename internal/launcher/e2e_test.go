package launcher

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alexei-led/codex-router/internal/appserver"
	"github.com/alexei-led/codex-router/internal/config"
	"github.com/alexei-led/codex-router/internal/control"
	"github.com/alexei-led/codex-router/internal/proxy"
	"github.com/alexei-led/codex-router/internal/responses"
	"github.com/alexei-led/codex-router/internal/route"
)

const (
	e2eKeyEnv    = "CODEX_ROUTER_TEST_KEY"
	e2eWait      = 5 * time.Second
	e2ePoll      = 5 * time.Millisecond
	e2eRoutedTo  = "low"
	e2eFirstItem = "event: response.output_item.done\ndata: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"message\"}}\n\n"
)

var (
	nativeRoute = config.Route{Model: "gpt-6.1-sol", Effort: config.EffortHigh}
	routedRoute = config.Route{Model: "gpt-6-luna", Effort: config.EffortMedium}
	manualRoute = config.Route{Model: "gpt-6-sol", Effort: config.EffortMedium}
)

// e2eUpstream is the fake ChatGPT backend: it records what each POST asked
// for and answers with a completed SSE stream, or cuts it after one item.
type e2eUpstream struct {
	*httptest.Server
	cut    atomic.Bool
	mu     sync.Mutex
	routes []config.Route
	authOK []bool
	bodies [][]byte
	meta   []string
}

func newE2EUpstream(t *testing.T) *e2eUpstream {
	t.Helper()
	u := &e2eUpstream{}
	u.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model     string `json:"model"`
			Reasoning struct {
				Effort config.Effort `json:"effort"`
			} `json:"reasoning"`
		}
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		u.mu.Lock()
		u.bodies = append(u.bodies, raw)
		u.meta = append(u.meta, r.Header.Get(responses.HeaderTurnMetadata))
		u.routes = append(u.routes, config.Route{Model: body.Model, Effort: body.Reasoning.Effort})
		u.authOK = append(u.authOK, r.Header.Get("Authorization") == fakeToken)
		u.mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		if u.cut.Load() {
			_, _ = io.WriteString(w, e2eFirstItem)
			w.(http.Flusher).Flush()
			panic(http.ErrAbortHandler)
		}
		_, _ = io.WriteString(w, evCompleted)
	}))
	t.Cleanup(u.Close)
	return u
}

func (u *e2eUpstream) seen() []config.Route {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]config.Route(nil), u.routes...)
}

// e2eClassifier answers in the system-one wire format, always picking
// e2eRoutedTo.
type e2eClassifier struct {
	*httptest.Server
	calls atomic.Int32
}

func newE2EClassifier(t *testing.T) *e2eClassifier {
	t.Helper()
	probs := map[string]float64{"micro": 0, "low": 0, "medium": 0, "high": 0, "uncertain": 0}
	probs[e2eRoutedTo] = 0.97
	answer, err := json.Marshal(map[string]any{"answers": map[string]any{
		"route": map[string]any{"type": "choice", "choice": e2eRoutedTo, "probabilities": probs, "confidence": 0.97},
	}})
	if err != nil {
		t.Fatal(err)
	}
	c := &e2eClassifier{}
	c.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		c.calls.Add(1)
		_, _ = w.Write(answer)
	}))
	t.Cleanup(c.Close)
	return c
}

// e2e is a started session (real proxy, engine, control socket, fake
// app-server child) with a fake upstream and classifier behind it.
type e2e struct {
	h   *harness
	s   *session
	up  *e2eUpstream
	cls *e2eClassifier
	ctl *control.Client
	url string
	// token is what the app-server got in EnvToken; Codex sends it as
	// proxy.HeaderToken.
	token string
}

func newE2E(t *testing.T) *e2e {
	t.Helper()
	e := &e2e{up: newE2EUpstream(t), cls: newE2EClassifier(t)}
	e.h = newHarness(t, e2eKeyEnv+"=k")
	cfg := e.h.opts.Config
	cc := cfg.Classifiers[cfg.Classifier]
	cc.KeyEnv, cc.Endpoint = e2eKeyEnv, e.cls.URL
	cfg.Classifiers[cfg.Classifier] = cc
	// Background reviews would add classifier calls to a long turn.
	cfg.Policy.ReviewEveryRequests = 0
	e.h.opts.Config = cfg
	e.h.opts.Upstream = e.up.URL + "/backend-api/codex"

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	s, err := start(ctx, e.h.opts.withDefaults())
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(s.close)
	e.s = s
	port, err := os.ReadFile(filepath.Join(s.dir, PortFileName))
	if err != nil {
		t.Fatal(err)
	}
	e.url = "http://127.0.0.1:" + strings.TrimSpace(string(port)) + "/v1/responses"
	e.token = e.startToken(t)
	if e.ctl, err = control.Dial(filepath.Join(s.dir, control.SocketName)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.ctl.Close() })
	return e
}

// startToken returns the token the fake app-server recorded at start; start
// returns only after the app-server answered initialize, so it is there.
func (e *e2e) startToken(t *testing.T) string {
	t.Helper()
	starts := e.h.recorded(t, "start")
	if len(starts) != 1 {
		t.Fatalf("app-server starts = %d, want 1", len(starts))
	}
	var s struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(starts[0], &s); err != nil {
		t.Fatal(err)
	}
	return s.Token
}

type request struct {
	turn     string
	kind     string
	subagent string
	step     int
}

func turnReq(turn string, step int) request {
	return request{turn: turn, kind: responses.RequestKindTurn, step: step}
}

func (r request) metadata() string {
	return fmt.Sprintf(`{"session_id":"s1","thread_id":%q,"turn_id":%q,"request_kind":%q,"agent_name":"/root"}`,
		fakeThread, r.turn, r.kind)
}

// post sends one request through the proxy and drains the reply.
func (e *e2e) post(t *testing.T, r request) (int, error) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, e.url, bytes.NewReader(stepBody(r.step)))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", fakeToken)
	req.Header.Set(proxy.HeaderToken, e.token)
	req.Header.Set(responses.HeaderTurnMetadata, r.metadata())
	if r.subagent != "" {
		req.Header.Set(responses.HeaderSubagent, r.subagent)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	_, err = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode, err
}

func (e *e2e) mustPost(t *testing.T, r request) {
	t.Helper()
	if status, err := e.post(t, r); err != nil || status != http.StatusOK {
		t.Fatalf("post %+v: status %d, err %v", r, status, err)
	}
}

func (e *e2e) snapshot(t *testing.T) route.Snapshot {
	t.Helper()
	if err := e.ctl.Send(route.Command{Op: route.OpSnapshot}); err != nil {
		t.Fatal(err)
	}
	// The snapshot is written before the ack, so it is the newest queued.
	var snap route.Snapshot
	for {
		select {
		case snap = <-e.ctl.Updates():
		default:
			return snap
		}
	}
}

// eventually polls the control snapshot until ok holds.
func (e *e2e) eventually(t *testing.T, what string, ok func(route.Snapshot) bool) route.Snapshot {
	t.Helper()
	deadline := time.Now().Add(e2eWait)
	for {
		snap := e.snapshot(t)
		if ok(snap) {
			return snap
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s; last mode %q, footer %+v, replies %d", what, snap.Mode, snap.Footer, snap.Stats.Replies)
		}
		time.Sleep(e2ePoll)
	}
}

func (e *e2e) command(t *testing.T, c route.Command) {
	t.Helper()
	if err := e.ctl.Send(c); err != nil {
		t.Fatal(err)
	}
}

// writes waits for n settings writes and returns them.
func (e *e2e) writes(t *testing.T, n int) []json.RawMessage {
	t.Helper()
	deadline := time.Now().Add(e2eWait)
	for {
		got := e.h.recorded(t, "update")
		if len(got) >= n || time.Now().After(deadline) {
			if len(got) != n {
				t.Fatalf("settings writes = %d, want %d", len(got), n)
			}
			return got
		}
		time.Sleep(e2ePoll)
	}
}

func (e *e2e) wantRoutes(t *testing.T, want ...config.Route) {
	t.Helper()
	got := e.up.seen()
	if len(got) != len(want) {
		t.Fatalf("upstream got %d requests (%v), want %d (%v)", len(got), got, len(want), want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("upstream request %d = %+v, want %+v", i+1, got[i], want[i])
		}
	}
	e.up.mu.Lock()
	defer e.up.mu.Unlock()
	for i, ok := range e.up.authOK {
		if !ok {
			t.Errorf("upstream request %d did not carry the client's auth header", i+1)
		}
	}
}

// wantForwardedVerbatim checks upstream request i carries exactly the bytes
// and turn metadata the client sent.
func (e *e2e) wantForwardedVerbatim(t *testing.T, i int, r request) {
	t.Helper()
	e.up.mu.Lock()
	defer e.up.mu.Unlock()
	if i >= len(e.up.bodies) {
		t.Fatalf("upstream saw %d requests, want request %d", len(e.up.bodies), i+1)
	}
	if !bytes.Equal(e.up.bodies[i], stepBody(r.step)) {
		t.Errorf("upstream body %d differs from the client's bytes:\n%s", i+1, e.up.bodies[i])
	}
	if want := r.metadata(); e.up.meta[i] != want {
		t.Errorf("upstream turn metadata %d = %q, want %q", i+1, e.up.meta[i], want)
	}
}

func repeat(r config.Route, n int) []config.Route {
	out := make([]config.Route, n)
	for i := range out {
		out[i] = r
	}
	return out
}

func TestE2EThreeToolTurnClassifiesOnceAndPinsEveryStep(t *testing.T) {
	e := newE2E(t)
	for step := 1; step <= 5; step++ {
		e.mustPost(t, turnReq("turn-1", step))
	}

	e.wantRoutes(t, repeat(routedRoute, 5)...)
	if n := e.cls.calls.Load(); n != 1 {
		t.Errorf("classifier calls = %d, want 1", n)
	}
	snap := e.eventually(t, "5 replies", func(s route.Snapshot) bool { return s.Stats.Replies == 5 })
	if snap.Stats.Switches != 1 || snap.Stats.ByTier[config.TierLow] != 5 {
		t.Errorf("stats = %d switches, by tier %v; want 1 switch and 5 low replies", snap.Stats.Switches, snap.Stats.ByTier)
	}
	if got := e.writes(t, 1); !strings.Contains(string(got[0]), `"model":"`+routedRoute.Model+`"`) {
		t.Errorf("settings write = %s, want the routed model", got[0])
	}
	e.eventually(t, "footer synced to the route", func(s route.Snapshot) bool {
		return s.FooterSynced && s.Footer == routedRoute
	})
}

func TestE2ESubagentRequestIsForwardedUnchanged(t *testing.T) {
	e := newE2E(t)
	r := request{turn: "turn-1", kind: responses.RequestKindTurn, subagent: "review", step: 1}
	e.mustPost(t, r)

	e.wantRoutes(t, nativeRoute)
	e.wantForwardedVerbatim(t, 0, r)
	if n := e.cls.calls.Load(); n != 0 {
		t.Errorf("classifier calls = %d, want 0", n)
	}
	if snap := e.snapshot(t); snap.Stats.Switches != 0 {
		t.Errorf("switches = %d, want 0", snap.Stats.Switches)
	}
	e.writes(t, 0)
}

func TestE2ECompactionMidTurnKeepsTheTurnsRoute(t *testing.T) {
	e := newE2E(t)
	compaction := request{turn: "turn-1", kind: "compaction", step: 2}
	e.mustPost(t, turnReq("turn-1", 1))
	e.mustPost(t, compaction)
	e.mustPost(t, turnReq("turn-1", 3))

	e.wantRoutes(t, routedRoute, nativeRoute, routedRoute)
	e.wantForwardedVerbatim(t, 1, compaction)
	if n := e.cls.calls.Load(); n != 1 {
		t.Errorf("classifier calls = %d, want 1", n)
	}
	e.eventually(t, "footer synced", func(s route.Snapshot) bool { return s.FooterSynced && s.Footer == routedRoute })
	e.writes(t, 1)
}

func TestE2EManualModelPickStopsRoutingUntilResume(t *testing.T) {
	e := newE2E(t)
	e.mustPost(t, turnReq("turn-1", 1))
	e.eventually(t, "footer synced", func(s route.Snapshot) bool { return s.FooterSynced && s.Footer == routedRoute })

	// What Codex's /model does: a settings write the router did not make.
	// The fake app-server has one connection, so the write goes through the
	// router's own client; the engine still sees an unexpected settings
	// update, which is what marks it foreign.
	ctx, cancel := context.WithTimeout(context.Background(), e2eWait)
	defer cancel()
	pick := appserver.Settings{Model: manualRoute.Model, Effort: manualRoute.Effort}
	if err := e.s.api.UpdateSettings(ctx, fakeThread, pick); err != nil {
		t.Fatal(err)
	}
	snap := e.eventually(t, "manual mode", func(s route.Snapshot) bool { return s.Mode == route.ModeManual })
	if snap.Footer != manualRoute {
		t.Errorf("footer = %+v, want the picked route %+v", snap.Footer, manualRoute)
	}

	e.mustPost(t, turnReq("turn-2", 1))
	e.wantRoutes(t, routedRoute, nativeRoute)
	e.wantForwardedVerbatim(t, 1, turnReq("turn-2", 1))
	e.writes(t, 2) // the router's first route plus the manual pick, nothing since
	if n := e.cls.calls.Load(); n != 1 {
		t.Errorf("classifier calls while manual = %d, want 1", n)
	}

	e.command(t, route.Command{Op: route.OpResume})
	e.eventually(t, "mode on", func(s route.Snapshot) bool { return s.Mode == route.ModeOn })
	e.mustPost(t, turnReq("turn-3", 1))
	e.wantRoutes(t, routedRoute, nativeRoute, routedRoute)
	if n := e.cls.calls.Load(); n != 2 {
		t.Errorf("classifier calls after resume = %d, want 2", n)
	}
}

func TestE2EPauseForwardsUnchangedUntilResume(t *testing.T) {
	e := newE2E(t)
	e.mustPost(t, turnReq("turn-1", 1))
	e.eventually(t, "footer synced", func(s route.Snapshot) bool { return s.FooterSynced && s.Footer == routedRoute })

	e.command(t, route.Command{Op: route.OpPause})
	e.mustPost(t, turnReq("turn-2", 1))
	e.eventually(t, "paused mode and the footer back on the native route", func(s route.Snapshot) bool {
		return s.Mode == route.ModePaused && s.FooterSynced && s.Footer == nativeRoute
	})
	e.wantRoutes(t, routedRoute, nativeRoute)
	if n := e.cls.calls.Load(); n != 1 {
		t.Errorf("classifier calls while paused = %d, want 1", n)
	}

	e.command(t, route.Command{Op: route.OpResume})
	e.mustPost(t, turnReq("turn-3", 1))
	e.wantRoutes(t, routedRoute, nativeRoute, routedRoute)
	if n := e.cls.calls.Load(); n != 2 {
		t.Errorf("classifier calls after resume = %d, want 2", n)
	}
	e.eventually(t, "mode on", func(s route.Snapshot) bool { return s.Mode == route.ModeOn })
}

func TestE2EClassifierDownForwardsUnchangedAndPausesAfterThreeFailures(t *testing.T) {
	e := newE2E(t)
	e.cls.Close()

	for i := 1; i <= 4; i++ {
		e.mustPost(t, turnReq(fmt.Sprintf("turn-%d", i), 1))
	}

	e.wantRoutes(t, repeat(nativeRoute, 4)...)
	snap := e.snapshot(t)
	if snap.Classifier.Failures != 3 || !snap.Classifier.PausedUntil.After(snap.At) {
		t.Errorf("classifier health = %d failures, paused until %v (at %v); want 3 failures and a pause",
			snap.Classifier.Failures, snap.Classifier.PausedUntil, snap.At)
	}
	if !hasFinding(snap, route.FindingClassifier) {
		t.Errorf("findings = %v, want a classifier finding", snap.Worth)
	}
	if snap.Stats.Switches != 0 {
		t.Errorf("switches = %d, want 0", snap.Stats.Switches)
	}
	// The engine may restate the native route once; it never writes another.
	for _, w := range e.h.recorded(t, "update") {
		if strings.Contains(string(w), routedRoute.Model) {
			t.Errorf("settings write %s moved the footer off the native route", w)
		}
	}
}

func hasFinding(s route.Snapshot, kind route.FindingKind) bool {
	for _, f := range s.Worth {
		if f.Kind == kind {
			return true
		}
	}
	return false
}

func TestE2EUpstreamCloseAfterFirstItemIsNotRetried(t *testing.T) {
	e := newE2E(t)
	e.up.cut.Store(true)

	status, err := e.post(t, turnReq("turn-1", 1))
	if err == nil && status == http.StatusOK {
		t.Error("client saw a clean reply, want the cut stream to reach it")
	}
	e.wantRoutes(t, routedRoute)
}

func TestE2ERequestWithoutTheSessionTokenIsRejected(t *testing.T) {
	e := newE2E(t)
	for _, token := range []string{"", strings.Repeat("0", len(e.token))} {
		e.token = token
		if status, err := e.post(t, turnReq("turn-1", 1)); err != nil || status != http.StatusUnauthorized {
			t.Errorf("token %q: status %d, err %v; want 401", token, status, err)
		}
	}

	e.wantRoutes(t)
	if n := e.cls.calls.Load(); n != 0 {
		t.Errorf("classifier calls = %d, want 0", n)
	}
	if snap := e.snapshot(t); snap.Stats.Replies != 0 || snap.Stats.Switches != 0 {
		t.Errorf("stats = %d replies, %d switches; want none", snap.Stats.Replies, snap.Stats.Switches)
	}
}
