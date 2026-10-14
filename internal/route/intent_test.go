package route

import (
	"reflect"
	"sync"
	"testing"

	"github.com/alexei-led/codex-router/internal/config"
	"github.com/alexei-led/codex-router/internal/responses"
)

// savedIntents records what the engine hands to Deps.SaveIntent.
type savedIntents struct {
	mu  sync.Mutex
	all []Intent
}

func (s *savedIntents) save(in Intent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.all = append(s.all, in)
}

func (s *savedIntents) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.all)
}

func (s *savedIntents) last(t *testing.T) Intent {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.all) == 0 {
		t.Fatal("no intent saved")
	}
	return s.all[len(s.all)-1]
}

func tierPtr(t config.Tier) *config.Tier { return &t }

func withoutRev(in Intent) Intent {
	in.Rev = 0
	return in
}

func TestIntentSurvivesARestartAndLearnedStateDoesNot(t *testing.T) {
	h := newHarness(t, testConfig())
	h.asker.script(answer(tier("low", 0.97)), answer(tier("low", 0.97)))
	h.firstStep("routed", "r1")
	h.reply(meta("routed", "r1"), low.Model, 1000, 0)
	h.firstStep("manual", "m1")
	h.engine.SettingsUpdated("manual", config.Route{Model: "gpt-6-sol", Effort: config.EffortLow})
	h.apply(Command{Op: OpPin, Session: "pinned", Tier: config.TierHigh})
	h.apply(Command{Op: OpPause, Session: "paused"})
	h.apply(Command{Op: OpPreset, Preset: config.PresetQuality, Scope: ScopeHome})
	h.apply(Command{Op: OpPause, Scope: ScopeHome})

	saved := h.engine.Intent()
	want := Intent{Paused: true, Preset: config.PresetQuality, Threads: map[string]ThreadIntent{
		"manual": {Manual: true},
		"pinned": {Pin: tierPtr(config.TierHigh)},
		"paused": {Paused: true},
	}}
	if got := withoutRev(saved); !reflect.DeepEqual(got, want) {
		t.Fatalf("intent = %+v, want %+v", got, want)
	}

	r := newHarness(t, testConfig(), func(d *Deps) { d.Intent = saved })
	if got := withoutRev(r.engine.Intent()); !reflect.DeepEqual(got, want) {
		t.Fatalf("restored intent = %+v, want %+v", got, want)
	}
	if s := r.engine.SnapshotFor("routed"); s.Stats.Replies != 0 || s.Last != nil || len(s.Recent) != 0 || s.Mode != ModePaused {
		t.Fatalf("routed thread after restart = %+v, want no learned state", s)
	}
	if s := r.engine.SnapshotFor("manual"); s.Mode != ModePaused || s.Preset != config.PresetQuality {
		t.Fatalf("manual thread under the home pause = mode %s preset %s", s.Mode, s.Preset)
	}

	r.asker.script(answer(tier("low", 0.97)))
	r.apply(Command{Op: OpResume, Session: "manual"})
	if s := r.engine.SnapshotFor("manual"); s.Mode != ModePaused {
		t.Fatalf("manual thread after a session resume = %s, want the home pause kept", s.Mode)
	}
	r.apply(Command{Op: OpResume, Scope: ScopeHome})
	rw, ok := r.firstStep("pinned", "p1")
	wantRoute(t, rw, ok, high)
	rw, ok = r.firstStep("paused", "x1")
	wantUnchanged(t, rw, ok)
	if r.asker.Calls() != 0 {
		t.Fatalf("classifier calls = %d, want none for a pinned and a paused turn", r.asker.Calls())
	}
}

func TestRestoreDropsInvalidIntent(t *testing.T) {
	bad := config.Tier("galactic")
	in := Intent{Preset: "frugal", Threads: map[string]ThreadIntent{"a": {Pin: &bad}, "": {Paused: true}}}

	h := newHarness(t, testConfig(), func(d *Deps) { d.Intent = in })

	if got := withoutRev(h.engine.Intent()); !reflect.DeepEqual(got, Intent{Preset: config.PresetBalanced}) {
		t.Fatalf("intent = %+v, want the config's preset and nothing else", got)
	}
}

