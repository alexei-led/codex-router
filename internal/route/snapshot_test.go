package route

import (
	"maps"
	"math"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/alexei-led/codex-router/internal/catalog"
	"github.com/alexei-led/codex-router/internal/classifier"
	"github.com/alexei-led/codex-router/internal/config"
	"github.com/alexei-led/codex-router/internal/policy"
	"github.com/alexei-led/codex-router/internal/responses"
)

func TestPricingKeepsSavingsAndExtraCostSeparately(t *testing.T) {
	prices := map[string]config.Price{
		"native": {Input: 2, CachedInput: .2, Output: 8},
		"cheap":  {Input: 1, CachedInput: .1, Output: 4},
		"costly": {Input: 3, CachedInput: .3, Output: 12},
	}
	st := newTally()
	u := responses.Usage{InputTokens: 1000, CachedInputTokens: 500, OutputTokens: 100}
	for _, model := range []string{"cheap", "costly", "native"} {
		st.reply(Decision{Route: config.Route{Model: model}}, u, time.Now(), prices, "native")
	}
	if math.Abs(st.SavingsUSD-.00095) > 1e-10 || math.Abs(st.ExtraUSD-.00095) > 1e-10 {
		t.Fatalf("savings = %g, extra = %g; want .00095 each", st.SavingsUSD, st.ExtraUSD)
	}
	if math.Abs(st.BaselineUSD-st.RoutedUSD) > 1e-10 {
		t.Fatalf("equal savings and extra should net to zero: %+v", st.Stats)
	}
}

func TestPricingUsesServedModel(t *testing.T) {
	for _, served := range []string{"backend-model", low.Model + "-variant"} {
		t.Run(served, func(t *testing.T) {
			cfg := testConfig()
			cfg.Prices = map[string]config.Price{
				medium.Model: {Input: 2, CachedInput: .1, Output: 1}, low.Model: {Input: 1, CachedInput: .1, Output: 1}, served: {Input: 3, CachedInput: .1, Output: 1},
			}
			h := newHarness(t, cfg)
			h.asker.script(answer(tier("low", .97)))
			m := meta("th", "t1")
			h.decide(req(medium, userMsg(secretPrompt)), m)
			h.reply(m, served, 1000, 0)
			st := h.engine.Snapshot().Stats
			if math.Abs(st.RoutedUSD-.0031) > 1e-10 || math.Abs(st.BaselineUSD-.0021) > 1e-10 {
				t.Fatalf("costs = routed %g, native %g; want .0031, .0021", st.RoutedUSD, st.BaselineUSD)
			}
		})
	}
}

// Snapshot returns the most recently active thread's snapshot, for tests
// that drive one thread.
func (e *Engine) Snapshot() Snapshot {
	s := func() Snapshot {
		e.mu.Lock()
		defer e.mu.Unlock()
		return e.snapshot(e.latest(), e.now())
	}()
	return e.withHealth(s)
}

func scored(choice config.Tier, probs map[config.Tier]float64) classifier.Advice {
	a := classifier.Advice{Choice: classifier.Choice(choice), Probabilities: map[classifier.Choice]float64{}}
	for t, p := range probs {
		a.Probabilities[classifier.Choice(t)] = p
	}
	return a
}

func TestSnapshotsAreSequenced(t *testing.T) {
	h := newHarness(t, testConfig())
	first := h.engine.Snapshot()
	if err := h.engine.Apply(Command{Op: OpPause}); err != nil {
		t.Fatal(err)
	}
	if second := h.engine.Snapshot(); second.Seq <= first.Seq {
		t.Fatalf("seq %d then %d, want increasing", first.Seq, second.Seq)
	}
}

