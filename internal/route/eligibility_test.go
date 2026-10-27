package route

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/alexei-led/codex-router/internal/catalog"
	"github.com/alexei-led/codex-router/internal/classifier"
	"github.com/alexei-led/codex-router/internal/config"
)

func TestCustomDeclarationsKeepTheirModelsAndEfforts(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		for _, activity := range append([]config.Activity{""}, config.Activities...) {
			for _, mode := range config.RoutingModes {
				cfg := config.Default()
				if legacy {
					cfg = legacyConfig()
				} else {
					cfg.Routes[config.TierMedium] = config.Route{Model: "work/custom", Effort: config.EffortLow}
				}
				path := filepath.Join(t.TempDir(), "router.json")
				data, err := json.Marshal(cfg)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, data, 0o600); err != nil {
					t.Fatal(err)
				}
				cfg, err = config.Load(path)
				if err != nil {
					t.Fatal(err)
				}
				cfg.Mode = mode
				h := newHarness(t, cfg)
				cat := h.cat.Current()
				model := cat.Models[sol]
				model.Slug = "work/custom"
				model.ReasoningLevels = []catalog.ReasoningLevel{{Effort: config.EffortLow}, {Effort: config.EffortMedium}}
				cat.Models[model.Slug] = model
				h.cat.Set(cat)
				a := mediumTier
				if activity != "" {
					a = withActivity(a, activity, 1)
				}
				h.asker.script(answer(a))
				original := cfg.Routes[config.TierMedium]
				h.decide(req(original, userMsg(secretPrompt)), meta("th", "turn"))
				want := genericTarget(cfg, mode, config.TierMedium, activity, original.Effort, levelsIn(cat))
				if got := h.last().Route; got != want {
					t.Fatalf("legacy=%v %s/%s = %v, want %v", legacy, mode, activity, got, want)
				}
			}
		}
	}
}

func TestExplicitActivityCellsAndOptOut(t *testing.T) {
	for _, mode := range config.RoutingModes {
		cfg := config.Default()
		cfg.Activities[config.ActivityCode] = map[config.Tier]config.Route{config.TierMedium: {Model: sol, Effort: config.EffortLow}}
		got := target(cfg, mode, config.TierMedium, config.ActivityCode, "", func(string) []config.Effort { return config.Efforts })
		want := genericTarget(cfg, mode, config.TierMedium, config.ActivityCode, "", func(string) []config.Effort { return config.Efforts })
		if got != want {
			t.Fatalf("%s explicit activity = %v, want %v", mode, got, want)
		}
		cfg = config.Default()
		cfg.Activities = nil
		if got, want := target(cfg, mode, config.TierMedium, config.ActivityCode, "", levelsIn(catalog.Catalog{})), target(cfg, mode, config.TierMedium, "", "", levelsIn(catalog.Catalog{})); got != want {
			t.Fatalf("%s activity opt-out = %v, generic = %v", mode, got, want)
		}
	}
}

func TestPreferredTargetEligibilityFallback(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*catalog.Catalog)
	}{
		{"missing", func(c *catalog.Catalog) { delete(c.Models, terra) }},
		{"incompatible", func(c *catalog.Catalog) { m := c.Models[terra]; m.CompHash = "other"; c.Models[terra] = m }},
		{"effort", func(c *catalog.Catalog) {
			m := c.Models[terra]
			m.ReasoningLevels = []catalog.ReasoningLevel{{Effort: config.EffortLow}}
			c.Models[terra] = m
		}},
		{"context", func(c *catalog.Catalog) { m := c.Models[terra]; m.ContextWindow = 10; c.Models[terra] = m }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := config.Default()
			cfg.Mode = config.ModeQuality
			h := newHarness(t, cfg)
			c := h.cat.Current()
			tc.mutate(&c)
			h.cat.Set(c)
			h.asker.script(answer(classifier.Advice{Choice: "low", Probabilities: map[classifier.Choice]float64{"low": 1}}))
			native := config.Route{Model: sol, Effort: config.EffortMedium}
			rw, ok := h.decide(req(native, userMsg(secretPrompt)), meta("th", "turn"))
			want := config.Route{Model: luna, Effort: config.EffortXHigh}
			wantRoute(t, rw, ok, want)
			d := h.last()
			if d.Preferred == nil || d.Preferred.Model != terra || d.Reason != ReasonUnavailable {
				t.Fatalf("fallback reason = %+v", d)
			}
			if preview := h.engine.SnapshotFor("th").Routes[config.TierLow]; preview != want {
				t.Fatalf("preview=%v, actual=%v", preview, want)
			}
		})
	}
}

func TestPinPreviewBypassesCostMode(t *testing.T) {
	cfg := config.Default()
	cfg.Mode = config.ModeCost
	h := newHarness(t, cfg)
	h.apply(Command{Op: OpPin, Session: "th", Tier: config.TierHigh})
	if got := h.engine.SnapshotFor("th").Routes[config.TierHigh]; got != cfg.Routes[config.TierHigh] {
		t.Fatalf("pin preview %v", got)
	}
	rw, ok := h.decide(req(cfg.Routes[config.TierMedium], userMsg(secretPrompt)), meta("th", "turn"))
	wantRoute(t, rw, ok, cfg.Routes[config.TierHigh])
}
