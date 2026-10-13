package route

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alexei-led/codex-router/internal/catalog"
	"github.com/alexei-led/codex-router/internal/classifier"
	"github.com/alexei-led/codex-router/internal/config"
	"github.com/alexei-led/codex-router/internal/policy"
	"github.com/alexei-led/codex-router/internal/responses"
)

const testCatalog = `{"models":[
 {"slug":"gpt-6.1-sol","context_window":500000,"effective_context_window_percent":95,"comp_hash":"3000","use_responses_lite":true,"tool_mode":"code_mode_only",
  "supported_reasoning_levels":[{"effort":"low"},{"effort":"medium"},{"effort":"high"},{"effort":"xhigh"}]},
 {"slug":"gpt-6-sol","context_window":272000,"effective_context_window_percent":95,"comp_hash":"3000","use_responses_lite":true,"tool_mode":"code_mode_only",
  "supported_reasoning_levels":[{"effort":"low"},{"effort":"medium"},{"effort":"high"},{"effort":"xhigh"}]},
 {"slug":"gpt-6-luna","context_window":272000,"effective_context_window_percent":95,"comp_hash":"3000","use_responses_lite":true,"tool_mode":"code_mode_only",
  "supported_reasoning_levels":[{"effort":"low"},{"effort":"medium"},{"effort":"high"},{"effort":"xhigh"}]},
 {"slug":"gpt-5.5","context_window":272000,"effective_context_window_percent":95,"comp_hash":"2911",
  "supported_reasoning_levels":[{"effort":"low"},{"effort":"medium"},{"effort":"high"}]}
]}`

var (
	high   = config.Route{Model: "gpt-6.1-sol", Effort: config.EffortHigh}
	medium = config.Route{Model: "gpt-6-sol", Effort: config.EffortMedium}
	low    = config.Route{Model: "gpt-6-luna", Effort: config.EffortMedium}
	debug  = config.Route{Model: "gpt-6-sol", Effort: config.EffortHigh}
)

type fakeAsker struct {
	mu      sync.Mutex
	calls   int
	answers []func() (classifier.Advice, error)
}

func (f *fakeAsker) Ask(ctx context.Context, _ classifier.Input) (classifier.Advice, error) {
	f.mu.Lock()
	f.calls++
	var next func() (classifier.Advice, error)
	if len(f.answers) > 0 {
		next, f.answers = f.answers[0], f.answers[1:]
	}
	f.mu.Unlock()
	if next == nil {
		return classifier.Advice{}, classifier.ErrUnreachable
	}
	return next()
}

func (f *fakeAsker) Health() classifier.Health { return classifier.Health{} }

func (f *fakeAsker) Calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func (f *fakeAsker) script(answers ...func() (classifier.Advice, error)) *fakeAsker {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.answers = append(f.answers, answers...)
	return f
}

func answer(a classifier.Advice) func() (classifier.Advice, error) {
	return func() (classifier.Advice, error) { return a, nil }
}

func fail(err error) func() (classifier.Advice, error) {
	return func() (classifier.Advice, error) { return classifier.Advice{}, err }
}

func tier(t config.Tier, p float64) classifier.Advice {
	return classifier.Advice{Choice: classifier.Choice(t), Probabilities: map[classifier.Choice]float64{classifier.Choice(t): p}}
}

func withActivity(a classifier.Advice, act config.Activity, p float64) classifier.Advice {
	a.Activity = &classifier.ActivityAdvice{Choice: act, Probabilities: map[config.Activity]float64{act: p}}
	return a
}

func continuing(a classifier.Advice, c float64) classifier.Advice {
	a.Continuation = &c
	return a
}

type recordingSink struct {
	mu        sync.Mutex
	decisions []Decision
	snapshots int
	engine    *Engine
}

func (s *recordingSink) OnDecision(d Decision) {
	if s.engine != nil {
		s.engine.Snapshot() // deadlocks if called under the engine lock
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.decisions = append(s.decisions, d)
}

func (s *recordingSink) OnSnapshot(Snapshot) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.snapshots++
}

func (s *recordingSink) Decisions() []Decision {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Decision(nil), s.decisions...)
}

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

type harness struct {
	t      *testing.T
	engine *Engine
	asker  *fakeAsker
	sink   *recordingSink
	clock  *clock
	logs   *bytes.Buffer
	cat    *catalog.Store
}

func testConfig() config.Config {
	cfg := config.Default()
	cfg.Passthrough = []string{"You are performing a CONTEXT CHECKPOINT"}
	return cfg
}

