package route

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/alexei-led/codex-router/internal/classifier"
	"github.com/alexei-led/codex-router/internal/config"
	"github.com/alexei-led/codex-router/internal/policy"
	"github.com/alexei-led/codex-router/internal/responses"
)

var (
	solMedium  = config.Route{Model: sol, Effort: config.EffortMedium}
	solHigh    = config.Route{Model: sol, Effort: config.EffortHigh}
	lunaHigh   = config.Route{Model: luna, Effort: config.EffortHigh}
	mediumTier = classifier.Advice{Choice: "medium", Probabilities: map[classifier.Choice]float64{"medium": 0.97, "high": 0.03}}
)

// modeHarness is the shipped routes with no background reviews, so a test
// script decides every step.
func modeHarness(t *testing.T, opts ...func(*Deps)) *harness {
	cfg := config.Default()
	cfg.Policy.ReviewEveryRequests = 0
	return newHarness(t, cfg, opts...)
}

// TestCostModeWaitsForTheGatesOnTheSameTask is the brief's regression: a
// switch to cost during an unverified medium continuation, before the dwell
// ends, keeps Sol/medium while the advice stays medium, and takes Luna/high
// only once the dwell, a passed check and the bars allow it, in one switch.
func TestCostModeWaitsForTheGatesOnTheSameTask(t *testing.T) {
	h := modeHarness(t)
	same := continuing(mediumTier, 0.9)
	h.asker.script(answer(mediumTier), answer(same), answer(same), answer(same))
	items := []responses.Item{userMsg(secretPrompt)}
	m := meta("th", "t1")
	rw, ok := h.decide(req(solMedium, items...), m)
	wantUnchanged(t, rw, ok)
	h.reply(m, sol, 50_000, 0)

	h.apply(Command{Op: OpMode, Session: "th", Mode: config.ModeCost})

	items = append(items, userMsg("keep going"))
	m = meta("th", "t2")
	rw, ok = h.decide(req(solMedium, items...), m)
	wantUnchanged(t, rw, ok)
	if d := h.last(); d.Reason != policy.ReasonDowngradeHeld || d.RoutingMode != config.ModeCost || d.RoutingModeSource != SourceSession {
		t.Fatalf("continuation right after the switch = %+v", d)
	}
	for i := range 4 {
		id := fmt.Sprint("c", i)
		items = append(items, call(id, "ls"), result(id, 0, ""))
		rw, ok = h.decide(req(solMedium, items...), m)
		wantUnchanged(t, rw, ok)
		h.reply(m, sol, 50_000, 45_000)
	}

	items = append(items, userMsg("and the next bit"))
	m = meta("th", "t3")
	rw, ok = h.decide(req(solMedium, items...), m)
	wantUnchanged(t, rw, ok)
	if d := h.last(); d.Reason != policy.ReasonDowngradeHeld {
		t.Fatalf("dwell passed without a passed check = %+v", d)
	}
	items = append(items, call("t", "go test ./..."), result("t", 0, "ok"))
	h.decide(req(solMedium, items...), m)
	h.reply(m, sol, 50_000, 45_000)

	items = append(items, userMsg("tidy up"))
	rw, ok = h.decide(req(solMedium, items...), meta("th", "t4"))
	wantRoute(t, rw, ok, lunaHigh)
	if d := h.last(); d.Reason != ReasonMode || d.Tier != config.TierMedium {
		t.Fatalf("after the gates = %+v", d)
	}
	if s := h.engine.SnapshotFor("th"); s.Stats.Switches != 1 {
		t.Fatalf("switches = %d, want exactly one", s.Stats.Switches)
	}
	for _, d := range h.sink.Decisions()[:len(h.sink.Decisions())-1] {
		if d.Route != solMedium {
			t.Fatalf("a decision before the gates moved: %+v", d)
		}
	}
}

func TestQualityModeTakesTheEffortLeverOnTheNextTurn(t *testing.T) {
	h := modeHarness(t)
	h.asker.script(answer(mediumTier), answer(mediumTier))
	m := meta("th", "t1")
	h.decide(req(solMedium, userMsg(secretPrompt)), m)
	h.reply(m, sol, 50_000, 0)
	h.apply(Command{Op: OpMode, Session: "th", Mode: config.ModeQuality})

	rw, ok := h.decide(req(solMedium, userMsg(secretPrompt), userMsg("next")), meta("th", "t2"))

	wantRoute(t, rw, ok, solHigh)
	if d := h.last(); d.Reason != ReasonMode || d.Estimate == nil || d.Estimate.TaxTokens != 0 {
		t.Fatalf("decision = %+v, want a mode move with no cache tax", d)
	}
}

func TestAModeChangeDoesNotMoveAStayingRoute(t *testing.T) {
	for _, tc := range []struct {
		name   string
		advice func() (classifier.Advice, error)
		reason policy.Reason
	}{
		{"uncertain answer", answer(classifier.Advice{Choice: "uncertain", Probabilities: map[classifier.Choice]float64{"uncertain": 1}}), policy.ReasonUncertain},
		{"classifier failure", fail(classifier.ErrTimeout), policy.ReasonError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := modeHarness(t)
			h.asker.script(answer(mediumTier), tc.advice)
			m := meta("th", "t1")
			h.decide(req(solMedium, userMsg(secretPrompt)), m)
			h.reply(m, sol, 50_000, 0)
			h.apply(Command{Op: OpMode, Session: "th", Mode: config.ModeCost})

			rw, ok := h.decide(req(solMedium, userMsg(secretPrompt), userMsg("next")), meta("th", "t2"))

			wantUnchanged(t, rw, ok)
			if d := h.last(); d.Reason != tc.reason {
				t.Fatalf("decision = %+v", d)
			}
		})
	}
	t.Run("later steps of the turn", func(t *testing.T) {
		h := modeHarness(t)
		h.asker.script(answer(mediumTier))
		m := meta("th", "t1")
		items := []responses.Item{userMsg(secretPrompt)}
		h.decide(req(solMedium, items...), m)
		h.apply(Command{Op: OpMode, Session: "th", Mode: config.ModeCost})
		items = append(items, call("c", "ls"), result("c", 0, ""))

		rw, ok := h.decide(req(solMedium, items...), m)

		wantUnchanged(t, rw, ok)
		if d := h.last(); d.Reason != ReasonContinue {
			t.Fatalf("decision = %+v", d)
		}
	})
}

