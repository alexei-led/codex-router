package route

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/alexei-led/codex-router/internal/config"
	"github.com/alexei-led/codex-router/internal/home"
	"github.com/alexei-led/codex-router/internal/policy"
	"github.com/alexei-led/codex-router/internal/responses"
)

// firstStep runs a turn's first step on thread th with one user message.
func (h *harness) firstStep(th, turn string) (responses.Rewrite, bool) {
	h.t.Helper()
	return h.decide(req(medium, userMsg(secretPrompt), userMsg(turn)), meta(th, turn))
}

func (h *harness) apply(c Command) {
	h.t.Helper()
	if err := h.engine.Apply(c); err != nil {
		h.t.Fatalf("Apply(%+v) = %v", c, err)
	}
}

func (h *harness) session(id string) SessionInfo {
	h.t.Helper()
	for _, s := range h.engine.Sessions() {
		if s.ThreadID == id {
			return s
		}
	}
	h.t.Fatalf("no session %q", id)
	return SessionInfo{}
}

func threadIDs(ds []Decision) []string {
	var out []string
	for _, d := range ds {
		out = append(out, d.ThreadID)
	}
	return out
}

func TestInterleavedThreadsKeepTheirOwnTallies(t *testing.T) {
	h := newHarness(t, testConfig())
	h.asker.script(answer(tier("low", 0.97)), answer(tier("high", 0.99)), answer(tier("medium", 0.99)), answer(tier("low", 0.97)))

	h.firstStep("a", "a1")
	h.reply(meta("a", "a1"), low.Model, 1000, 0)
	h.firstStep("b", "b1")
	h.reply(meta("b", "b1"), high.Model, 1000, 0)
	h.firstStep("c", "c1")
	h.reply(meta("c", "c1"), medium.Model, 1000, 0)
	h.firstStep("a", "a2")
	h.reply(meta("a", "a2"), low.Model, 1000, 900)

	tests := []struct {
		thread     string
		replies    int
		switches   int
		strip      []config.Tier
		lastSwitch *Switch
	}{
		{"a", 2, 1, []config.Tier{config.TierLow, config.TierLow}, &Switch{From: config.TierMedium, To: config.TierLow}},
		{"b", 1, 1, []config.Tier{config.TierHigh}, &Switch{From: config.TierMedium, To: config.TierHigh}},
		{"c", 1, 0, []config.Tier{config.TierMedium}, nil},
	}
	for _, tt := range tests {
		s := h.engine.SnapshotFor(tt.thread)
		info := h.session(tt.thread)
		if s.ThreadID != tt.thread || s.Stats.Replies != tt.replies || info.Replies != tt.replies {
			t.Errorf("%s: snapshot thread %q replies %d, info replies %d; want %d", tt.thread, s.ThreadID, s.Stats.Replies, info.Replies, tt.replies)
		}
		if info.Switches != tt.switches || s.Stats.Switches != tt.switches {
			t.Errorf("%s: switches %d / %d, want %d", tt.thread, s.Stats.Switches, info.Switches, tt.switches)
		}
		if !slices.Equal(s.Stats.Strip, tt.strip) || !slices.Equal(info.Strip, tt.strip) {
			t.Errorf("%s: strip %v / %v, want %v", tt.thread, s.Stats.Strip, info.Strip, tt.strip)
		}
		if got := s.Stats.LastSwitch; (got == nil) != (tt.lastSwitch == nil) ||
			got != nil && (got.From != tt.lastSwitch.From || got.To != tt.lastSwitch.To) {
			t.Errorf("%s: last switch %+v, want %+v", tt.thread, got, tt.lastSwitch)
		}
		if ids := threadIDs(s.Recent); len(ids) != tt.replies || slices.ContainsFunc(ids, func(id string) bool { return id != tt.thread }) {
			t.Errorf("%s: recent threads %v", tt.thread, ids)
		}
		if s.Last == nil || s.Last.ThreadID != tt.thread {
			t.Errorf("%s: last decision %+v", tt.thread, s.Last)
		}
	}
}

