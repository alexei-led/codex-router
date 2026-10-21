package route

import (
	"cmp"
	"flag"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/alexei-led/codex-router/internal/classifier"
	"github.com/alexei-led/codex-router/internal/config"
	"github.com/alexei-led/codex-router/internal/responses"
)

var updateMatrix = flag.Bool("update-matrix", false, "rewrite the decision matrix in docs/evaluation.md from the current engine")

const (
	matrixDoc   = "../../docs/evaluation.md"
	matrixStart = "<!-- mode-matrix:start -->\n"
	matrixEnd   = "<!-- mode-matrix:end -->\n"
)

// profileConfig is a user-like profile: a third, mid-priced model, activity
// overrides on several tiers, and prices for every routed model.
func profileConfig() config.Config {
	c := config.Default()
	c.Routes[config.TierMedium] = config.Route{Model: "gpt-6-sol", Effort: config.EffortMedium}
	c.Activities = map[config.Activity]map[config.Tier]config.Route{
		config.ActivityDebug:  {config.TierMedium: {Effort: config.EffortHigh}},
		config.ActivityCode:   {config.TierLow: {Model: "gpt-6-sol", Effort: config.EffortLow}},
		config.ActivityPlan:   {config.TierHigh: {Effort: config.EffortXHigh}},
		config.ActivityReview: {config.TierMedium: {Model: sol, Effort: config.EffortMedium}},
		config.ActivityDocs:   {config.TierHigh: {Model: "gpt-6-sol", Effort: config.EffortHigh}},
	}
	c.Prices["gpt-6-sol"] = config.Price{Input: 1.25, CachedInput: 0.125, Output: 10}
	return c
}

// spend orders two routes by what they cost to run: input price first, then
// effort. Unpriced or equally priced models order by effort alone.
func spend(cfg config.Config, a, b config.Route) int {
	if pa, pb := cfg.Prices[a.Model].Input, cfg.Prices[b.Model].Input; pa > 0 && pb > 0 && pa != pb {
		return cmp.Compare(pa, pb)
	}
	return cmp.Compare(effortRank(a.Effort), effortRank(b.Effort))
}