func newHarness(t *testing.T, cfg config.Config) *harness {
	t.Helper()
	cat, err := catalog.Parse([]byte(testCatalog))
	if err != nil {
		t.Fatal(err)
	}
	store := &catalog.Store{}
	store.Set(cat)
	h := &harness{
		t: t, asker: &fakeAsker{}, sink: &recordingSink{}, logs: &bytes.Buffer{}, cat: store,
		clock: &clock{t: time.Date(2026, 10, 13, 18, 0, 0, 0, time.UTC)},
	}
	h.engine = New(Deps{
		Config: cfg, Catalog: store, Classifier: h.asker, Now: h.clock.Now, Sink: h.sink,
		Logger: slog.New(slog.NewTextHandler(h.logs, nil)),
	})
	h.sink.engine = h.engine
	t.Cleanup(func() {
		h.engine.reviews.Wait()
		if strings.Contains(h.logs.String(), secretPrompt) {
			t.Errorf("logs carry request text:\n%s", h.logs.String())
		}
	})
	return h
}

const secretPrompt = "fix the flaky retry test in pkg/queue"

func userMsg(text string) responses.Item {
	return responses.Item{Type: "message", Role: "user", Text: text}
}

func call(id, cmd string) responses.Item {
	args, _ := json.Marshal(map[string]string{"cmd": cmd})
	return responses.Item{Type: "function_call", Name: "exec_command", CallID: id, Arguments: string(args)}
}

func result(id string, code int, body string) responses.Item {
	out := fmt.Sprintf("Chunk ID: 1\nWall time: 0.4 seconds\nProcess exited with code %d\nOriginal token count: 3\nOutput:\n%s", code, body)
	return responses.Item{Type: "function_call_output", CallID: id, Output: out}
}

func editGo(id string) responses.Item {
	return responses.Item{Type: "custom_tool_call", Name: "apply_patch", CallID: id,
		Arguments: "*** Begin Patch\n*** Update File: retry.go\n@@\n-a\n+b\n*** End Patch\n"}
}

func edited(id string) responses.Item {
	return responses.Item{Type: "custom_tool_call_output", CallID: id, Output: "Success."}
}

func req(r config.Route, items ...responses.Item) *responses.Request {
	return &responses.Request{Model: r.Model, Effort: r.Effort, Input: items}
}

func meta(thread, turn string) responses.Meta {
	return responses.Meta{SessionID: "s", ThreadID: thread, TurnID: turn}
}

func (h *harness) decide(r *responses.Request, m responses.Meta) (responses.Rewrite, bool) {
	h.t.Helper()
	return h.engine.Decide(context.Background(), r, m)
}

func (h *harness) reply(m responses.Meta, model string, input, cached int) {
	h.engine.Observe(m, model, responses.Usage{InputTokens: input, CachedInputTokens: cached, OutputTokens: 100, TotalTokens: input + 100}, 200)
}

func (h *harness) last() Decision {
	h.t.Helper()
	ds := h.sink.Decisions()
	if len(ds) == 0 {
		h.t.Fatal("no decision published")
	}
	return ds[len(ds)-1]
}

func wantRoute(t *testing.T, rw responses.Rewrite, ok bool, want config.Route) {
	t.Helper()
	if !ok || rw.Model != want.Model || rw.Effort != want.Effort {
		t.Fatalf("rewrite = %+v, %v; want %+v", rw, ok, want)
	}
}

func wantUnchanged(t *testing.T, rw responses.Rewrite, ok bool) {
	t.Helper()
	if ok {
		t.Fatalf("rewrite = %+v, want the request unchanged", rw)
	}
}

func TestFirstStepRoutesOnClassifierAdvice(t *testing.T) {
	h := newHarness(t, testConfig())
	h.asker.script(answer(tier("low", 0.97)))

	rw, ok := h.decide(req(medium, userMsg(secretPrompt)), meta("th", "t1"))

	wantRoute(t, rw, ok, low)
	d := h.last()
	if d.Tier != config.TierLow || d.Reason != policy.ReasonDowngrade || d.Original != medium || d.MidTurn {
		t.Fatalf("decision = %+v", d)
	}
}

func TestRetrySameFingerprintReusesDecision(t *testing.T) {
	h := newHarness(t, testConfig())
	h.asker.script(answer(tier("low", 0.97)))
	r := req(medium, userMsg(secretPrompt))

	first, ok1 := h.decide(r, meta("th", "t1"))
	again, ok2 := h.decide(r, meta("th", "t1"))

	wantRoute(t, first, ok1, low)
	wantRoute(t, again, ok2, low)
	if h.asker.Calls() != 1 || len(h.sink.Decisions()) != 1 {
		t.Fatalf("classifier calls = %d, decisions = %d; want 1 and 1", h.asker.Calls(), len(h.sink.Decisions()))
	}
}

