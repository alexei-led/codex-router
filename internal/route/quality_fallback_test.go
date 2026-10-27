package route

import (
	"testing"

	"github.com/alexei-led/codex-router/internal/catalog"
	"github.com/alexei-led/codex-router/internal/config"
)

func TestQualityFallbackIsNotWeakerThanEligibleBalancedPair(t *testing.T) {
	cfg := config.Default()
	cfg.Mode = config.ModeQuality
	h := newHarness(t, cfg)
	c := h.cat.Current()
	m := c.Models[sol]
	m.ReasoningLevels = []catalog.ReasoningLevel{{Effort: config.EffortLow}}
	c.Models[sol] = m
	h.cat.Set(c)
	h.asker.script(answer(withActivity(mediumTier, config.ActivityCode, 1)))
	h.decide(req(cfg.Routes[cfg.Baseline], userMsg(secretPrompt)), meta("th", "turn"))
	if d := h.last(); d.Route != (config.Route{Model: terra, Effort: config.EffortXHigh}) {
		t.Fatalf("quality fallback weaker than balanced: %+v", d)
	}
}
