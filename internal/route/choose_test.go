package route

import (
	"testing"

	"github.com/alexei-led/codex-router/internal/classifier"
	"github.com/alexei-led/codex-router/internal/config"
)

func TestCompareRoutes(t *testing.T) {
	cfg := config.Default()
	priced := config.Default()
	priced.Prices = map[string]config.Price{"gpt-6-luna": {Output: 10}, "gpt-6-sol": {Output: 4}}
	tests := []struct {
		name    string
		cfg     config.Config
		a, b    config.Route
		want    int
		ordered bool
	}{
		{"same route", cfg, medium, medium, 0, true},
		{"higher effort on one model", cfg, debug, medium, 1, true},
		{"lower effort on one model", cfg, medium, debug, -1, true},
		{"unknown effort", cfg, config.Route{Model: "gpt-6-sol", Effort: "max"}, medium, 0, false},
		{"stronger tier's model", cfg, high, medium, 1, true},
		{"cheaper tier's model", cfg, low, high, -1, true},
		{"model outside the routes", cfg, config.Route{Model: "gpt-x", Effort: config.EffortHigh}, low, 0, false},
		{"price orders before rank", priced, low, medium, 1, true},
		{"one price falls back to rank", priced, high, medium, 1, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := compareRoutes(tt.cfg, tt.a, tt.b)
			if got != tt.want || ok != tt.ordered {
				t.Fatalf("compareRoutes = %d, %v; want %d, %v", got, ok, tt.want, tt.ordered)
			}
		})
	}
}

func TestChooseActivity(t *testing.T) {
	cfg := config.Default()
	p := cfg.Effective()
	atMedium := running{tier: config.TierMedium, route: medium}
	onDebug := running{tier: config.TierMedium, activity: config.ActivityDebug, route: debug}
	labelled := func(act config.Activity, mass float64) *classifier.Advice {
		a := withActivity(tier("medium", 1), act, mass)
		return &a
	}
	sameTaskAdvice := func(act config.Activity, mass float64) *classifier.Advice {
		a := continuing(withActivity(tier("medium", 1), act, mass), 0.9)
		return &a
	}
	tests := []struct {
		name    string
		advice  *classifier.Advice
		inc     running
		midTurn bool
		down    bool
		want    config.Activity
	}{
		{"no advice keeps the incumbent", nil, onDebug, false, false, config.ActivityDebug},
		{"no activity answer keeps the incumbent", &classifier.Advice{Choice: "medium"}, onDebug, false, false, config.ActivityDebug},
		{"new task takes a strong label", labelled(config.ActivityDebug, 0.6), atMedium, false, false, config.ActivityDebug},
		{"new task drops a weak label", labelled(config.ActivityDebug, 0.5), onDebug, false, false, ""},
		{"new task drops an unknown label", labelled(config.ActivityUncertain, 0.9), onDebug, false, false, ""},
		{"same task takes a stronger route at once", sameTaskAdvice(config.ActivityDebug, 0.3), atMedium, false, false, config.ActivityDebug},
		{"mid-turn takes a stronger route at once", labelled(config.ActivityDebug, 0.3), atMedium, true, false, config.ActivityDebug},
		{"same task keeps an unknown label's incumbent", sameTaskAdvice(config.ActivityUncertain, 0.9), onDebug, false, false, config.ActivityDebug},
		{"same task holds a cheaper route without the gate", sameTaskAdvice(config.ActivityCode, 0.95), onDebug, false, false, config.ActivityDebug},
		{"same task takes a cheaper route past the gate", sameTaskAdvice(config.ActivityCode, 0.95), onDebug, false, true, config.ActivityCode},
		{"same task holds a weak cheaper label past the gate", sameTaskAdvice(config.ActivityCode, 0.5), onDebug, false, true, config.ActivityDebug},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := chooseActivity(cfg, p, tt.advice, tt.inc, config.EffortMedium, tt.midTurn, tt.down)
			if got != tt.want {
				t.Fatalf("chooseActivity = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestCellForModel(t *testing.T) {
	cfg := config.Default()
	cfg.Activities[config.ActivityPlan] = map[config.Tier]config.Route{config.TierHigh: {Model: "gpt-7"}}
	tests := []struct {
		route    config.Route
		tier     config.Tier
		activity config.Activity
	}{
		{medium, config.TierMedium, ""},
		{low, config.TierLow, ""},
		{config.Route{Model: "gpt-6-luna", Effort: config.EffortLow}, config.TierMicro, ""},
		{config.Route{Model: "gpt-6-luna", Effort: config.EffortXHigh}, config.TierMicro, ""},
		{debug, config.TierMedium, config.ActivityDebug},
		{config.Route{Model: "gpt-6-sol", Effort: config.EffortLow}, config.TierMedium, ""},
		{high, config.TierHigh, ""},
		{config.Route{Model: "gpt-7"}, config.TierHigh, config.ActivityPlan},
		{config.Route{Model: "gpt-5.5"}, config.TierMedium, ""},
	}
	for _, tt := range tests {
		t.Run(tt.route.Model+"/"+string(tt.route.Effort), func(t *testing.T) {
			tr, act := cellForModel(cfg, tt.route)
			if tr != tt.tier || act != tt.activity {
				t.Fatalf("cellForModel = %s/%s, want %s/%s", tr, act, tt.tier, tt.activity)
			}
		})
	}
}