func TestBusyThreadDoesNotPushOutAnothersDecisions(t *testing.T) {
	h := newHarness(t, testConfig())
	h.asker.script(answer(tier("low", 0.97)))
	h.firstStep("quiet", "q1")

	for i := range RecentKept + 6 {
		h.firstStep("busy", fmt.Sprint("b", i))
	}

	if got := h.engine.SnapshotFor("busy").Recent; len(got) != RecentKept {
		t.Fatalf("busy recent = %d, want %d", len(got), RecentKept)
	}
	quiet := h.engine.SnapshotFor("quiet")
	if len(quiet.Recent) != 1 || quiet.Recent[0].TurnID != "q1" || quiet.Last.TurnID != "q1" {
		t.Fatalf("quiet recent = %+v", quiet.Recent)
	}
}

func TestSessionPauseLeavesOtherThreadsRouted(t *testing.T) {
	h := newHarness(t, testConfig())
	h.asker.script(answer(tier("low", 0.97)), answer(tier("low", 0.97)), answer(tier("low", 0.97)), answer(tier("low", 0.97)))
	h.firstStep("a", "a1")
	h.firstStep("b", "b1")
	before := len(h.sink.Decisions())

	h.apply(Command{Op: OpPause, Session: "a", Scope: ScopeSession})

	writes := h.sink.Decisions()[before:]
	if len(writes) != 1 || writes[0].ThreadID != "a" || writes[0].Reason != policy.ReasonPaused || writes[0].Route != medium {
		t.Fatalf("restore writes = %+v, want one for a back to %+v", writes, medium)
	}
	rw, ok := h.firstStep("a", "a2")
	wantUnchanged(t, rw, ok)
	rw, ok = h.firstStep("b", "b2")
	wantRoute(t, rw, ok, low)
	if a, b := h.engine.SnapshotFor("a").Mode, h.engine.SnapshotFor("b").Mode; a != ModePaused || b != ModeOn {
		t.Fatalf("modes a %s b %s", a, b)
	}

	h.apply(Command{Op: OpResume, Session: "a", Scope: ScopeSession})
	rw, ok = h.firstStep("a", "a3")
	wantRoute(t, rw, ok, low)
}

func TestHomePauseCoversEveryThread(t *testing.T) {
	h := newHarness(t, testConfig())
	h.asker.script(answer(tier("low", 0.97)), answer(tier("low", 0.97)), answer(tier("low", 0.97)))
	h.firstStep("a", "a1")
	h.firstStep("b", "b1")
	before := len(h.sink.Decisions())

	h.apply(Command{Op: OpPause, Session: "a", Scope: ScopeHome})

	if got := threadIDs(h.sink.Decisions()[before:]); !slices.Equal(got, []string{"b", "a"}) {
		t.Fatalf("restore writes for %v, want b and a", got)
	}
	for _, th := range []string{"a", "b"} {
		rw, ok := h.firstStep(th, th+"2")
		wantUnchanged(t, rw, ok)
		if s := h.session(th); s.Mode != ModePaused || s.Attention != AttentionPaused || s.Route != medium {
			t.Fatalf("%s: %+v", th, s)
		}
	}
	if h.asker.Calls() != 2 {
		t.Fatalf("classifier calls = %d while paused", h.asker.Calls())
	}

	h.apply(Command{Op: OpPause, Session: "a"})
	h.apply(Command{Op: OpResume})

	if a, b := h.session("a").Mode, h.session("b").Mode; a != ModePaused || b != ModeOn {
		t.Fatalf("after home resume: a %s b %s; want the session pause kept", a, b)
	}
	rw, ok := h.firstStep("b", "b3")
	wantRoute(t, rw, ok, low)
}

func TestTwoPinsOnOneThreadLastWinsOnce(t *testing.T) {
	h := newHarness(t, testConfig())
	h.asker.script(answer(tier("medium", 0.99)))

	h.apply(Command{Op: OpPin, Session: "a", Tier: config.TierHigh})
	h.apply(Command{Op: OpPin, Session: "a", Tier: config.TierLow})
	if p := h.engine.SnapshotFor("a").Pin; p == nil || *p != config.TierLow {
		t.Fatalf("pin = %v, want low", p)
	}

	rw, ok := h.firstStep("a", "a1")
	wantRoute(t, rw, ok, low)
	if d := h.last(); d.Reason != policy.ReasonPinned || h.engine.SnapshotFor("a").Pin != nil {
		t.Fatalf("decision = %+v, pin = %v", d, h.engine.SnapshotFor("a").Pin)
	}
	h.firstStep("a", "a2")
	if d := h.last(); d.Reason == policy.ReasonPinned || h.asker.Calls() != 1 {
		t.Fatalf("second turn decision = %+v, calls %d", d, h.asker.Calls())
	}
}