func TestLaterStepsKeepTheTurnRouteWithoutAsking(t *testing.T) {
	h := newHarness(t, testConfig())
	h.asker.script(answer(tier("low", 0.97)))
	items := []responses.Item{userMsg(secretPrompt)}
	m := meta("th", "t1")
	h.decide(req(medium, items...), m)

	items = append(items, call("c1", "ls"), result("c1", 0, "a.go"))
	rw, ok := h.decide(req(medium, items...), m)

	wantRoute(t, rw, ok, low)
	if d := h.last(); d.Step != 1 || !d.MidTurn || d.Reason != ReasonContinue {
		t.Fatalf("decision = %+v", d)
	}
	if h.asker.Calls() != 1 {
		t.Fatalf("classifier calls = %d, want 1", h.asker.Calls())
	}
}

func TestPassthroughKindsAreUntouched(t *testing.T) {
	tests := []struct {
		name string
		meta responses.Meta
		text string
	}{
		{"subagent", responses.Meta{ThreadID: "th", TurnID: "t1", Subagent: "review"}, secretPrompt},
		{"no turn id", responses.Meta{ThreadID: "th"}, secretPrompt},
		{"compaction prompt", meta("th", "t1"), "You are performing a CONTEXT CHECKPOINT now"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t, testConfig())
			h.asker.script(answer(tier("low", 0.97)))

			rw, ok := h.decide(req(medium, userMsg(tt.text)), tt.meta)

			wantUnchanged(t, rw, ok)
			if h.asker.Calls() != 0 || len(h.sink.Decisions()) != 0 {
				t.Fatalf("classifier calls = %d, decisions = %d; want none", h.asker.Calls(), len(h.sink.Decisions()))
			}
		})
	}
}

func TestOutOfGroupRequests(t *testing.T) {
	t.Run("unknown request model", func(t *testing.T) {
		h := newHarness(t, testConfig())
		h.asker.script(answer(tier("low", 0.97)))
		rw, ok := h.decide(req(config.Route{Model: "gpt-9"}, userMsg(secretPrompt)), meta("th", "t1"))
		wantUnchanged(t, rw, ok)
		if h.asker.Calls() != 0 {
			t.Fatalf("classifier calls = %d, want 0", h.asker.Calls())
		}
	})
	t.Run("request model in another group", func(t *testing.T) {
		h := newHarness(t, testConfig())
		h.asker.script(answer(tier("low", 0.97)))
		original := config.Route{Model: "gpt-5.5", Effort: config.EffortMedium}
		rw, ok := h.decide(req(original, userMsg(secretPrompt)), meta("th", "t1"))
		wantUnchanged(t, rw, ok)
		if d := h.last(); d.Reason != policy.ReasonOutOfGroup || d.Route != original {
			t.Fatalf("decision = %+v", d)
		}
	})
	t.Run("route model in another group", func(t *testing.T) {
		cfg := testConfig()
		cfg.Routes[config.TierLow] = config.Route{Model: "gpt-5.5", Effort: config.EffortLow}
		h := newHarness(t, cfg)
		h.asker.script(answer(tier("low", 0.97)))
		rw, ok := h.decide(req(medium, userMsg(secretPrompt)), meta("th", "t1"))
		wantUnchanged(t, rw, ok)
		if d := h.last(); d.Reason != policy.ReasonOutOfGroup {
			t.Fatalf("decision = %+v", d)
		}
		if s := h.engine.Snapshot(); len(s.Blocked) != 1 || s.Blocked[0] != "gpt-5.5" {
			t.Fatalf("blocked = %v", s.Blocked)
		}
	})
}

func TestClassifierFailureLeavesTheTurnUnrouted(t *testing.T) {
	tests := []struct {
		name   string
		answer func() (classifier.Advice, error)
	}{
		{"timeout", fail(classifier.ErrTimeout)},
		{"panic", func() (classifier.Advice, error) { panic("boom") }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t, testConfig())
			h.asker.script(tt.answer, answer(tier("low", 0.97)))
			m := meta("th", "t1")
			items := []responses.Item{userMsg(secretPrompt)}

			rw, ok := h.decide(req(medium, items...), m)
			wantUnchanged(t, rw, ok)
			if d := h.last(); d.Reason != policy.ReasonError {
				t.Fatalf("decision = %+v", d)
			}

			items = append(items, call("c1", "ls"), result("c1", 0, ""))
			rw, ok = h.decide(req(medium, items...), m)
			wantUnchanged(t, rw, ok)

			rw, ok = h.decide(req(medium, userMsg(secretPrompt), userMsg("next")), meta("th", "t2"))
			wantRoute(t, rw, ok, low)
		})
	}
}