func TestDecisionCarriesTheClassifierReading(t *testing.T) {
	advice := scored("low", map[config.Tier]float64{"high": 0.02, "medium": 0.05, "low": 0.9, "micro": 0.03})
	advice.Probabilities[classifier.ChoiceUncertain] = 0.01
	advice.Activity = &classifier.ActivityAdvice{Choice: config.ActivityDebug, Probabilities: map[config.Activity]float64{
		config.ActivityDebug: 0.72, config.ActivityCode: 0.15, config.ActivityExplore: 0.06, config.ActivityPlan: 0.06, config.ActivityDocs: 0,
	}}
	want := &Reading{
		Tiers:    map[config.Tier]float64{"high": 0.02, "medium": 0.05, "low": 0.9, "micro": 0.03},
		Activity: config.ActivityDebug, ActivityP: 0.72,
		Alternates: []Share{{config.ActivityCode, 0.15}, {config.ActivityExplore, 0.06}},
	}
	h := newHarness(t, testConfig())
	h.asker.script(answer(advice))
	items := []responses.Item{userMsg(secretPrompt)}
	m := meta("th", "t1")

	h.decide(req(medium, items...), m)
	if got := h.last().Reading; !reflect.DeepEqual(got, want) {
		t.Fatalf("first step reading = %+v, want %+v", got, want)
	}
	items = append(items, call("c1", "ls"), result("c1", 0, "a.go"))
	h.decide(req(medium, items...), m)
	if got := h.last().Reading; !reflect.DeepEqual(got, want) {
		t.Fatalf("later step reading = %+v, want the turn's %+v", got, want)
	}
	if got := h.engine.Snapshot().Last.Reading; !reflect.DeepEqual(got, want) {
		t.Fatalf("snapshot reading = %+v", got)
	}
}

func TestReadingWithoutAnAnswer(t *testing.T) {
	t.Run("pinned turn", func(t *testing.T) {
		h := newHarness(t, testConfig())
		if err := h.engine.Apply(Command{Op: OpPin, Session: "th", Tier: config.TierHigh}); err != nil {
			t.Fatal(err)
		}
		h.decide(req(medium, userMsg(secretPrompt)), meta("th", "t1"))
		if d := h.last(); d.Reason != policy.ReasonPinned || d.Reading != nil {
			t.Fatalf("decision = %+v", d)
		}
	})
	t.Run("answer without activity", func(t *testing.T) {
		h := newHarness(t, testConfig())
		h.asker.script(answer(tier("low", 0.97)))
		h.decide(req(medium, userMsg(secretPrompt)), meta("th", "t1"))
		want := &Reading{Tiers: map[config.Tier]float64{"low": 0.97}}
		if got := h.last().Reading; !reflect.DeepEqual(got, want) {
			t.Fatalf("reading = %+v, want %+v", got, want)
		}
	})
}

func TestStatsKeepTheLastReplyCache(t *testing.T) {
	h := newHarness(t, testConfig())
	h.asker.script(answer(tier("low", 0.97)))
	m := meta("th", "t1")
	items := []responses.Item{userMsg(secretPrompt)}
	h.decide(req(medium, items...), m)
	h.reply(m, "gpt-6-luna", 1000, 0)
	items = append(items, call("c1", "ls"), result("c1", 0, ""))
	h.decide(req(medium, items...), m)
	h.reply(m, "gpt-6-luna", 2000, 1900)

	st := h.engine.Snapshot().Stats
	if st.LastInputTokens != 2000 || st.LastCachedTokens != 1900 || st.CacheReusePct != 63 {
		t.Fatalf("stats = %+v", st)
	}
}

func TestClassifierMedianLatency(t *testing.T) {
	tests := []struct {
		name      string
		latencies []time.Duration
		want      time.Duration
	}{
		{"none", nil, 0},
		{"odd count", []time.Duration{300, 100, 500}, 300},
		{"even count", []time.Duration{300, 100, 500, 200}, 250},
		{"zero latencies are not answers timed", []time.Duration{0, 400, 0}, 400},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t, testConfig())
			items := []responses.Item{}
			for i, l := range tt.latencies {
				a := tier("medium", 0.9)
				a.Latency = l * time.Millisecond
				h.asker.script(answer(a))
				items = append(items, userMsg(secretPrompt))
				h.decide(req(medium, items...), meta("th", string(rune('a'+i))))
			}
			if got := h.engine.Snapshot().ClassifierMedian; got != tt.want*time.Millisecond {
				t.Fatalf("median = %v, want %v", got, tt.want*time.Millisecond)
			}
		})
	}
}