func TestPinOnOneThreadIsNotUsedByAnother(t *testing.T) {
	h := newHarness(t, testConfig())
	h.asker.script(answer(tier("medium", 0.99)))
	h.apply(Command{Op: OpPin, Session: "a", Tier: config.TierHigh})
	// A pending pin is intent the user must be able to see and clear, even
	// before the thread's first step. It still must not affect thread b.
	if got := h.engine.Sessions(); !slices.ContainsFunc(got, func(s SessionInfo) bool {
		return s.ThreadID == "a" && s.PendingPin
	}) {
		t.Fatalf("a pending pin is not listed: %+v", got)
	}

	rw, ok := h.firstStep("b", "b1")
	wantUnchanged(t, rw, ok)
	if d := h.last(); d.ThreadID != "b" || d.Reason == policy.ReasonPinned {
		t.Fatalf("b decision = %+v", d)
	}
	rw, ok = h.firstStep("a", "a1")
	wantRoute(t, rw, ok, high)
	if d := h.last(); d.Reason != policy.ReasonPinned {
		t.Fatalf("a decision = %+v", d)
	}
}

func TestSessionsOrderAndAttention(t *testing.T) {
	cwds := map[string]string{"esc": t.TempDir(), "idle": t.TempDir()}
	hm := home.Home{ID: "3fa9c0d1e2b4", Path: "/h/.codex-work", Name: "work"}
	h := newHarness(t, testConfig(), func(d *Deps) {
		d.Home = hm
		d.Cwd = func(id string) string { return cwds[id] }
	})
	h.asker.script(answer(tier("medium", 0.99)), answer(tier("medium", 0.99)), answer(tier("medium", 0.99)), answer(tier("medium", 0.99)))
	step := func(th string) {
		h.clock.Advance(time.Second)
		h.firstStep(th, th+"1")
	}
	step("idle")
	step("manual")
	step("paused")
	h.clock.Advance(time.Second)
	m := meta("esc", "e1")
	failing := func(id string) []responses.Item {
		return []responses.Item{call(id, "go test ./pkg/queue"), result(id, 1, "--- FAIL: TestRetry\nretry_test.go:48: attempts = 2, want 3")}
	}
	items := []responses.Item{userMsg(secretPrompt)}
	h.decide(req(medium, items...), m)
	items = append(append(append(items, failing("c1")...), editGo("c2"), edited("c2")), failing("c3")...)
	h.decide(req(medium, items...), m)
	if d := h.last(); d.Reason != policy.ReasonEscalation {
		t.Fatalf("esc decision = %+v", d)
	}
	h.engine.SettingsUpdated("manual", config.Route{Model: "gpt-6-luna", Effort: config.EffortHigh})
	h.apply(Command{Op: OpPause, Session: "paused"})

	got := h.engine.Sessions()

	want := []struct {
		thread    string
		attention Attention
		mode      Mode
	}{
		{"esc", AttentionEscalation, ModeOn},
		{"paused", AttentionPaused, ModePaused},
		{"manual", AttentionManual, ModeManual},
		{"idle", "", ModeOn},
	}
	if len(got) != len(want) {
		t.Fatalf("sessions = %+v", got)
	}
	for i, w := range want {
		s := got[i]
		if s.ThreadID != w.thread || s.Attention != w.attention || s.Mode != w.mode {
			t.Errorf("sessions[%d] = %s %q %s, want %s %q %s", i, s.ThreadID, s.Attention, s.Mode, w.thread, w.attention, w.mode)
		}
		if s.ID != hm.ID+"/"+w.thread || s.Home != hm.ID || s.Cwd != cwds[w.thread] {
			t.Errorf("sessions[%d] id %q home %q cwd %q", i, s.ID, s.Home, s.Cwd)
		}
	}
	if l := got[0].Label; l != "work · "+filepath.Base(cwds["esc"]) {
		t.Errorf("label = %q", l)
	}
	if l := got[1].Label; l != "work" {
		t.Errorf("label without cwd = %q", l)
	}
	if got[0].Route != high || got[0].Tier != config.TierHigh || got[0].LastActive.IsZero() {
		t.Errorf("esc route %+v tier %s active %v", got[0].Route, got[0].Tier, got[0].LastActive)
	}
}