func TestIntentIsSavedOnEveryChange(t *testing.T) {
	saved := &savedIntents{}
	h := newHarness(t, testConfig(), func(d *Deps) { d.SaveIntent = saved.save })
	h.asker.script(answer(tier("low", 0.97)), answer(tier("low", 0.97)))

	h.apply(Command{Op: OpPin, Session: "a", Tier: config.TierHigh})
	if p := saved.last(t).Threads["a"].Pin; p == nil || *p != config.TierHigh {
		t.Fatalf("after pin: %+v", saved.last(t))
	}
	h.apply(Command{Op: OpSnapshot})
	if saved.count() != 1 {
		t.Fatalf("saves after a snapshot command = %d, want 1", saved.count())
	}

	h.firstStep("a", "a1")
	if in := saved.last(t); saved.count() != 2 || len(in.Threads) != 0 {
		t.Fatalf("after the pin was spent: %d saves, last %+v", saved.count(), in)
	}
	h.firstStep("a", "a2")
	if saved.count() != 2 {
		t.Fatalf("a plain step saved the intent (%d saves)", saved.count())
	}

	h.engine.SettingsUpdated("a", config.Route{Model: "gpt-6-sol", Effort: config.EffortLow})
	if !saved.last(t).Threads["a"].Manual {
		t.Fatalf("after a /model pick: %+v", saved.last(t))
	}
	h.apply(Command{Op: OpPreset, Preset: config.PresetThrifty})
	if in := saved.last(t); in.Preset != config.PresetThrifty {
		t.Fatalf("after preset: %+v", in)
	}

	for i := 1; i < len(saved.all); i++ {
		if saved.all[i].Rev <= saved.all[i-1].Rev {
			t.Fatalf("save %d has rev %d after %d, want increasing", i, saved.all[i].Rev, saved.all[i-1].Rev)
		}
	}
}

func TestLostIntentPausesTheHomeUntilResume(t *testing.T) {
	saved := &savedIntents{}
	h := newHarness(t, testConfig(), func(d *Deps) {
		d.IntentLost = true
		d.SaveIntent = saved.save
	})
	h.asker.script(answer(tier("low", 0.97)))

	rw, ok := h.firstStep("a", "a1")

	wantUnchanged(t, rw, ok)
	if s := h.session("a"); s.Attention != AttentionStateLost || s.Mode != ModePaused {
		t.Fatalf("session = %+v, want state-lost and paused", s)
	}
	s := h.engine.SnapshotFor("a")
	if !s.StateLost || len(s.Worth) == 0 || s.Worth[0].Kind != FindingState {
		t.Fatalf("snapshot state lost %v, findings %+v", s.StateLost, s.Worth)
	}
	if h.asker.Calls() != 0 {
		t.Fatalf("classifier calls = %d while the intent is lost", h.asker.Calls())
	}

	h.apply(Command{Op: OpResume, Scope: ScopeHome})

	if s := h.session("a"); s.Attention != "" || s.Mode != ModeOn {
		t.Fatalf("after resume: %+v", s)
	}
	if h.engine.SnapshotFor("a").StateLost || saved.last(t).Paused {
		t.Fatalf("after resume: state lost kept or saved paused: %+v", saved.last(t))
	}
	rw, ok = h.firstStep("a", "a2")
	wantRoute(t, rw, ok, low)
}

func TestSeededThreadHoldsItsRouteUntilAMeasuredReply(t *testing.T) {
	h := newHarness(t, testConfig())
	h.asker.script(answer(tier("high", 0.99)))
	h.engine.Resumed("a", low)
	items := []responses.Item{userMsg(secretPrompt)}
	m := meta("a", "a1")

	rw, ok := h.decide(req(low, items...), m)

	wantUnchanged(t, rw, ok)
	if s := h.session("a"); s.Attention != AttentionUnknown || s.Route != low || s.Tier != config.TierLow {
		t.Fatalf("seeded session = %+v", s)
	}
	items = append(items, call("c1", "ls"), result("c1", 0, ""))
	rw, ok = h.decide(req(low, items...), m)
	wantUnchanged(t, rw, ok)

	h.reply(m, low.Model, 1000, 0)
	if s := h.session("a"); s.Attention != "" {
		t.Fatalf("attention after the first reply = %q", s.Attention)
	}
	items = append(items, call("c2", "ls"), result("c2", 0, ""))
	rw, ok = h.decide(req(low, items...), m)
	wantUnchanged(t, rw, ok)
	if h.asker.Calls() != 0 {
		t.Fatalf("classifier calls = %d during the held turn", h.asker.Calls())
	}

	items = append(items, userMsg("now the big refactor"))
	rw, ok = h.decide(req(low, items...), meta("a", "a2"))
	wantRoute(t, rw, ok, high)
	if h.asker.Calls() != 1 {
		t.Fatalf("classifier calls = %d on the next turn, want 1", h.asker.Calls())
	}
}