// TestModeDecisionMatrix decides a fresh thread's first turn for every
// classifier tier × activity × mode, on the shipped config and a user-like
// profile. It checks the invariants every cell must keep, counts the cells a
// mode changes, and pins each resolved pair and reason to the table in
// docs/evaluation.md. Codex sends the baseline route; the answers are
// confident, so the policy and the mode gate pass on one vote and the pair
// shows the mode's effect.
func TestModeDecisionMatrix(t *testing.T) {
	tiers := []classifier.Choice{"micro", "low", "medium", "high", "uncertain"}
	acts := append(append([]config.Activity(nil), config.Activities...), config.ActivityUncertain)
	// Cells whose route differs from balanced: every low, medium and high
	// cell, less those already at the end of the mode's ladder, plus the
	// uncertain debug cell where cost keeps the cheaper incumbent.
	minDiffer := map[string]map[config.RoutingMode]int{
		"default": {config.ModeCost: 25, config.ModeQuality: 24},
		"profile": {config.ModeCost: 25, config.ModeQuality: 23},
	}
	var table strings.Builder
	table.WriteString("| Config | Tier | Activity | cost | balanced | quality |\n| --- | --- | --- | --- | --- | --- |\n")
	for _, profile := range []struct {
		name string
		cfg  func() config.Config
	}{{"default", config.Default}, {"profile", profileConfig}} {
		differ := map[config.RoutingMode]int{}
		for _, tr := range tiers {
			for _, act := range acts {
				fmt.Fprintf(&table, "| %s | %s | %s |", profile.name, tr, act)
				got := map[config.RoutingMode]Decision{}
				for _, mode := range config.RoutingModes {
					cfg := profile.cfg()
					cfg.Mode = mode
					h := newHarness(t, cfg)
					advice := classifier.Advice{Choice: tr, Probabilities: map[classifier.Choice]float64{tr: 0.99}}
					h.asker.script(answer(withActivity(advice, act, 0.95)))
					h.decide(req(cfg.RouteFor(cfg.Baseline, ""), userMsg(secretPrompt)), meta("th", "t1"))
					got[mode] = h.last()
					fmt.Fprintf(&table, " %s/%s (%s) |", strings.TrimPrefix(got[mode].Route.Model, "gpt-"), got[mode].Route.Effort, got[mode].Reason)
				}
				table.WriteString("\n")

				cfg := profile.cfg()
				cell := fmt.Sprintf("%s %s %s", profile.name, tr, act)
				cost, bal, quality := got[config.ModeCost].Route, got[config.ModeBalanced].Route, got[config.ModeQuality].Route
				tier := config.Tier(tr)
				if slices.Contains(config.Tiers, tier) {
					if want := resolve(cfg.RouteFor(tier, act), ""); bal != want {
						t.Errorf("%s: balanced %v, want the configured pair %v", cell, bal, want)
					}
				}
				if tier == config.TierMicro && (cost != bal || quality != bal) {
					t.Errorf("%s: micro moved with the mode: cost %v, balanced %v, quality %v", cell, cost, bal, quality)
				}
				if spend(cfg, cost, bal) > 0 {
					t.Errorf("%s: cost %v spends more than balanced %v", cell, cost, bal)
				}
				if spend(cfg, quality, bal) < 0 {
					t.Errorf("%s: quality %v spends less than balanced %v", cell, quality, bal)
				}
				if d := got[config.ModeCost]; tier != config.TierMicro && (d.Tier == config.TierMicro || effortRank(d.Route.Effort) < 0) {
					t.Errorf("%s: cost fell to %s %v, below low or off the thinking efforts", cell, d.Tier, d.Route)
				}
				for _, mode := range []config.RoutingMode{config.ModeCost, config.ModeQuality} {
					if got[mode].Route != bal {
						differ[mode]++
					}
				}
			}
		}
		for mode, want := range minDiffer[profile.name] {
			if differ[mode] < want {
				t.Errorf("%s: %s differs from balanced in %d cells, want at least %d", profile.name, mode, differ[mode], want)
			}
		}
	}

	got := table.String()
	doc, err := os.ReadFile(matrixDoc)
	if err != nil {
		t.Fatal(err)
	}
	before, rest, ok1 := strings.Cut(string(doc), matrixStart)
	_, after, ok2 := strings.Cut(rest, matrixEnd)
	if !ok1 || !ok2 {
		t.Fatalf("%s lacks the %q and %q markers", matrixDoc, matrixStart, matrixEnd)
	}
	if *updateMatrix {
		if err := os.WriteFile(matrixDoc, []byte(before+matrixStart+got+matrixEnd+after), 0o600); err != nil {
			t.Fatal(err)
		}
		return
	}
	if want, _, _ := strings.Cut(rest, matrixEnd); want != got {
		t.Errorf("the decision matrix in %s is stale; run go test ./internal/route -run TestModeDecisionMatrix -update-matrix", matrixDoc)
	}
}

// TestAStartingRouteStaysWithinTheModesBounds starts a thread on the
// balanced medium pair, in each mode, fresh or seeded by a brain restart and
// measured, with a decision that keeps the incumbent: the starting route is
// inferred from the configured table, never from a mode's targets, so cost
// never runs pricier than balanced and quality never cheaper.
func TestAStartingRouteStaysWithinTheModesBounds(t *testing.T) {
	keeps := []struct {
		name   string
		advice func() (classifier.Advice, error)
	}{
		{"uncertain", answer(classifier.Advice{Choice: "uncertain", Probabilities: map[classifier.Choice]float64{"uncertain": 1}})},
		{"weak downgrade", answer(classifier.Advice{Choice: "low", Probabilities: map[classifier.Choice]float64{"low": 0.4, "medium": 0.6}})},
		{"classifier error", fail(classifier.ErrTimeout)},
	}
	for _, profile := range []struct {
		name string
		cfg  func() config.Config
	}{{"default", config.Default}, {"profile", profileConfig}} {
		for _, seeded := range []bool{false, true} {
			for _, k := range keeps {
				got := map[config.RoutingMode]config.Route{}
				for _, mode := range config.RoutingModes {
					cfg := profile.cfg()
					cfg.Mode = mode
					native := cfg.RouteFor(config.TierMedium, "")
					h := newHarness(t, cfg)
					items := []responses.Item{userMsg(secretPrompt)}
					if seeded {
						h.engine.Resumed("th", native)
						m := meta("th", "t0")
						h.decide(req(native, items...), m)
						h.reply(m, native.Model, 50_000, 0)
						items = append(items, userMsg("next"))
					}
					h.asker.script(k.advice)
					h.decide(req(native, items...), meta("th", "t1"))
					got[mode] = h.last().Route
				}
				cfg, bal := profile.cfg(), got[config.ModeBalanced]
				if spend(cfg, got[config.ModeCost], bal) > 0 || spend(cfg, got[config.ModeQuality], bal) < 0 {
					t.Errorf("%s seeded=%v %s: cost %v, balanced %v, quality %v", profile.name, seeded, k.name, got[config.ModeCost], bal, got[config.ModeQuality])
				}
			}
		}
	}
}