func TestSessionsHideIdleThreads(t *testing.T) {
	ids := func(ss []SessionInfo) []string {
		var out []string
		for _, s := range ss {
			out = append(out, s.ThreadID)
		}
		return out
	}
	tests := []struct {
		name    string
		oldIdle time.Duration
		want    []string
	}{
		{"just under the threshold", sessionIdle - time.Second, []string{"new", "old"}},
		{"at the threshold", sessionIdle, []string{"new"}},
		{"long idle keeps the latest", 2 * time.Hour, []string{"new"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t, testConfig())
			h.asker.script(answer(tier("low", 0.97)), answer(tier("low", 0.97)))
			h.firstStep("old", "o1")
			h.clock.Advance(time.Second)
			h.firstStep("new", "n1")

			h.clock.Advance(tt.oldIdle - time.Second)

			if got := ids(h.engine.Sessions()); !slices.Equal(got, tt.want) {
				t.Fatalf("sessions = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestSessionsKeepIntentRowsPastIdleExpiry(t *testing.T) {
	ids := func(ss []SessionInfo) []string {
		var out []string
		for _, s := range ss {
			out = append(out, s.ThreadID)
		}
		return out
	}
	for _, tc := range []struct {
		name   string
		intent func(*harness)
		want   Attention
	}{
		{"paused", func(h *harness) { h.apply(Command{Op: OpPause, Session: "old"}) }, AttentionPaused},
		{"manual", func(h *harness) {
			h.engine.SettingsUpdated("old", config.Route{Model: "gpt-6-luna", Effort: config.EffortHigh})
		}, AttentionManual},
		{"pending pin", func(h *harness) { h.apply(Command{Op: OpPin, Session: "old", Tier: config.TierHigh}) }, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, testConfig())
			h.asker.script(answer(tier("medium", 0.97)), answer(tier("medium", 0.97)))
			h.firstStep("old", "old-1")
			tc.intent(h)
			h.clock.Advance(time.Second)
			h.firstStep("new", "new-1")
			h.clock.Advance(11 * time.Minute)

			if got := ids(h.engine.Sessions()); !slices.Contains(got, "old") {
				t.Fatalf("sessions = %v, want intent-bearing old thread retained", got)
			}
			var old SessionInfo
			for _, s := range h.engine.Sessions() {
				if s.ThreadID == "old" {
					old = s
				}
			}
			if tc.name == "pending pin" {
				if !old.PendingPin {
					t.Error("pending pin was not exposed on SessionInfo")
				}
				data, err := json.Marshal(old)
				if err != nil {
					t.Fatal(err)
				}
				var wire map[string]any
				if err := json.Unmarshal(data, &wire); err != nil {
					t.Fatal(err)
				}
				if wire["pending_pin"] != true {
					t.Errorf("pending_pin JSON = %v, want true (%s)", wire["pending_pin"], data)
				}
			} else if old.Attention != tc.want {
				t.Errorf("attention = %q, want %q", old.Attention, tc.want)
			}
			if slices.Contains(ids(h.engine.Sessions()), "new") == false {
				t.Fatal("latest idle thread disappeared")
			}
		})
	}
}

func TestOrdinaryIdleThreadStillExpiresBesideLatest(t *testing.T) {
	h := newHarness(t, testConfig())
	h.asker.script(answer(tier("medium", 0.97)), answer(tier("medium", 0.97)))
	h.firstStep("old", "old-1")
	h.clock.Advance(time.Second)
	h.firstStep("new", "new-1")
	h.clock.Advance(11 * time.Minute)
	var got []string
	for _, s := range h.engine.Sessions() {
		got = append(got, s.ThreadID)
	}
	if !slices.Equal(got, []string{"new"}) {
		t.Fatalf("sessions = %v, want only latest new thread", got)
	}
}

func TestPartialPricingDoesNotPublishSessionTotals(t *testing.T) {
	cfg := testConfig()
	cfg.Policy.UpgradeVotes = 1
	cfg.Prices = map[string]config.Price{
		medium.Model: {Input: 2, CachedInput: .2, Output: 8},
	}
	h := newHarness(t, cfg)
	h.asker.script(answer(tier("medium", .97)), answer(tier("high", .99)))
	h.firstStep("a", "a1")
	h.reply(meta("a", "a1"), medium.Model, 1000, 0)
	h.firstStep("a", "a2")
	h.reply(meta("a", "a2"), high.Model, 1000, 0)
	s := h.session("a")
	if s.PricedReplies != 1 || s.EligibleReplies != 2 {
		t.Errorf("pricing coverage = %d/%d, want 1/2", s.PricedReplies, s.EligibleReplies)
	}
	if s.SpentUSD != nil || s.SavedUSD != nil {
		t.Fatalf("partial pricing exposed session totals: spent=%v saved=%v", s.SpentUSD, s.SavedUSD)
	}
	data, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]any
	if err := json.Unmarshal(data, &wire); err != nil {
		t.Fatal(err)
	}
	if _, ok := wire["spent_usd"]; ok {
		t.Errorf("partial spent_usd present in JSON: %s", data)
	}
	if _, ok := wire["saved_usd"]; ok {
		t.Errorf("partial saved_usd present in JSON: %s", data)
	}
	if wire["priced_replies"] != float64(1) || wire["eligible_replies"] != float64(2) {
		t.Errorf("JSON pricing coverage = %v/%v, want 1/2 (%s)", wire["priced_replies"], wire["eligible_replies"], data)
	}
}

func TestPricingUsesPerReplyNativeModelsAfterModelChange(t *testing.T) {
	cfg := testConfig()
	cfg.Prices = map[string]config.Price{
		medium.Model: {Input: 2, CachedInput: .2, Output: 8},
		low.Model:    {Input: 1, CachedInput: .1, Output: 4},
	}
	h := newHarness(t, cfg)
	h.asker.script(answer(tier("medium", .97)), answer(tier("medium", .97)))
	h.firstStep("a", "a1")
	h.reply(meta("a", "a1"), medium.Model, 1000, 0)
	picked := config.Route{Model: low.Model, Effort: config.EffortHigh}
	h.engine.SettingsUpdated("a", picked)
	h.apply(Command{Op: OpResume, Session: "a"})
	if _, ok := h.decide(req(picked, userMsg(secretPrompt), userMsg("a2")), meta("a", "a2")); !ok {
		t.Fatal("second turn was not routed")
	}
	h.reply(meta("a", "a2"), medium.Model, 1000, 0)
	s := h.session("a")
	if s.PricedReplies != 2 || s.EligibleReplies != 2 {
		t.Fatalf("pricing coverage = %d/%d, want 2/2", s.PricedReplies, s.EligibleReplies)
	}
	if s.SpentUSD == nil || s.SavedUSD == nil {
		t.Fatalf("fully priced replies have no totals: %+v", s)
	}
	if *s.SpentUSD <= 0 || *s.SavedUSD >= 0 {
		t.Errorf("per-reply totals = spent %v saved %v, want positive spent and negative savings", *s.SpentUSD, *s.SavedUSD)
	}
}

func TestSessionsCarryUSDOnlyWhenPriced(t *testing.T) {
	priced := testConfig()
	priced.Prices = map[string]config.Price{
		"gpt-6-luna": {Input: 1, CachedInput: 0.1, Output: 4},
		"gpt-6-sol":  {Input: 2, CachedInput: 0.2, Output: 8},
	}
	nativeUnpriced := testConfig()
	nativeUnpriced.Prices = map[string]config.Price{low.Model: {Input: 1, CachedInput: 0.1, Output: 4}}
	tests := []struct {
		name  string
		cfg   config.Config
		price bool
	}{
		{"priced", priced, true},
		{"unpriced", testConfig(), false},
		{"native unpriced", nativeUnpriced, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t, tt.cfg)
			h.asker.script(answer(tier("low", 0.97)))
			h.firstStep("a", "a1")
			h.reply(meta("a", "a1"), low.Model, 1000, 0)

			s := h.session("a")

			if (s.SpentUSD != nil) != tt.price || (s.SavedUSD != nil) != tt.price {
				t.Fatalf("spent %v saved %v", s.SpentUSD, s.SavedUSD)
			}
			if s.EligibleReplies != 1 || (s.PricedReplies == 1) != tt.price {
				t.Fatalf("pricing coverage = %d/%d, want priced=%v", s.PricedReplies, s.EligibleReplies, tt.price)
			}
			if tt.price && (*s.SpentUSD <= 0 || *s.SavedUSD <= 0) {
				t.Fatalf("spent %v saved %v, want both positive", *s.SpentUSD, *s.SavedUSD)
			}
			if tt.name == "priced" && (*s.SpentUSD < 0.00139 || *s.SpentUSD > 0.00141 || *s.SavedUSD < 0.00139 || *s.SavedUSD > 0.00141) {
				t.Errorf("fully priced totals changed: spent %v saved %v, want about $0.0014 each", *s.SpentUSD, *s.SavedUSD)
			}
		})
	}
}

