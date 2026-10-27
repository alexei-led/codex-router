package route

import (
	"testing"

	"github.com/alexei-led/codex-router/internal/catalog"
	"github.com/alexei-led/codex-router/internal/config"
)

func TestCostHighRetainsAnEligibleBalancedHighFallback(t *testing.T) {
	for _, activity := range []config.Activity{"", config.ActivityDebug} {
		for _, tc := range []struct {
			name   string
			mutate func(*catalog.Catalog)
		}{
			{"missing", func(c *catalog.Catalog) { delete(c.Models, terra) }},
			{"incompatible", func(c *catalog.Catalog) { m := c.Models[terra]; m.CompHash = "other"; c.Models[terra] = m }},
			{"unsupported effort", func(c *catalog.Catalog) {
				m := c.Models[terra]
				m.ReasoningLevels = []catalog.ReasoningLevel{{Effort: config.EffortLow}}
				c.Models[terra] = m
			}},
			{"context", func(c *catalog.Catalog) { m := c.Models[terra]; m.ContextWindow = 10; c.Models[terra] = m }},
		} {
			t.Run(string(activity)+"/"+tc.name, func(t *testing.T) {
				cfg := config.Default()
				cfg.Mode = config.ModeCost
				h := newHarness(t, cfg)
				c := h.cat.Current()
				tc.mutate(&c)
				h.cat.Set(c)
				advice := tier("high", 1)
				if activity != "" {
					advice = withActivity(advice, activity, 1)
				}
				h.asker.script(answer(advice))
				native := config.Route{Model: sol, Effort: config.EffortMedium}
				rw, ok := h.decide(req(native, userMsg(secretPrompt)), meta("th", "turn"))
				want := config.Route{Model: sol, Effort: config.EffortHigh}
				if activity == config.ActivityDebug {
					want.Effort = config.EffortXHigh
				}
				wantRoute(t, rw, ok, want)
				if d := h.last(); d.Tier != config.TierHigh || d.Route != want || d.Reason != ReasonUnavailable {
					t.Fatalf("same-tier fallback=%+v, want high %v unavailable", d, want)
				}
			})
		}
	}
}

func TestQualityRejectsAnUnavailableStrongerFallback(t *testing.T) {
	cfg := config.Default()
	cfg.Mode = config.ModeQuality
	h := newHarness(t, cfg)
	c := h.cat.Current()
	delete(c.Models, sol)
	m := c.Models[terra]
	m.ReasoningLevels = []catalog.ReasoningLevel{{Effort: config.EffortLow}, {Effort: config.EffortMedium}}
	c.Models[terra] = m
	h.cat.Set(c)
	h.asker.script(answer(tier("medium", 1)))
	native := config.Route{Model: terra, Effort: config.EffortMedium}
	rw, ok := h.decide(req(native, userMsg(secretPrompt)), meta("th", "turn"))
	wantUnchanged(t, rw, ok)
	if d := h.last(); d.Tier != config.TierMedium || d.Route != native || d.Reason != ReasonUnavailable {
		t.Fatalf("quality lost eligible balanced medium: %+v", d)
	}
}
