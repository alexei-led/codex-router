package route

import (
	"testing"

	"github.com/alexei-led/codex-router/internal/config"
)

const (
	sol  = "gpt-6.1-sol"
	luna = "gpt-6-luna"
)

func TestTargetDefaults(t *testing.T) {
	cfg := config.Default()
	all := func(string) []config.Effort { return nil }
	r := func(m string, e config.Effort) config.Route { return config.Route{Model: m, Effort: e} }
	cases := []struct {
		tier     config.Tier
		activity config.Activity
		cost     config.Route
		balanced config.Route
		quality  config.Route
	}{
		{config.TierMicro, "", r(luna, config.EffortMedium), r(luna, config.EffortMedium), r(luna, config.EffortMedium)},
		{config.TierLow, "", r(luna, config.EffortMedium), r(luna, config.EffortHigh), r(luna, config.EffortXHigh)},
		{config.TierMedium, "", r(luna, config.EffortHigh), r(sol, config.EffortMedium), r(sol, config.EffortHigh)},
		{config.TierMedium, config.ActivityDebug, r(luna, config.EffortHigh), r(sol, config.EffortHigh), r(sol, config.EffortXHigh)},
		{config.TierHigh, "", r(sol, config.EffortMedium), r(sol, config.EffortHigh), r(sol, config.EffortXHigh)},
		{config.TierHigh, config.ActivityDebug, r(sol, config.EffortMedium), r(sol, config.EffortHigh), r(sol, config.EffortXHigh)},
	}
	for _, tc := range cases {
		for mode, want := range map[config.RoutingMode]config.Route{
			config.ModeCost: tc.cost, config.ModeBalanced: tc.balanced, config.ModeQuality: tc.quality, "": tc.balanced,
		} {
			if got := target(cfg, mode, tc.tier, tc.activity, config.EffortMedium, all); got != want {
				t.Errorf("%q %s/%s = %+v, want %+v", mode, tc.tier, tc.activity, got, want)
			}
		}
	}
}

func TestTargetLevelsAndPrices(t *testing.T) {
	only := func(m string, efforts ...config.Effort) func(string) []config.Effort {
		return func(model string) []config.Effort {
			if model == m {
				return efforts
			}
			return nil
		}
	}
	unpriced := config.Default()
	unpriced.Prices = nil
	sameModel := config.Default()
	sameModel.Routes[config.TierLow] = config.Route{Model: sol, Effort: config.EffortLow}
	emptyEffort := config.Default()
	emptyEffort.Routes[config.TierMedium] = config.Route{Model: sol}
	pricierBelow := config.Default()
	pricierBelow.Prices[luna] = config.Price{Input: 3, Output: 1}
	cases := []struct {
		name   string
		cfg    config.Config
		mode   config.RoutingMode
		tier   config.Tier
		sent   config.Effort
		levels func(string) []config.Effort
		want   config.Route
	}{
		{"quality without xhigh moves up a tier", config.Default(), config.ModeQuality, config.TierLow, "", only(luna, config.EffortLow, config.EffortMedium, config.EffortHigh), config.Route{Model: sol, Effort: config.EffortMedium}},
		{"quality without xhigh at high stays", config.Default(), config.ModeQuality, config.TierHigh, "", only(sol, config.EffortMedium, config.EffortHigh), config.Route{Model: sol, Effort: config.EffortHigh}},
		{"quality skips an unsupported level upward", config.Default(), config.ModeQuality, config.TierMedium, "", only(sol, config.EffortMedium, config.EffortXHigh), config.Route{Model: sol, Effort: config.EffortXHigh}},
		{"cost projects onto the current level: no move", config.Default(), config.ModeCost, config.TierHigh, "", only(sol, config.EffortLow, config.EffortHigh), config.Route{Model: sol, Effort: config.EffortHigh}},
		{"cost never raises effort", config.Default(), config.ModeCost, config.TierHigh, "", only(sol, config.EffortLow, config.EffortXHigh), config.Route{Model: sol, Effort: config.EffortHigh}},
		{"cost falls to the highest lower level", config.Default(), config.ModeCost, config.TierHigh, "", only(sol, config.EffortLow), config.Route{Model: sol, Effort: config.EffortLow}},
		{"cost at low effort stays: the floor", sameModel, config.ModeCost, config.TierLow, "", nil, config.Route{Model: sol, Effort: config.EffortLow}},
		{"cost never lands on micro", sameModel, config.ModeCost, config.TierMedium, "", nil, config.Route{Model: sol, Effort: config.EffortLow}},
		{"unpriced models use the effort lever", unpriced, config.ModeCost, config.TierMedium, "", nil, config.Route{Model: sol, Effort: config.EffortLow}},
		{"a pricier lower model uses the effort lever", pricierBelow, config.ModeCost, config.TierMedium, "", nil, config.Route{Model: sol, Effort: config.EffortLow}},
		{"an empty effort resolves to the sent one", emptyEffort, config.ModeQuality, config.TierMedium, config.EffortLow, nil, config.Route{Model: sol, Effort: config.EffortMedium}},
		{"an effort off the ladder gets no effort lever", emptyEffort, config.ModeQuality, config.TierMedium, "max", nil, config.Route{Model: sol, Effort: config.EffortHigh}},
		{"an effort off the ladder in cost keeps the pair", emptyEffort, config.ModeCost, config.TierHigh, "max", nil, config.Route{Model: sol, Effort: config.EffortMedium}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			levels := tc.levels
			if levels == nil {
				levels = func(string) []config.Effort { return nil }
			}
			if got := target(tc.cfg, tc.mode, tc.tier, "", tc.sent, levels); got != tc.want {
				t.Errorf("target = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestTargetDoesNotCompound(t *testing.T) {
	cfg := config.Default()
	all := func(string) []config.Effort { return nil }
	first := target(cfg, config.ModeCost, config.TierHigh, "", "", all)
	for range 3 {
		if got := target(cfg, config.ModeCost, config.TierHigh, "", first.Effort, all); got != first {
			t.Fatalf("repeated cost target = %+v, want %+v", got, first)
		}
	}
}