func TestResumedOnAKnownThread(t *testing.T) {
	picked := config.Route{Model: "gpt-6-sol", Effort: config.EffortLow}
	tests := []struct {
		name       string
		manual     bool // a /model pick reported before the resume
		reported   config.Route
		wantResync bool
		wantMode   Mode
	}{
		{"the router's write", false, low, true, ModeOn},
		{"a restarted daemon's native route", false, medium, true, ModeOn},
		{"a pick made while not subscribed", false, picked, false, ModeManual},
		{"a manual thread", true, high, false, ModeManual},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			saved := &savedIntents{}
			h := newHarness(t, testConfig(), func(d *Deps) { d.SaveIntent = saved.save })
			h.asker.script(answer(tier("low", 0.97)))
			rw, ok := h.firstStep("a", "a1")
			wantRoute(t, rw, ok, low)
			if tt.manual {
				h.engine.SettingsUpdated("a", picked)
			}

			resync := h.engine.Resumed("a", tt.reported)

			if s := h.session("a"); resync != tt.wantResync || s.Mode != tt.wantMode {
				t.Fatalf("resync %v, mode %s; want %v, %s", resync, s.Mode, tt.wantResync, tt.wantMode)
			}
			if manual := tt.wantMode == ModeManual; saved.count() > 0 != manual || manual && !saved.last(t).Threads["a"].Manual {
				t.Fatalf("saved intents %+v, want the manual hold saved: %v", saved.all, manual)
			}
		})
	}
}

func TestHoldUnseededHoldsAThreadAlreadyInConversation(t *testing.T) {
	conversation := []responses.Item{userMsg(secretPrompt), {Type: "message", Role: "assistant", Text: "done"}, userMsg("next")}
	tests := []struct {
		name     string
		hold     bool
		items    []responses.Item
		wantHeld bool
	}{
		{"brain, mid-conversation", true, conversation, true},
		{"brain, a thread's first turn", true, []responses.Item{userMsg(secretPrompt)}, false},
		{"launcher, mid-conversation", false, conversation, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t, testConfig(), func(d *Deps) { d.HoldUnseeded = tt.hold })
			h.asker.script(answer(tier("low", 0.97)))

			rw, ok := h.decide(req(medium, tt.items...), meta("a", "a1"))

			if !tt.wantHeld {
				wantRoute(t, rw, ok, low)
				return
			}
			wantUnchanged(t, rw, ok)
			if s := h.session("a"); s.Attention != AttentionUnknown || h.asker.Calls() != 0 {
				t.Fatalf("attention %q, classifier calls %d; want held without a call", s.Attention, h.asker.Calls())
			}
			h.reply(meta("a", "a1"), medium.Model, 1000, 0)
			h.decide(req(medium, append(tt.items, userMsg("more"))...), meta("a", "a2"))
			if s := h.session("a"); s.Attention != "" || h.asker.Calls() != 1 {
				t.Fatalf("after a measured reply: attention %q, classifier calls %d; want routed", s.Attention, h.asker.Calls())
			}
		})
	}
}

func TestSeedWithTheModelDefaultEffort(t *testing.T) {
	h := newHarness(t, testConfig())
	def := config.Route{Model: medium.Model}

	h.engine.Resumed("a", def)
	h.engine.Resumed("b", config.Route{})
	rw, ok := h.firstStep("a", "a1")

	wantUnchanged(t, rw, ok)
	if s := h.session("a"); s.Attention != AttentionUnknown || s.Route != def {
		t.Fatalf("session = %+v, want held on %+v", s, def)
	}
	h.engine.mu.Lock()
	defer h.engine.mu.Unlock()
	if h.engine.lookup("b") != nil {
		t.Fatal("a seed without a model made a thread")
	}
}

func TestPinEndsASeededHold(t *testing.T) {
	h := newHarness(t, testConfig())
	h.engine.Resumed("a", medium)
	h.apply(Command{Op: OpPin, Session: "a", Tier: config.TierHigh})

	rw, ok := h.firstStep("a", "a1")

	wantRoute(t, rw, ok, high)
	if s := h.session("a"); s.Attention != "" {
		t.Fatalf("attention = %q after a pinned turn", s.Attention)
	}
}