func TestReviewLatencyCountsTowardTheMedian(t *testing.T) {
	cfg := testConfig()
	cfg.Policy.ReviewEveryRequests = 1
	h := newHarness(t, cfg)
	first, review := tier("medium", 0.9), tier("medium", 0.9)
	first.Latency, review.Latency = 100*time.Millisecond, 300*time.Millisecond
	h.asker.script(answer(first), answer(review))
	m := meta("th", "t1")
	items := []responses.Item{userMsg(secretPrompt)}
	h.decide(req(medium, items...), m)
	items = append(items, call("c1", "ls"), result("c1", 0, ""))
	h.decide(req(medium, items...), m)
	h.engine.reviews.Wait()

	if got := h.engine.Snapshot().ClassifierMedian; got != 200*time.Millisecond {
		t.Fatalf("median = %v, want 200ms", got)
	}
}

func TestContextGuardIsCountedAndWorthALook(t *testing.T) {
	h := newHarness(t, testConfig())
	h.asker.script(answer(tier("low", 0.99)))
	r := req(medium, userMsg(secretPrompt))
	r.Bytes = 4 * 250_000

	h.decide(r, meta("th", "t1"))

	s := h.engine.Snapshot()
	if s.Stats.FitHeld != 1 || !slices.ContainsFunc(s.Worth, func(f Finding) bool { return f.Kind == FindingContext }) {
		t.Fatalf("fit held = %d, worth = %+v", s.Stats.FitHeld, s.Worth)
	}
}

func TestSnapshotDistinguishesUnknownAndIncompatibleModels(t *testing.T) {
	t.Run("empty catalog", func(t *testing.T) {
		h := newHarness(t, testConfig())
		h.cat.Set(catalog.Catalog{})
		s := h.engine.Snapshot()
		if len(s.Blocked) != 0 {
			t.Fatalf("blocked = %v with empty catalog; unknown models are not known incompatible", s.Blocked)
		}
		if s.CatalogAvailable || s.CurrentModelKnown || !slices.Contains(s.CompatibilityUnknown, medium.Model) {
			t.Fatalf("catalog state available=%v unknown=%v", s.CatalogAvailable, s.CompatibilityUnknown)
		}
	})

	t.Run("missing current model fails closed but is not blocked", func(t *testing.T) {
		h := newHarness(t, testConfig())
		req := req(config.Route{Model: "missing-current", Effort: config.EffortMedium}, userMsg(secretPrompt))
		if _, ok := h.decide(req, meta("th", "t1")); ok {
			t.Fatal("unknown current model was routed")
		}
		s := h.engine.SnapshotFor("th")
		if len(s.Blocked) != 0 {
			t.Fatalf("blocked = %v for missing current model, want compatibility unknown", s.Blocked)
		}
		if !s.CatalogAvailable || s.CurrentModelKnown || !slices.Contains(s.CompatibilityUnknown, "missing-current") {
			t.Fatalf("unknown current model not reported: %+v", s)
		}
	})

	t.Run("missing target model is unknown", func(t *testing.T) {
		cfg := testConfig()
		cfg.Routes[config.TierHigh] = config.Route{Model: "missing-target", Effort: config.EffortHigh}
		h := newHarness(t, cfg)
		s := h.engine.Snapshot()
		if slices.Contains(s.Blocked, "missing-target") {
			t.Fatalf("missing target listed as blocked: %v", s.Blocked)
		}
		if !slices.Contains(s.CompatibilityUnknown, "missing-target") {
			t.Fatalf("missing target not reported as unknown: %v", s.CompatibilityUnknown)
		}
	})

	t.Run("known equal and unequal groups", func(t *testing.T) {
		cfg := testConfig()
		cfg.Routes[config.TierHigh] = config.Route{Model: "gpt-5.5", Effort: config.EffortHigh}
		h := newHarness(t, cfg)
		s := h.engine.Snapshot()
		if !s.CurrentModelKnown || !slices.Equal(s.Blocked, []string{"gpt-5.5"}) {
			t.Fatalf("blocked = %v, want only known unequal gpt-5.5", s.Blocked)
		}
		if len(s.CompatibilityUnknown) != 0 {
			t.Fatalf("unknown = %v, want no unknown models", s.CompatibilityUnknown)
		}
	})

	t.Run("catalog becoming available", func(t *testing.T) {
		h := newHarness(t, testConfig())
		h.cat.Set(catalog.Catalog{})
		if s := h.engine.Snapshot(); len(s.Blocked) != 0 {
			t.Fatalf("empty catalog blocked = %v", s.Blocked)
		}
		cat, err := catalog.Parse([]byte(testCatalog))
		if err != nil {
			t.Fatal(err)
		}
		h.cat.Set(cat)
		if s := h.engine.Snapshot(); len(s.Blocked) != 0 || len(s.CompatibilityUnknown) != 0 || !s.CatalogAvailable {
			t.Fatalf("available catalog snapshot = blocked %v unknown %v available %v", s.Blocked, s.CompatibilityUnknown, s.CatalogAvailable)
		}
	})
}