func TestNoClassifierForwardsEverything(t *testing.T) {
	cat, _ := catalog.Parse([]byte(testCatalog))
	store := &catalog.Store{}
	store.Set(cat)
	e := New(Deps{Config: testConfig(), Catalog: store})
	rw, ok := e.Decide(context.Background(), req(medium, userMsg(secretPrompt)), meta("th", "t1"))
	wantUnchanged(t, rw, ok)
}

func TestManualPinAfterForeignSettingsUpdate(t *testing.T) {
	h := newHarness(t, testConfig())
	h.asker.script(answer(tier("low", 0.97)), answer(tier("low", 0.97)), answer(tier("high", 0.99)))

	h.decide(req(medium, userMsg(secretPrompt)), meta("th", "t1"))
	h.engine.SettingsUpdated("th", low)
	if s := h.engine.Snapshot(); s.Mode != ModeOn || !s.FooterSynced || s.Footer != low {
		t.Fatalf("after echo: mode %s synced %v footer %+v", s.Mode, s.FooterSynced, s.Footer)
	}

	picked := config.Route{Model: "gpt-6-sol", Effort: config.EffortLow}
	h.engine.SettingsUpdated("th", picked)
	if s := h.engine.Snapshot(); s.Mode != ModeManual {
		t.Fatalf("mode = %s, want manual", s.Mode)
	}
	rw, ok := h.decide(req(picked, userMsg(secretPrompt), userMsg("again")), meta("th", "t2"))
	wantUnchanged(t, rw, ok)

	if err := h.engine.Apply(Command{Op: OpResume}); err != nil {
		t.Fatal(err)
	}
	rw, ok = h.decide(req(picked, userMsg(secretPrompt), userMsg("again"), userMsg("more")), meta("th", "t3"))
	if !ok || h.engine.Snapshot().Mode != ModeOn {
		t.Fatalf("after resume: rewrite %+v %v, mode %s", rw, ok, h.engine.Snapshot().Mode)
	}
}

func TestEchoesOfQueuedWritesAreNotManual(t *testing.T) {
	h := newHarness(t, testConfig())
	h.asker.script(answer(tier("low", 0.97)), answer(tier("high", 0.99)))
	h.decide(req(medium, userMsg(secretPrompt)), meta("th", "t1"))
	h.decide(req(low, userMsg(secretPrompt), userMsg("big refactor")), meta("th", "t2"))

	h.engine.SettingsUpdated("th", low)
	h.engine.SettingsUpdated("th", high)

	if s := h.engine.Snapshot(); s.Mode != ModeOn || !s.FooterSynced {
		t.Fatalf("mode %s synced %v", s.Mode, s.FooterSynced)
	}
}

func TestSettingsUpdateBeforeAnyWriteIsNotManual(t *testing.T) {
	h := newHarness(t, testConfig())
	h.asker.script(answer(tier("medium", 0.97)))
	h.decide(req(medium, userMsg(secretPrompt)), meta("th", "t1"))

	h.engine.SettingsUpdated("th", low)

	if s := h.engine.Snapshot(); s.Mode != ModeOn {
		t.Fatalf("mode = %s, want on", s.Mode)
	}
}

func TestPauseForwardsUnchangedAndRestoresTheFooter(t *testing.T) {
	h := newHarness(t, testConfig())
	h.asker.script(answer(tier("low", 0.97)), answer(tier("low", 0.97)))
	h.decide(req(medium, userMsg(secretPrompt)), meta("th", "t1"))

	if err := h.engine.Apply(Command{Op: OpPause}); err != nil {
		t.Fatal(err)
	}
	restore := h.last()
	if restore.Reason != policy.ReasonPaused || restore.Route != medium || restore.ThreadID != "th" {
		t.Fatalf("restore write = %+v", restore)
	}
	rw, ok := h.decide(req(medium, userMsg(secretPrompt), userMsg("next")), meta("th", "t2"))
	wantUnchanged(t, rw, ok)
	if s := h.engine.Snapshot(); s.Mode != ModePaused {
		t.Fatalf("mode = %s", s.Mode)
	}
	h.engine.SettingsUpdated("th", medium)
	if s := h.engine.Snapshot(); !s.FooterSynced {
		t.Fatal("restore echo not taken as an echo")
	}

	if err := h.engine.Apply(Command{Op: OpResume}); err != nil {
		t.Fatal(err)
	}
	rw, ok = h.decide(req(medium, userMsg(secretPrompt), userMsg("next"), userMsg("more")), meta("th", "t3"))
	wantRoute(t, rw, ok, low)
}

