package route

import (
	"fmt"
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

	if got := withoutRev(h.engine.Intent()); !reflect.DeepEqual(got, Intent{}) {
		t.Fatalf("intent = %+v, want none", got)
	}
	if s := h.engine.Snapshot(); s.Preset != config.PresetBalanced {
		t.Fatalf("preset = %s, want the config's", s.Preset)
	}
}

func TestRestartKeepsOnlyAPresetACommandSet(t *testing.T) {
	cases := []struct {
		name      string
		command   config.Preset // "" sends no preset command
		want      config.Preset
		wantSaved config.Preset
	}{
		{"the config's preset follows a config edit", "", config.PresetQuality, ""},
		{"a commanded preset survives", config.PresetThrifty, config.PresetThrifty, config.PresetThrifty},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			saved := &savedIntents{}
			h := newHarness(t, testConfig(), func(d *Deps) { d.SaveIntent = saved.save })
			if tc.command != "" {
				h.apply(Command{Op: OpPreset, Preset: tc.command, Scope: ScopeHome})
			}
			h.apply(Command{Op: OpPause, Session: "a"})

			edited := testConfig()
			edited.Preset = config.PresetQuality
			again := &savedIntents{}
			r := newHarness(t, edited, func(d *Deps) { d.Intent, d.SaveIntent = saved.last(t), again.save })
			r.apply(Command{Op: OpResume, Session: "a"})

			if s := r.engine.Snapshot(); s.Preset != tc.want {
				t.Fatalf("preset after restart = %s, want %s", s.Preset, tc.want)
			}
			if p := again.last(t).Preset; p != tc.wantSaved {
				t.Fatalf("saved preset after restart = %q, want %q", p, tc.wantSaved)
			}
		})
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
		{"the native route at the model's default effort", false, config.Route{Model: medium.Model}, true, ModeOn},
		{"the router's write at the model's default effort", false, config.Route{Model: low.Model}, true, ModeOn},
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
		{"brain, a first turn with Codex's context items", true, []responses.Item{
			{Type: "additional_tools", Role: "developer"},
			{Type: responses.TypeMessage, Role: "developer", Text: "instructions"},
			{Type: responses.TypeMessage, Role: "user", Text: "<environment_context>"},
			userMsg(secretPrompt),
		}, false},
		{"brain, after a tool call", true, []responses.Item{
			userMsg(secretPrompt), {Type: responses.TypeFunctionCall, Name: "exec"}, userMsg("next"),
		}, true},
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

func TestSeedWithTheDefaultEffortThenAnExplicitEffort(t *testing.T) {
	seed := config.Route{Model: medium.Model}
	cases := []struct {
		name   string
		report func(e *Engine, r config.Route)
		effort config.Effort
		manual bool
	}{
		{"settings at the model's default", settings, config.EffortMedium, false},
		{"settings at another effort", settings, config.EffortHigh, true},
		{"resume at the model's default", resume, config.EffortMedium, false},
		{"resume at another effort", resume, config.EffortHigh, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, testConfig(), func(d *Deps) { d.HoldUnseeded = true })
			h.asker.script(answer(tier("low", 0.97)))
			h.engine.Resumed("a", seed)
			picked := config.Route{Model: medium.Model, Effort: tc.effort}
			history := []responses.Item{userMsg(secretPrompt), {Type: responses.TypeMessage, Role: "assistant", Text: "done"}}

			tc.report(h.engine, picked)
			h.decide(req(picked, append(history, userMsg("next"))...), meta("a", "a1"))
			h.reply(meta("a", "a1"), medium.Model, 1000, 0)
			rw, ok := h.decide(req(picked, append(history, userMsg("next"), userMsg("more"))...), meta("a", "a2"))

			if manual := h.session("a").Mode == ModeManual; manual != tc.manual {
				t.Fatalf("manual = %v, want %v", manual, tc.manual)
			}
			if tc.manual {
				wantUnchanged(t, rw, ok)
				if h.asker.Calls() != 0 {
					t.Fatalf("classifier calls = %d, want none: the pick holds the thread", h.asker.Calls())
				}
			}
		})
	}
}