func TestWorth(t *testing.T) {
	now := time.Date(2026, 10, 13, 18, 0, 0, 0, time.UTC)
	quiet := Snapshot{
		At: now, Mode: ModeOn, ClassifierLabel: "Jev", Baseline: medium, Footer: medium,
		Stats: Stats{Replies: 40, Switches: 3, CacheReusePct: 92, UncachedSwitchTokens: 90_000},
	}
	tests := []struct {
		name   string
		mutate func(*Snapshot)
		want   []Finding
	}{
		{"nothing to look at", func(*Snapshot) {}, nil},
		{"classifier paused", func(s *Snapshot) {
			s.Classifier = classifier.Health{Failures: 3, PausedUntil: now.Add(time.Minute)}
		}, []Finding{{FindingClassifier, "Jev paused until 18:01:00: turns go unrouted"}}},
		{"classifier pause over", func(s *Snapshot) {
			s.Classifier = classifier.Health{PausedUntil: now.Add(-time.Minute)}
		}, nil},
		{"classifier failing", func(s *Snapshot) {
			s.Classifier = classifier.Health{Failures: 1}
		}, []Finding{{FindingClassifier, "Jev failed 1 call in a row: a failed turn goes unrouted"}}},
		{"manual thread", func(s *Snapshot) {
			s.Mode, s.Footer = ModeManual, low
		}, []Finding{{FindingManual, "footer picked with /model: routing holds gpt-6-luna · medium until resume"}}},
		{"context guard", func(s *Snapshot) { s.Stats.FitHeld = 2 },
			[]Finding{{FindingContext, "context guard kept 2 steps on a larger window than the policy chose"}}},
		{"blocked routes", func(s *Snapshot) { s.Blocked = []string{"gpt-5.5"} },
			[]Finding{{FindingBlocked, "routes to gpt-5.5 never run: outside gpt-6-sol's compatibility group"}}},
		{"low cache reuse after switches", func(s *Snapshot) { s.Stats.CacheReusePct = 41 },
			[]Finding{{FindingCache, "cache reuse 41% after 3 switches; switches wrote 90k uncached tokens"}}},
		{"low cache reuse on few replies", func(s *Snapshot) { s.Stats.CacheReusePct, s.Stats.Replies = 41, 19 }, nil},
		{"low cache reuse without switches", func(s *Snapshot) { s.Stats.CacheReusePct, s.Stats.Switches = 41, 0 }, nil},
		{"most urgent first", func(s *Snapshot) {
			s.Stats.CacheReusePct, s.Mode, s.Classifier.Failures = 41, ModeManual, 2
		}, []Finding{
			{FindingClassifier, "Jev failed 2 calls in a row: a failed turn goes unrouted"},
			{FindingManual, "footer picked with /model: routing holds gpt-6-sol · medium until resume"},
			{FindingCache, "cache reuse 41% after 3 switches; switches wrote 90k uncached tokens"},
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := quiet
			tt.mutate(&s)
			if got := worth(s); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("worth = %+v\nwant  %+v", got, tt.want)
			}
		})
	}
}