func TestPinNextTurnIsConsumedOnce(t *testing.T) {
	h := newHarness(t, testConfig())
	h.asker.script(answer(tier("medium", 0.99)))
	if err := h.engine.Apply(Command{Op: OpPin, Tier: config.TierHigh}); err != nil {
		t.Fatal(err)
	}
	m := meta("th", "t1")
	items := []responses.Item{userMsg(secretPrompt)}

	rw, ok := h.decide(req(medium, items...), m)
	wantRoute(t, rw, ok, high)
	if d := h.last(); d.Reason != policy.ReasonPinned || h.engine.Snapshot().Pin != nil {
		t.Fatalf("decision = %+v, pin = %v", d, h.engine.Snapshot().Pin)
	}
	items = append(items, call("c1", "ls"), result("c1", 0, ""))
	rw, ok = h.decide(req(medium, items...), m)
	wantRoute(t, rw, ok, high)
	if h.asker.Calls() != 0 {
		t.Fatalf("classifier calls = %d during a pinned turn", h.asker.Calls())
	}

	rw, ok = h.decide(req(high, append(items, userMsg("next"))...), meta("th", "t2"))
	if d := h.last(); d.Reason == policy.ReasonPinned || h.asker.Calls() != 1 {
		t.Fatalf("second turn: decision %+v rewrite %+v %v calls %d", d, rw, ok, h.asker.Calls())
	}
}

func TestUnpinClearsThePin(t *testing.T) {
	h := newHarness(t, testConfig())
	h.asker.script(answer(tier("medium", 0.99)))
	_ = h.engine.Apply(Command{Op: OpPin, Tier: config.TierHigh})
	_ = h.engine.Apply(Command{Op: OpUnpin})

	rw, ok := h.decide(req(medium, userMsg(secretPrompt)), meta("th", "t1"))

	wantUnchanged(t, rw, ok)
	if d := h.last(); d.Reason != policy.ReasonSameTier {
		t.Fatalf("decision = %+v", d)
	}
}

func TestApplyRejectsBadCommands(t *testing.T) {
	h := newHarness(t, testConfig())
	for _, c := range []Command{
		{Op: OpPin, Tier: "huge"},
		{Op: OpPreset, Preset: "lavish"},
		{Op: "explode"},
	} {
		if err := h.engine.Apply(c); err == nil {
			t.Errorf("Apply(%+v) = nil, want an error", c)
		}
	}
}

func TestPresetChangesTheEffectivePolicy(t *testing.T) {
	h := newHarness(t, testConfig())
	if err := h.engine.Apply(Command{Op: OpPreset, Preset: config.PresetQuality}); err != nil {
		t.Fatal(err)
	}
	s := h.engine.Snapshot()
	if s.Preset != config.PresetQuality || s.Policy.UpgradeVotes != 1 {
		t.Fatalf("preset %s, upgrade votes %d", s.Preset, s.Policy.UpgradeVotes)
	}
}

func TestEscalationOnRepeatedFailureInsideATurn(t *testing.T) {
	h := newHarness(t, testConfig())
	h.asker.script(answer(tier("medium", 0.99)))
	m := meta("th", "t1")
	failing := func(id string) []responses.Item {
		return []responses.Item{call(id, "go test ./pkg/queue"), result(id, 1, "--- FAIL: TestRetry\nretry_test.go:48: attempts = 2, want 3")}
	}
	items := []responses.Item{userMsg(secretPrompt)}
	h.decide(req(medium, items...), m)
	items = append(items, failing("c1")...)
	rw, ok := h.decide(req(medium, items...), m)
	wantUnchanged(t, rw, ok)

	items = append(append(items, editGo("c2"), edited("c2")), failing("c3")...)
	rw, ok = h.decide(req(medium, items...), m)

	wantRoute(t, rw, ok, high)
	if d := h.last(); d.Reason != policy.ReasonEscalation || !d.MidTurn {
		t.Fatalf("decision = %+v", d)
	}
	items = append(items, call("c4", "ls"), result("c4", 0, ""))
	rw, ok = h.decide(req(medium, items...), m)
	wantRoute(t, rw, ok, high)
	if d := h.last(); d.Reason == policy.ReasonEscalation {
		t.Fatal("the same failure escalated twice")
	}
}