func TestSideThreadIsNotASession(t *testing.T) {
	h := newHarness(t, testConfig())
	h.asker.script(answer(tier("high", 0.99)))
	h.firstStep("main", "m1")
	title := meta("title", "t1")
	title.ThreadSource = "thread_title"

	rw, ok := h.decide(req(medium, userMsg("name this thread")), title)
	h.reply(title, medium.Model, 1000, 0)

	wantUnchanged(t, rw, ok)
	if got := h.engine.Sessions(); len(got) != 1 || got[0].ThreadID != "main" {
		t.Fatalf("sessions = %+v, want only the main thread", got)
	}
	if h.asker.Calls() != 1 {
		t.Fatalf("classifier calls = %d, want 1 (the side thread is not classified)", h.asker.Calls())
	}
}

func TestSnapshotForAnUnknownThread(t *testing.T) {
	h := newHarness(t, testConfig())
	h.apply(Command{Op: OpPause, Scope: ScopeHome})

	s := h.engine.SnapshotFor("nobody")

	if s.ThreadID != "" || s.Mode != ModePaused || s.Last != nil || s.Stats.Replies != 0 || s.Baseline != medium {
		t.Fatalf("snapshot = %+v", s)
	}
}

func TestApplyRejectsBadScopes(t *testing.T) {
	tests := []struct {
		name string
		cmd  Command
	}{
		{"pin without a session", Command{Op: OpPin, Tier: config.TierHigh}},
		{"pin on the home", Command{Op: OpPin, Session: "a", Scope: ScopeHome, Tier: config.TierHigh}},
		{"unpin without a session", Command{Op: OpUnpin}},
		{"session pause without a session", Command{Op: OpPause, Scope: ScopeSession}},
		{"session resume without a session", Command{Op: OpResume, Scope: ScopeSession}},
		{"mode on the home", Command{Op: OpMode, Scope: ScopeHome, Mode: config.ModeQuality}},
		{"mode without a session", Command{Op: OpMode, Mode: config.ModeQuality}},
		{"unknown scope", Command{Op: OpPause, Scope: "galaxy"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t, testConfig())
			if err := h.engine.Apply(tt.cmd); err == nil {
				t.Fatalf("Apply(%+v) = nil, want an error", tt.cmd)
			}
			if s := h.engine.Snapshot(); s.Mode != ModeOn || s.RoutingMode != config.ModeBalanced {
				t.Fatalf("a rejected command changed state: %+v", s)
			}
		})
	}
}

func TestRestoredIntentIsListedBeforeItsNextStep(t *testing.T) {
	tests := []struct {
		name   string
		intent ThreadIntent
	}{
		{"paused", ThreadIntent{Paused: true}},
		{"manual", ThreadIntent{Manual: true}},
		{"pinned", ThreadIntent{Pin: new(config.TierHigh)}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t, testConfig(), func(d *Deps) {
				d.Intent = Intent{Threads: map[string]ThreadIntent{"restored": tt.intent}}
			})
			if got := h.engine.Sessions(); !slices.ContainsFunc(got, func(s SessionInfo) bool { return s.ThreadID == "restored" }) {
				t.Fatalf("restored %s session is hidden before its next step: %+v", tt.name, got)
			}
		})
	}
}