// TestSupportParts pins the evidence line's fragments and the string they
// spell: Support keeps its wording, and no fragment is split on the separator,
// which a classifier's own label may hold.
func TestSupportParts(t *testing.T) {
	cfg := testConfig()
	advice := tier("high", 0.7)
	advice.Activity = &classifier.ActivityAdvice{Choice: config.ActivityDebug, Probabilities: map[config.Activity]float64{config.ActivityDebug: 0.72}}
	bar := &policy.Estimate{Mass: 0.88, Threshold: 0.82}
	spaced := cfg
	spaced.Classifiers = maps.Clone(cfg.Classifiers)
	labeled := spaced.Classifiers[spaced.Classifier]
	labeled.Label = "Jev · fast"
	spaced.Classifiers[spaced.Classifier] = labeled
	tests := []struct {
		name   string
		cfg    config.Config
		d      Decision
		advice *classifier.Advice
		tax    int
		want   []string
		line   string
	}{
		{"upgrade weighs the mass above", cfg, Decision{Reason: policy.ReasonUpgrade, Estimate: bar}, &advice, 41_000,
			[]string{"Jev gave stronger tiers 88% (needs 82%)", "switch ≈ 41k uncached tokens"},
			"Jev gave stronger tiers 88% (needs 82%) · switch ≈ 41k uncached tokens"},
		{"pending downgrade weighs the mass below", cfg, Decision{Reason: policy.ReasonDowngradePending, Estimate: bar}, &advice, 0,
			[]string{"Jev gave cheaper tiers 88% (needs 82%)"}, "Jev gave cheaper tiers 88% (needs 82%)"},
		{"activity move weighs the activity", cfg, Decision{Reason: ReasonActivityUp, Activity: config.ActivityDebug, Estimate: bar}, &advice, 0,
			[]string{"Jev gave debug 88% (needs 82%)"}, "Jev gave debug 88% (needs 82%)"},
		{"no bar names the answer", cfg, Decision{Reason: policy.ReasonSameTier}, &advice, 0,
			[]string{"Jev gave high 70%"}, "Jev gave high 70%"},
		{"escalation needs no answer", cfg, Decision{Reason: policy.ReasonEscalation}, nil, 41_000,
			[]string{"no classifier needed", "switch ≈ 41k uncached tokens"},
			"no classifier needed · switch ≈ 41k uncached tokens"},
		{"a label holding the separator stays one fragment", spaced, Decision{Reason: policy.ReasonSameTier}, &advice, 0,
			[]string{"Jev · fast gave high 70%"}, "Jev · fast gave high 70%"},
		{"no evidence, no fragments", cfg, Decision{Reason: ReasonContinue}, nil, 0, nil, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := supportParts(tt.d, tt.advice, tt.cfg, tt.tax)
			if !slices.Equal(got, tt.want) {
				t.Fatalf("supportParts = %q\nwant %q", got, tt.want)
			}
			if line := supportLine(got); line != tt.line {
				t.Fatalf("supportLine = %q, want %q", line, tt.line)
			}
		})
	}
}