func TestReviewEveryFourRequestsAppliesAtTheNextStep(t *testing.T) {
	h := newHarness(t, testConfig())
	h.asker.script(answer(tier("medium", 0.99)), answer(tier("high", 0.99)))
	m := meta("th", "t1")
	items := []responses.Item{userMsg(secretPrompt)}
	step := func(i int) (responses.Rewrite, bool) {
		items = append(items, call(fmt.Sprint("c", i), "ls"), result(fmt.Sprint("c", i), 0, ""))
		return h.decide(req(medium, items...), m)
	}
	h.decide(req(medium, items...), m)
	for i := 1; i <= 3; i++ {
		rw, ok := step(i)
		wantUnchanged(t, rw, ok)
	}
	if h.asker.Calls() != 1 {
		t.Fatalf("calls before the review = %d, want 1", h.asker.Calls())
	}

	rw, ok := step(4)
	wantUnchanged(t, rw, ok)
	h.engine.reviews.Wait()
	if h.asker.Calls() != 2 {
		t.Fatalf("calls after the 5th request = %d, want 2", h.asker.Calls())
	}

	rw, ok = step(5)
	wantRoute(t, rw, ok, high)
	if d := h.last(); d.Reason != policy.ReasonUpgrade || !d.MidTurn {
		t.Fatalf("decision = %+v", d)
	}
}

func TestNewTurnDropsAPendingReview(t *testing.T) {
	h := newHarness(t, testConfig())
	h.asker.script(answer(tier("medium", 0.99)), answer(tier("high", 0.99)), answer(tier("medium", 0.99)))
	m := meta("th", "t1")
	items := []responses.Item{userMsg(secretPrompt)}
	h.decide(req(medium, items...), m)
	for i := 1; i <= 4; i++ {
		items = append(items, call(fmt.Sprint("c", i), "ls"), result(fmt.Sprint("c", i), 0, ""))
		h.decide(req(medium, items...), m)
	}
	h.engine.reviews.Wait()

	rw, ok := h.decide(req(medium, append(items, userMsg("next"))...), meta("th", "t2"))

	wantUnchanged(t, rw, ok)
	if d := h.last(); d.Reason != policy.ReasonSameTier {
		t.Fatalf("decision = %+v", d)
	}
}

func TestActivityOverrideRoutesInsideTheTier(t *testing.T) {
	h := newHarness(t, testConfig())
	h.asker.script(answer(withActivity(tier("medium", 0.99), config.ActivityDebug, 0.8)))

	rw, ok := h.decide(req(medium, userMsg(secretPrompt)), meta("th", "t1"))

	wantRoute(t, rw, ok, debug)
	if d := h.last(); d.Activity != config.ActivityDebug || d.Reason != ReasonActivityUp {
		t.Fatalf("decision = %+v", d)
	}
}

func TestWeakActivityKeepsTheBaseRoute(t *testing.T) {
	h := newHarness(t, testConfig())
	h.asker.script(answer(withActivity(tier("medium", 0.99), config.ActivityDebug, 0.4)))

	rw, ok := h.decide(req(medium, userMsg(secretPrompt)), meta("th", "t1"))

	wantUnchanged(t, rw, ok)
}

func TestObservedToolCallsMoveTheTurnUp(t *testing.T) {
	cfg := testConfig()
	cfg.Activities[config.ActivityCode] = map[config.Tier]config.Route{config.TierMedium: {Effort: config.EffortHigh}}
	h := newHarness(t, cfg)
	h.asker.script(answer(withActivity(tier("medium", 0.99), config.ActivityExplore, 0.9)))
	m := meta("th", "t1")
	items := []responses.Item{userMsg(secretPrompt)}
	h.decide(req(medium, items...), m)

	items = append(items, editGo("c1"), edited("c1"))
	rw, ok := h.decide(req(medium, items...), m)

	wantRoute(t, rw, ok, debug)
	if d := h.last(); d.Reason != ReasonObserved || d.Activity != config.ActivityCode {
		t.Fatalf("decision = %+v", d)
	}
}

func TestDowngradeOnTheSameTaskWaitsForDwellAndACheck(t *testing.T) {
	tests := []struct {
		name     string
		steps    int
		check    int
		want     config.Route
		rewrites bool
		reason   policy.Reason
	}{
		{"dwell passed and check passed", 2, 0, low, true, policy.ReasonDowngrade},
		{"check failed", 2, 1, high, false, policy.ReasonDowngradeHeld},
		{"dwell not passed", 0, 0, high, false, policy.ReasonDowngradeHeld},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := testConfig()
			cfg.Policy.MinDwellRequests = 2
			cfg.Policy.ReviewEveryRequests = 0
			h := newHarness(t, cfg)
			sameTask := continuing(tier("low", 0.99), 0.9)
			h.asker.script(answer(sameTask), answer(sameTask))
			m := meta("th", "t1")
			items := []responses.Item{userMsg(secretPrompt)}
			rw, ok := h.decide(req(high, items...), m)
			wantUnchanged(t, rw, ok)
			if d := h.last(); d.Reason != policy.ReasonDowngradeHeld {
				t.Fatalf("first turn = %+v", d)
			}
			for i := 1; i <= tt.steps; i++ {
				items = append(items, call(fmt.Sprint("c", i), "ls"), result(fmt.Sprint("c", i), 0, ""))
				h.decide(req(high, items...), m)
			}

			items = append(items, call("t", "go test ./..."), result("t", tt.check, "ok"), userMsg("now tidy up"))
			rw, ok = h.decide(req(high, items...), meta("th", "t2"))

			if ok != tt.rewrites || (ok && (rw.Model != tt.want.Model || rw.Effort != tt.want.Effort)) {
				t.Fatalf("rewrite = %+v %v, want %+v", rw, ok, tt.want)
			}
			if d := h.last(); d.Reason != tt.reason {
				t.Fatalf("decision = %+v", d)
			}
		})
	}
}