func settings(e *Engine, r config.Route) { e.SettingsUpdated("a", r) }

func resume(e *Engine, r config.Route) { e.Resumed("a", r) }

func TestBrainRestartMidTurnIsNotAPick(t *testing.T) {
	h := newHarness(t, testConfig(), func(d *Deps) { d.HoldUnseeded = true })
	// The router wrote medium for the next turn; the running turn started on high.
	h.engine.Resumed("a", medium)
	midTurn := []responses.Item{
		userMsg(secretPrompt), {Type: responses.TypeFunctionCall, Name: "exec", CallID: "c1"},
		{Type: responses.TypeFunctionCallOutput, CallID: "c1", Output: "ok"},
	}

	rw, ok := h.decide(req(high, midTurn...), meta("a", "a1"))

	wantRoute(t, rw, ok, medium)
	if s := h.session("a"); s.Mode == ModeManual {
		t.Fatalf("session = %+v, want not manual: the step carries its turn's settings", s)
	}
	picked := config.Route{Model: "gpt-6-sol", Effort: config.EffortLow}
	h.decide(req(picked, append(midTurn, userMsg("next"))...), meta("a", "a2"))
	if s := h.session("a"); s.Mode != ModeManual {
		t.Fatalf("mode = %s, want manual: the next turn shows a /model pick", s.Mode)
	}
}

func TestHoldEndedByARunningTurnsReplyLeavesThatTurnUnrouted(t *testing.T) {
	h := newHarness(t, testConfig(), func(d *Deps) { d.HoldUnseeded = true })
	h.asker.script(answer(tier("low", 0.97)), answer(tier("low", 0.97)))
	h.engine.Resumed("a", medium)
	midTurn := []responses.Item{
		userMsg(secretPrompt), {Type: responses.TypeFunctionCall, Name: "exec", CallID: "c1"},
		{Type: responses.TypeFunctionCallOutput, CallID: "c1", Output: "ok"},
	}

	// The restarted brain first sees the reply to a step the old brain decided.
	h.reply(meta("a", "a1"), medium.Model, 1000, 0)
	rw, ok := h.decide(req(medium, midTurn...), meta("a", "a1"))

	wantUnchanged(t, rw, ok)
	if h.asker.Calls() != 0 {
		t.Fatalf("classifier calls = %d mid-turn, want the rest of the turn unrouted", h.asker.Calls())
	}
	rw, ok = h.decide(req(medium, append(midTurn, userMsg("next"))...), meta("a", "a2"))
	if h.asker.Calls() != 1 {
		t.Fatalf("next turn: classifier calls = %d, want routed (rewrite %+v %v)", h.asker.Calls(), rw, ok)
	}
}