// TestAPinBypassesEveryMode pins each tier under each mode: the route is the
// tier's configured pair, whatever the mode.
func TestAPinBypassesEveryMode(t *testing.T) {
	for _, profile := range []func() config.Config{config.Default, profileConfig} {
		for _, tier := range config.Tiers {
			for _, mode := range config.RoutingModes {
				cfg := profile()
				cfg.Mode = mode
				h := newHarness(t, cfg)
				h.apply(Command{Op: OpPin, Session: "th", Tier: tier})
				h.decide(req(cfg.RouteFor(cfg.Baseline, ""), userMsg(secretPrompt)), meta("th", "t1"))
				if d, want := h.last(), resolve(cfg.RouteFor(tier, ""), ""); d.Route != want {
					t.Errorf("%s pinned to %s: route %v, want %v", mode, tier, d.Route, want)
				}
			}
		}
	}
}

// modeTotals sums a replay's routing over its threads.
type modeTotals struct {
	decisions, onSol, effortSum, switches, uncached int
	routedUSD                                       float64
	priced, eligible                                int
}

func measure(t *testing.T, cfg config.Config, mode config.RoutingMode) modeTotals {
	var m modeTotals
	cfg.Mode = mode
	for _, seed := range []uint64{1, 2, 3} {
		decisions, h := replay(t, cfg, seed)
		for _, d := range decisions {
			m.decisions++
			if d.Route.Model == sol {
				m.onSol++
			}
			m.effortSum += effortRank(d.Route.Effort)
		}
		for _, id := range []string{"a", "b"} {
			s := h.engine.SnapshotFor(id).Stats
			m.switches += s.Switches
			m.uncached += s.UncachedSwitchTokens
			m.routedUSD += s.RoutedUSD
			m.priced += s.PricedReplies
			m.eligible += s.EligibleReplies
		}
	}
	return m
}

// TestModesMakeAMeasurableDifference replays the same seeded sessions under
// each mode and compares what the router chose: estimated cost (observed
// tokens at list prices), the share of steps on the strong model, mean effort,
// switches and the cache they cost. The replay's token counts do not depend
// on the route, so the cost difference is the price of the models chosen; an
// effort change shows only in the effort mix.
func TestModesMakeAMeasurableDifference(t *testing.T) {
	for _, profile := range []struct {
		name     string
		cfg      func() config.Config
		unpriced bool
	}{{"default", config.Default, false}, {"profile", profileConfig, false}, {"unpriced", testConfig, true}} {
		t.Run(profile.name, func(t *testing.T) {
			t.Parallel()
			got := map[config.RoutingMode]modeTotals{}
			for _, mode := range config.RoutingModes {
				m := measure(t, profile.cfg(), mode)
				got[mode] = m
				t.Logf("%-8s decisions %4d  usd %8.4f (priced %d/%d)  sol %3d%%  effort %.2f  switches %3d  switch-uncached %8d",
					mode, m.decisions, m.routedUSD, m.priced, m.eligible, 100*m.onSol/m.decisions, m.effort(), m.switches, m.uncached)
			}
			cost, bal, quality := got[config.ModeCost], got[config.ModeBalanced], got[config.ModeQuality]
			if quality.effort() <= bal.effort() {
				t.Errorf("quality mean effort %.2f, balanced %.2f: quality must spend more", quality.effort(), bal.effort())
			}
			switch {
			case profile.unpriced && (cost.routedUSD != 0 || cost.effort() >= bal.effort()):
				t.Errorf("cost without prices must lower effort only: usd %v, effort %.2f vs %.2f", cost.routedUSD, cost.effort(), bal.effort())
			case !profile.unpriced && cost.routedUSD >= bal.routedUSD:
				t.Errorf("cost mode usd %.4f, balanced %.4f: cost must spend less", cost.routedUSD, bal.routedUSD)
			}
		})
	}
}

// effort is the mean effort rank of the decisions: 0 low to 3 xhigh.
func (m modeTotals) effort() float64 { return float64(m.effortSum) / float64(m.decisions) }
