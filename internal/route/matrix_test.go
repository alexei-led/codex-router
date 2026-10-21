package route

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/alexei-led/codex-router/internal/classifier"
	"github.com/alexei-led/codex-router/internal/config"
)

var updateMatrix = flag.Bool("update-matrix", false, "rewrite testdata/mode-matrix.golden from the current engine")

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

// TestModeDecisionMatrix decides a fresh thread's first turn for every
// classifier tier × activity × mode, on the shipped config and a user-like
// profile, and pins each resolved pair and reason to testdata/mode-matrix.golden.
// Codex sends the baseline route; the answers are confident, so the policy
// and the mode gate pass on one vote and the pair shows the mode's effect.
func TestModeDecisionMatrix(t *testing.T) {
	tiers := []classifier.Choice{"micro", "low", "medium", "high", "uncertain"}
	acts := append(append([]config.Activity(nil), config.Activities...), config.ActivityUncertain)
	var out bytes.Buffer
	for _, profile := range []struct {
		name string
		cfg  func() config.Config
	}{{"default", config.Default}, {"profile", profileConfig}} {
		for _, tr := range tiers {
			for _, act := range acts {
				fmt.Fprintf(&out, "%-8s %-9s %-9s", profile.name, tr, act)
				for _, mode := range config.RoutingModes {
					cfg := profile.cfg()
					cfg.Mode = mode
					h := newHarness(t, cfg)
					advice := classifier.Advice{Choice: tr, Probabilities: map[classifier.Choice]float64{tr: 0.99}}
					h.asker.script(answer(withActivity(advice, act, 0.95)))
					base := cfg.RouteFor(cfg.Baseline, "")
					h.decide(req(base, userMsg(secretPrompt)), meta("th", "t1"))
					d := h.last()
					fmt.Fprintf(&out, " | %s %-24s %-14s", mode, d.Route.Model+"/"+string(d.Route.Effort), d.Reason)
				}
				out.WriteString("\n")
			}
		}
	}
	path := filepath.Join("testdata", "mode-matrix.golden")
	if *updateMatrix {
		if err := os.WriteFile(path, out.Bytes(), 0o600); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(out.Bytes(), want) {
		t.Errorf("decision matrix differs from %s; got:\n%s", path, out.String())
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