func TestHeldTurnKeepsTheSeededRouteMidTurn(t *testing.T) {
	h := newHarness(t, testConfig(), func(d *Deps) { d.HoldUnseeded = true })
	h.asker.script(answer(tier("high", 0.99)))
	// The old brain routed the running turn to medium; the turn started on high.
	h.engine.Resumed("a", medium)
	items := []responses.Item{
		userMsg(secretPrompt), {Type: responses.TypeFunctionCall, Name: "exec", CallID: "c1"},
		{Type: responses.TypeFunctionCallOutput, CallID: "c1", Output: "ok"},
	}

	rw, ok := h.decide(req(high, items...), meta("a", "a1"))
	wantRoute(t, rw, ok, medium)
	h.reply(meta("a", "a1"), medium.Model, 1000, 0)
	items = append(items, responses.Item{Type: responses.TypeFunctionCall, Name: "exec", CallID: "c2"},
		responses.Item{Type: responses.TypeFunctionCallOutput, CallID: "c2", Output: "ok"})
	rw, ok = h.decide(req(high, items...), meta("a", "a1"))
	wantRoute(t, rw, ok, medium)
	rw, ok = h.decide(req(medium, items...), meta("a", "a1"))
	wantUnchanged(t, rw, ok)
	if h.asker.Calls() != 0 {
		t.Fatalf("classifier calls = %d in the held turn", h.asker.Calls())
	}

	rw, ok = h.decide(req(medium, append(items, userMsg("next"))...), meta("a", "a2"))
	if h.asker.Calls() != 1 {
		t.Fatalf("next turn: classifier calls = %d, want routed (rewrite %+v %v)", h.asker.Calls(), rw, ok)
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

func TestEvictionKeepsThreadIntent(t *testing.T) {
	cases := []struct {
		name  string
		setup func(h *harness)
		want  ThreadIntent
	}{
		{"pause", func(h *harness) { h.apply(Command{Op: OpPause, Session: "a"}) }, ThreadIntent{Paused: true}},
		{"pin", func(h *harness) { h.apply(Command{Op: OpPin, Session: "a", Tier: config.TierHigh}) }, ThreadIntent{Pin: tierPtr(config.TierHigh)}},
		{"manual hold", func(h *harness) {
			h.asker.script(answer(tier("low", 0.97)))
			h.firstStep("a", "a1")
			h.engine.SettingsUpdated("a", config.Route{Model: "gpt-6-sol", Effort: config.EffortLow})
		}, ThreadIntent{Manual: true}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, testConfig())
			tc.setup(h)
			for i := range MaxThreads {
				h.asker.script(answer(tier("low", 0.97)))
				h.firstStep(fmt.Sprint("other", i), "turn")
			}

			if got := h.engine.Intent().Threads["a"]; !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("intent of a after %d other threads = %+v, want %+v", MaxThreads, got, tc.want)
			}
			if tc.name == "pause" {
				rw, ok := h.firstStep("a", "a2")
				wantUnchanged(t, rw, ok)
			}
		})
	}
}

func TestEvictionDropsTheOldestThreadWithoutIntent(t *testing.T) {
	h := newHarness(t, testConfig())
	h.apply(Command{Op: OpPause, Session: "a"})
	for i := range MaxThreads + 1 {
		h.asker.script(answer(tier("low", 0.97)))
		h.firstStep(fmt.Sprint("other", i), "turn")
	}

	if h.engine.SnapshotFor("other0").Last != nil {
		t.Error("the oldest thread without intent was kept")
	}
	if _, ok := h.engine.Intent().Threads["a"]; !ok {
		t.Error("a paused thread was evicted")
	}
}

func TestRestoreKeepsEveryIntentThread(t *testing.T) {
	in := Intent{Threads: map[string]ThreadIntent{}}
	for i := range MaxThreads + 10 {
		in.Threads[fmt.Sprint("t", i)] = ThreadIntent{Paused: true}
	}

	h := newHarness(t, testConfig(), func(d *Deps) { d.Intent = in })

	if got := len(h.engine.Intent().Threads); got != len(in.Threads) {
		t.Fatalf("restored %d intent threads, want %d", got, len(in.Threads))
	}
}

func TestLostIntentSurvivesASaveAndRestart(t *testing.T) {
	saved := &savedIntents{}
	h := newHarness(t, testConfig(), func(d *Deps) { d.IntentLost, d.SaveIntent = true, saved.save })
	h.apply(Command{Op: OpPause, Session: "a"})
	h.apply(Command{Op: OpPin, Session: "b", Tier: config.TierHigh})
	h.apply(Command{Op: OpPreset, Preset: config.PresetQuality, Scope: ScopeHome})
	h.engine.SettingsUpdated("c", config.Route{Model: "gpt-6-sol", Effort: config.EffortLow})

	if n := saved.count(); n != 0 {
		t.Fatalf("%d intents saved while the intent is lost, want none", n)
	}
	if !h.engine.SnapshotFor("a").StateLost {
		t.Fatal("state lost cleared without a home resume")
	}

	h.apply(Command{Op: OpResume, Scope: ScopeHome})

	got := saved.last(t)
	if got.Paused || got.Preset != config.PresetQuality || !got.Threads["a"].Paused {
		t.Fatalf("intent saved at the home resume = %+v, want the commands made while lost", got)
	}
}
