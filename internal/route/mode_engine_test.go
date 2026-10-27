package route

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

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

// modeHarness exercises generic transforms on the legacy custom ladder.
// Terra shipped preferences have an independent exhaustive acceptance test.
func modeHarness(t *testing.T, opts ...func(*Deps)) *harness {
	cfg := legacyConfig()
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

// TestAModeChangeMidTurnWaitsForTheNextGate: tool calls that show a new
// activity mid-turn move the route with the mode the turn started under, so a
// mode switched mid-turn applies only through the gate.
func TestAModeChangeMidTurnWaitsForTheNextGate(t *testing.T) {
	h := modeHarness(t)
	h.asker.script(answer(mediumTier))
	m := meta("th", "t1")
	items := []responses.Item{userMsg(secretPrompt)}
	h.decide(req(solMedium, items...), m)
	h.reply(m, sol, 50_000, 0)
	h.apply(Command{Op: OpMode, Session: "th", Mode: config.ModeQuality})
	items = append(items, editGo("c1"), edited("c1"))

	rw, ok := h.decide(req(solMedium, items...), m)

	wantUnchanged(t, rw, ok)
	if d := h.last(); d.Route != solMedium {
		t.Fatalf("mid-turn step after the switch = %+v, want the turn's route", d)
	}
}

// TestAReturnToBalancedWaitsForTheGate: a route a mode aimed stays the
// incumbent after a switch back to balanced until a gate passes, as any mode
// move does.
func TestAReturnToBalancedWaitsForTheGate(t *testing.T) {
	t.Run("from cost on an uncertain answer", func(t *testing.T) {
		cfg := legacyConfig()
		cfg.Policy.ReviewEveryRequests = 0
		cfg.Mode = config.ModeCost
		h := newHarness(t, cfg)
		h.asker.script(answer(mediumTier), answer(classifier.Advice{Choice: "uncertain", Probabilities: map[classifier.Choice]float64{"uncertain": 1}}))
		m := meta("th", "t1")
		rw, ok := h.decide(req(lunaHigh, userMsg(secretPrompt)), m)
		wantUnchanged(t, rw, ok)
		h.reply(m, luna, 50_000, 0)
		h.apply(Command{Op: OpMode, Session: "th", Mode: config.ModeBalanced})

		rw, ok = h.decide(req(lunaHigh, userMsg(secretPrompt), userMsg("next")), meta("th", "t2"))

		wantUnchanged(t, rw, ok)
		if d := h.last(); d.Reason != policy.ReasonUncertain {
			t.Fatalf("decision = %+v, want Luna kept on an uncertain answer", d)
		}
	})
	t.Run("from quality on the same task", func(t *testing.T) {
		h := modeHarness(t)
		h.asker.script(answer(mediumTier), answer(mediumTier), answer(continuing(mediumTier, 0.9)))
		items := []responses.Item{userMsg(secretPrompt)}
		m := meta("th", "t1")
		h.decide(req(solMedium, items...), m)
		h.reply(m, sol, 50_000, 0)
		h.apply(Command{Op: OpMode, Session: "th", Mode: config.ModeQuality})
		items = append(items, userMsg("next"))
		m = meta("th", "t2")
		rw, ok := h.decide(req(solMedium, items...), m)
		wantRoute(t, rw, ok, solHigh)
		h.reply(m, sol, 50_000, 45_000)
		h.apply(Command{Op: OpMode, Session: "th", Mode: config.ModeBalanced})

		rw, ok = h.decide(req(solHigh, append(items, userMsg("keep going"))...), meta("th", "t3"))

		wantUnchanged(t, rw, ok)
		if d := h.last(); d.Reason != policy.ReasonDowngradeHeld {
			t.Fatalf("decision = %+v, want the move to Sol/medium held", d)
		}
	})
}

// TestQualityKeepsItsRouteWhileItsMoveDownWaits: while quality's own cheaper
// target waits for votes, the thread keeps its route rather than dropping to
// balanced's pair below that target.
func TestQualityKeepsItsRouteWhileItsMoveDownWaits(t *testing.T) {
	cfg := legacyConfig()
	cfg.Policy.ReviewEveryRequests = 0
	cfg.Mode = config.ModeQuality
	h := newHarness(t, cfg)
	solXHigh := config.Route{Model: sol, Effort: config.EffortXHigh}
	h.asker.script(answer(withActivity(mediumTier, config.ActivityDebug, 0.9)), answer(withActivity(mediumTier, "chat", 0.9)))
	m := meta("th", "t1")
	rw, ok := h.decide(req(solMedium, userMsg(secretPrompt)), m)
	wantRoute(t, rw, ok, solXHigh)
	h.reply(m, sol, 50_000, 0)

	rw, ok = h.decide(req(solXHigh, userMsg(secretPrompt), userMsg("something else")), meta("th", "t2"))

	wantUnchanged(t, rw, ok)
	if d := h.last(); d.Reason != ReasonModePending {
		t.Fatalf("decision = %+v, want quality's move to Sol/high pending", d)
	}
}

// TestCostGatesByInputPrice: cost picks a target by input price, so its gate
// orders the move the same way. A model with cheaper input but pricier output
// is a cost move down, held on the same task like any other.
func TestCostGatesByInputPrice(t *testing.T) {
	cfg := legacyConfig()
	cheapIn := config.Route{Model: "gpt-6-sol", Effort: config.EffortHigh}
	cfg.Routes[config.TierLow] = cheapIn
	cfg.Prices["gpt-6-sol"] = config.Price{Input: 1.25, CachedInput: 0.125, Output: 12}
	cfg.Policy.ReviewEveryRequests = 0
	h := newHarness(t, cfg)
	same := continuing(mediumTier, 0.9)
	h.asker.script(answer(mediumTier), answer(same))
	items := []responses.Item{userMsg(secretPrompt)}
	m := meta("th", "t1")
	h.decide(req(solMedium, items...), m)
	h.reply(m, sol, 50_000, 0)
	h.apply(Command{Op: OpMode, Session: "th", Mode: config.ModeCost})

	rw, ok := h.decide(req(solMedium, append(items, userMsg("keep going"))...), meta("th", "t2"))

	wantUnchanged(t, rw, ok)
	if d := h.last(); d.Reason != policy.ReasonDowngradeHeld {
		t.Fatalf("continuation after the switch = %+v, want the cost move held", d)
	}
}

// TestTheUpwardFitFixRunsBalanced: when no route of the mode fits the context
// and the baseline's configured pair is sent instead, the thread runs that
// pair, so a later turn keeps it until a gate passes rather than snapping to
// the mode's pair.
func TestTheUpwardFitFixRunsBalanced(t *testing.T) {
	cfg := legacyConfig()
	cfg.Baseline = config.TierHigh
	cfg.Routes[config.TierMedium] = config.Route{Model: "gpt-6-sol", Effort: config.EffortMedium}
	cfg.Prices["gpt-6-sol"] = config.Price{Input: 1.25, CachedInput: 0.125, Output: 10}
	cfg.Mode = config.ModeCost
	cfg.Policy.ReviewEveryRequests = 0
	h := newHarness(t, cfg)
	h.asker.script(answer(mediumTier), answer(classifier.Advice{Choice: "uncertain", Probabilities: map[classifier.Choice]float64{"uncertain": 1}}))
	big := req(lunaHigh, userMsg(secretPrompt))
	big.Bytes = 4 * 300_000

	rw, ok := h.decide(big, meta("th", "t1"))

	wantRoute(t, rw, ok, solHigh)
	rw, ok = h.decide(req(solHigh, userMsg(secretPrompt), userMsg("next")), meta("th", "t2"))
	wantUnchanged(t, rw, ok)
	if d := h.last(); d.Route != solHigh {
		t.Fatalf("after the fit fix = %+v, want the sent pair kept", d)
	}
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
			cfg := legacyConfig()
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

func TestHomeThreadsReadTheirOwnProjectFile(t *testing.T) {
	withFile, plain := t.TempDir(), t.TempDir()
	if err := os.MkdirAll(filepath.Join(withFile, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(withFile, ".codex"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(withFile, config.ProjectFile), []byte(`{"mode":"cost"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cwds := map[string]string{"a": filepath.Join(withFile, "pkg"), "b": plain}
	h := modeHarness(t, func(d *Deps) {
		d.Cwd = func(id string) string { return cwds[id] }
		d.ProjectMode = config.ProjectModes(d.Cwd)
	})
	h.asker.script(answer(mediumTier), answer(mediumTier))

	for _, tc := range []struct {
		thread string
		mode   config.RoutingMode
		source ModeSource
		route  config.Route
	}{
		{"a", config.ModeCost, SourceProject, lunaHigh},
		{"b", config.ModeBalanced, SourceDefault, solMedium},
	} {
		h.firstStep(tc.thread, tc.thread+"1")
		if d := h.last(); d.Route != tc.route {
			t.Errorf("thread %s routed to %v, want %v", tc.thread, d.Route, tc.route)
		}
		if s := h.engine.SnapshotFor(tc.thread); s.RoutingMode != tc.mode || s.RoutingModeSource != tc.source {
			t.Errorf("thread %s: mode %s (%s), want %s (%s)", tc.thread, s.RoutingMode, s.RoutingModeSource, tc.mode, tc.source)
		}
	}
}

func TestAnInvalidProjectFileIsIgnoredWithAFinding(t *testing.T) {
	cfg := legacyConfig()
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

func TestConcurrentModeCommandsSaveTheLastMode(t *testing.T) {
	var mu sync.Mutex
	saved := map[string]config.RoutingMode{}
	var calls atomic.Int64
	h := modeHarness(t, func(d *Deps) {
		d.SaveMode = func(id string, m config.RoutingMode) {
			// A disk write takes uneven time: a slow early save must not
			// land after a later one.
			time.Sleep(time.Duration(calls.Add(1)%4) * 200 * time.Microsecond)
			mu.Lock()
			defer mu.Unlock()
			saved[id] = m
		}
	})
	var wg sync.WaitGroup
	for i := range 64 {
		wg.Go(func() {
			h.apply(Command{Op: OpMode, Session: "th", Mode: config.RoutingModes[i%len(config.RoutingModes)]})
		})
	}
	wg.Wait()

	if got, want := saved["th"], h.engine.SnapshotFor("th").RoutingMode; got != want {
		t.Fatalf("saved %q, engine runs %q", got, want)
	}
}

func TestDecisionLogNamesTheAnswerOfEveryStep(t *testing.T) {
	h := modeHarness(t)
	h.asker.script(answer(mediumTier), fail(classifier.ErrTimeout))
	m := meta("th", "t1")
	items := []responses.Item{userMsg(secretPrompt)}
	h.decide(req(solMedium, items...), m)
	h.decide(req(solMedium, append(items, call("c", "ls"), result("c", 0, ""))...), m)
	h.decide(req(solMedium, append(items, userMsg("next"))...), meta("th", "t2"))

	var lines []string
	for line := range strings.SplitSeq(h.logs.String(), "\n") {
		if strings.Contains(line, `msg="route decision"`) {
			lines = append(lines, line)
		}
	}
	want := [][]string{
		{"step=0", "classified=medium"},
		{"step=1", "reason=continue", "classified=medium"},
		{"turn=t2", "reason=error", `classified=""`},
	}
	if len(lines) != len(want) {
		t.Fatalf("route decision lines = %d, want %d:\n%s", len(lines), len(want), h.logs.String())
	}
	for i, ws := range want {
		for _, w := range ws {
			if !strings.Contains(lines[i], w) {
				t.Errorf("line %d lacks %q: %s", i, w, lines[i])
			}
		}
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