func TestContextFitGuard(t *testing.T) {
	t.Run("a thread too big for the chosen route moves up", func(t *testing.T) {
		h := newHarness(t, testConfig())
		h.asker.script(answer(tier("low", 0.99)))
		r := req(medium, userMsg(secretPrompt))
		r.Bytes = 4 * 250_000

		rw, ok := h.decide(r, meta("th", "t1"))

		wantRoute(t, rw, ok, high)
		if d := h.last(); d.Reason != policy.ReasonFit {
			t.Fatalf("decision = %+v", d)
		}
	})
	t.Run("the last reply's size counts", func(t *testing.T) {
		h := newHarness(t, testConfig())
		h.asker.script(answer(tier("medium", 0.99)), answer(tier("low", 0.99)))
		m := meta("th", "t1")
		h.decide(req(medium, userMsg(secretPrompt)), m)
		h.reply(m, "gpt-6-sol", 240_000, 0)

		rw, ok := h.decide(req(medium, userMsg(secretPrompt), userMsg("next")), meta("th", "t2"))

		wantRoute(t, rw, ok, high)
	})
	t.Run("nothing fits forwards the request", func(t *testing.T) {
		h := newHarness(t, testConfig())
		h.asker.script(answer(tier("low", 0.99)))
		r := req(medium, userMsg(secretPrompt))
		r.Bytes = 4 * 400_000

		rw, ok := h.decide(r, meta("th", "t1"))

		wantUnchanged(t, rw, ok)
	})
	t.Run("a classifier failure still moves up to a baseline that fits", func(t *testing.T) {
		cfg := testConfig()
		cfg.Baseline = config.TierHigh
		h := newHarness(t, cfg)
		h.asker.script(fail(classifier.ErrTimeout))
		r := req(low, userMsg(secretPrompt))
		r.Bytes = 4 * 250_000

		rw, ok := h.decide(r, meta("th", "t1"))

		wantRoute(t, rw, ok, high)
		if d := h.last(); d.Reason != policy.ReasonFit {
			t.Fatalf("decision = %+v", d)
		}
	})
}

func TestUnsupportedEffortIsNotUsed(t *testing.T) {
	cfg := testConfig()
	cfg.Routes[config.TierLow] = config.Route{Model: "gpt-6-luna", Effort: "max"}
	h := newHarness(t, cfg)
	h.asker.script(answer(tier("low", 0.99)))

	rw, ok := h.decide(req(medium, userMsg(secretPrompt)), meta("th", "t1"))

	wantUnchanged(t, rw, ok)
}

func TestSnapshotStats(t *testing.T) {
	cfg := testConfig()
	cfg.Prices = map[string]config.Price{
		"gpt-6-luna": {Input: 1, CachedInput: 0.1, Output: 4},
		"gpt-6-sol":  {Input: 2, CachedInput: 0.2, Output: 8},
	}
	h := newHarness(t, cfg)
	h.asker.script(answer(tier("low", 0.97)))
	m := meta("th", "t1")
	items := []responses.Item{userMsg(secretPrompt)}

	h.decide(req(medium, items...), m)
	h.reply(m, "gpt-6-luna", 1000, 0)
	items = append(items, call("c1", "ls"), result("c1", 0, ""))
	h.decide(req(medium, items...), m)
	h.reply(m, "gpt-6-luna", 1000, 900)

	s := h.engine.Snapshot()
	st := s.Stats
	if st.Replies != 2 || st.Switches != 1 || st.ByTier[config.TierLow] != 2 || len(st.Strip) != 2 {
		t.Fatalf("stats = %+v", st)
	}
	if st.CacheReusePct != 45 || st.LastSwitch == nil || st.LastSwitch.To != config.TierLow || st.LastSwitch.From != config.TierMedium {
		t.Fatalf("stats = %+v, last switch %+v", st, st.LastSwitch)
	}
	if st.RoutedUSD <= 0 || st.BaselineUSD <= st.RoutedUSD {
		t.Fatalf("usd routed %v baseline %v", st.RoutedUSD, st.BaselineUSD)
	}
	if !st.Since.Equal(h.clock.Now()) || s.Baseline != medium || s.ClassifierLabel != "Jev" || s.Window != 258400 {
		t.Fatalf("snapshot = %+v", s)
	}
	if s.Last == nil || len(s.Recent) != 2 || s.Group.CompHash != "3000" {
		t.Fatalf("snapshot last %+v recent %d group %+v", s.Last, len(s.Recent), s.Group)
	}
}