func TestAPinBypassesTheMode(t *testing.T) {
	h := modeHarness(t)
	h.apply(Command{Op: OpMode, Session: "th", Mode: config.ModeCost})
	h.apply(Command{Op: OpPin, Session: "th", Tier: config.TierHigh})
	m := meta("th", "t1")
	items := []responses.Item{userMsg(secretPrompt)}

	rw, ok := h.decide(req(solMedium, items...), m)

	wantRoute(t, rw, ok, solHigh)
	items = append(items, call("c", "ls"), result("c", 0, ""))
	rw, ok = h.decide(req(solHigh, items...), m)
	wantUnchanged(t, rw, ok)
	if d := h.last(); d.RoutingMode != config.ModeCost {
		t.Fatalf("pinned decision mode = %s, want the thread's", d.RoutingMode)
	}
}

func TestModePrecedence(t *testing.T) {
	cases := []struct {
		name    string
		session config.RoutingMode
		project config.RoutingMode
		cfgMode config.RoutingMode
		want    config.RoutingMode
		source  ModeSource
	}{
		{"nothing set", "", "", "", config.ModeBalanced, SourceDefault},
		{"config", "", "", config.ModeCost, config.ModeCost, SourceConfig},
		{"project beats config", "", config.ModeQuality, config.ModeCost, config.ModeQuality, SourceProject},
		{"session beats project", config.ModeCost, config.ModeQuality, config.ModeBalanced, config.ModeCost, SourceSession},
		{"default clears the session", ModeDefault, config.ModeQuality, config.ModeCost, config.ModeQuality, SourceProject},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := config.Default()
			cfg.Mode = tc.cfgMode
			h := newHarness(t, cfg, func(d *Deps) {
				d.ProjectMode = func(string) (config.RoutingMode, error) { return tc.project, nil }
			})
			if tc.session != "" {
				h.apply(Command{Op: OpMode, Session: "th", Mode: config.ModeQuality})
				h.apply(Command{Op: OpMode, Session: "th", Mode: tc.session})
			}
			h.asker.script(answer(mediumTier))
			h.firstStep("th", "t1")

			if d := h.last(); d.RoutingMode != tc.want || d.RoutingModeSource != tc.source {
				t.Fatalf("decision mode %s (%s), want %s (%s)", d.RoutingMode, d.RoutingModeSource, tc.want, tc.source)
			}
			if s := h.engine.SnapshotFor("th"); s.RoutingMode != tc.want || s.Policy != cfg.Effective(tc.want) {
				t.Fatalf("snapshot mode %s, policy %+v", s.RoutingMode, s.Policy)
			}
		})
	}
}

func TestAnInvalidProjectFileIsIgnoredWithAFinding(t *testing.T) {
	cfg := config.Default()
	cfg.Mode = config.ModeCost
	h := newHarness(t, cfg, func(d *Deps) {
		d.ProjectMode = func(string) (config.RoutingMode, error) {
			return "", errors.New(`/repo/.codex/router.json: unknown field "routes"`)
		}
	})
	h.asker.script(answer(mediumTier))
	h.firstStep("th", "t1")

	s := h.engine.SnapshotFor("th")
	if s.RoutingMode != config.ModeCost || s.RoutingModeSource != SourceConfig {
		t.Fatalf("mode %s (%s), want the config's", s.RoutingMode, s.RoutingModeSource)
	}
	found := false
	for _, f := range s.Worth {
		found = found || f.Kind == FindingProject && strings.Contains(f.Text, `unknown field "routes"`)
	}
	if !found {
		t.Fatalf("findings = %+v, want the project file", s.Worth)
	}
	if n := strings.Count(h.logs.String(), "route project file ignored"); n != 1 {
		t.Fatalf("logged the ignored file %d times, want once", n)
	}
}

func TestSnapshotRoutesFollowTheMode(t *testing.T) {
	h := modeHarness(t)
	h.apply(Command{Op: OpMode, Session: "th", Mode: config.ModeCost})
	want := map[config.Tier]config.Route{
		config.TierMicro:  {Model: luna, Effort: config.EffortMedium},
		config.TierLow:    {Model: luna, Effort: config.EffortMedium},
		config.TierMedium: lunaHigh,
		config.TierHigh:   solMedium,
	}
	if s := h.engine.SnapshotFor("th"); fmt.Sprint(s.Routes) != fmt.Sprint(want) {
		t.Fatalf("routes = %v, want %v", s.Routes, want)
	}
}

func TestDecisionLogCarriesTheMode(t *testing.T) {
	h := modeHarness(t)
	h.apply(Command{Op: OpMode, Session: "th", Mode: config.ModeQuality})
	h.asker.script(answer(mediumTier))
	h.firstStep("th", "t1")

	for _, want := range []string{"mode=quality", "modeSource=session", "classified=medium", "model=gpt-6.1-sol", "effort=high"} {
		if !strings.Contains(h.logs.String(), want) {
			t.Errorf("route decision log lacks %q:\n%s", want, h.logs.String())
		}
	}
}