func TestPassthroughRepliesAreNotCounted(t *testing.T) {
	h := newHarness(t, testConfig())
	h.asker.script(answer(tier("low", 0.97)))
	m := meta("th", "t1")
	h.decide(req(medium, userMsg(secretPrompt)), m)
	h.reply(m, "gpt-6-luna", 1000, 0)

	h.decide(req(medium, userMsg("You are performing a CONTEXT CHECKPOINT")), m)
	h.reply(m, "gpt-6-sol", 1000, 0)

	if got := h.engine.Snapshot().Stats.Replies; got != 1 {
		t.Fatalf("replies = %d, want 1", got)
	}
}

func TestStripMergesPairsPastItsWidth(t *testing.T) {
	ta := newTally()
	for i := range 100 {
		tr := config.TierLow
		if i%2 == 1 {
			tr = config.TierHigh
		}
		ta.push(tr)
	}
	if len(ta.Strip) > StripCells || ta.RepliesPerCell != 4 || len(ta.Strip) != 25 {
		t.Fatalf("cells %d per %d", len(ta.Strip), ta.RepliesPerCell)
	}
	if ta.Strip[len(ta.Strip)-1] != config.TierHigh {
		t.Fatalf("last cell = %s, want the last reply's tier", ta.Strip[len(ta.Strip)-1])
	}
}

func TestThreadsAreBounded(t *testing.T) {
	h := newHarness(t, testConfig())
	for range MaxThreads + 2 {
		h.asker.script(answer(tier("low", 0.97)))
	}
	r := req(medium, userMsg(secretPrompt))
	h.decide(r, meta("t0", "turn"))
	for i := 1; i <= MaxThreads; i++ {
		h.decide(req(medium, userMsg(secretPrompt)), meta(fmt.Sprint("t", i), "turn"))
	}

	h.decide(r, meta("t0", "turn"))

	if got := h.asker.Calls(); got != MaxThreads+2 {
		t.Fatalf("classifier calls = %d, want the evicted thread to start over", got)
	}
}

func TestThreadsDoNotShareState(t *testing.T) {
	h := newHarness(t, testConfig())
	h.asker.script(answer(tier("low", 0.97)), answer(tier("high", 0.99)))

	a, okA := h.decide(req(medium, userMsg(secretPrompt)), meta("a", "t1"))
	b, okB := h.decide(req(medium, userMsg(secretPrompt)), meta("b", "t1"))

	wantRoute(t, a, okA, low)
	wantRoute(t, b, okB, high)
}

func TestSinkPanicDoesNotBreakTheRequest(t *testing.T) {
	cat, _ := catalog.Parse([]byte(testCatalog))
	store := &catalog.Store{}
	store.Set(cat)
	asker := (&fakeAsker{}).script(answer(tier("low", 0.97)))
	e := New(Deps{Config: testConfig(), Catalog: store, Classifier: asker, Sink: panicSink{}})

	rw, ok := e.Decide(context.Background(), req(medium, userMsg(secretPrompt)), meta("th", "t1"))

	wantRoute(t, rw, ok, low)
}

type panicSink struct{}

func (panicSink) OnDecision(Decision) { panic("sink") }
func (panicSink) OnSnapshot(Snapshot) { panic("sink") }

func TestConcurrentThreads(t *testing.T) {
	h := newHarness(t, testConfig())
	var wg sync.WaitGroup
	for i := range 16 {
		h.asker.script(answer(tier("low", 0.97)))
		wg.Go(func() {
			m := meta(fmt.Sprint("th", i), "t1")
			h.decide(req(medium, userMsg(secretPrompt)), m)
			h.reply(m, "gpt-6-luna", 1000, 0)
			h.engine.Snapshot()
		})
	}
	wg.Wait()
	if got := h.engine.Snapshot().Stats.Replies; got != 16 {
		t.Fatalf("replies = %d, want 16", got)
	}
}
