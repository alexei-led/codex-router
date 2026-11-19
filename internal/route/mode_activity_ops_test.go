package route

import (
	"testing"

	"github.com/alexei-led/codex-router/internal/classifier"
	"github.com/alexei-led/codex-router/internal/config"
)

func TestModeActivityTargetOwnsOpsReasoningOnFreshTaskAndContinuation(t *testing.T) {
	for _, continuation := range []bool{false, true} {
		name := "fresh"
		if continuation {
			name = "continuation"
		}
		t.Run(name, func(t *testing.T) {
			cfg := mixedOpsConfig()
			delete(cfg.Activities[config.ActivityDebug], config.TierMedium)
			cfg.Mode = config.ModeQuality
			target := config.Route{Model: "sonnet", Effort: config.EffortHigh}
			mode := cfg.Modes[config.ModeQuality]
			mode.Activities = map[config.Activity]map[config.Tier]*config.Route{
				config.ActivityDebug: {config.TierMedium: &target},
			}
			cfg.Modes[config.ModeQuality] = mode
			answers := []string{opsAnswer("medium", "reason", "0.99", "0")}
			if continuation {
				answers = []string{
					opsAnswer("medium", "execute", "0.99", "0"),
					opsAnswer("medium", "reason", "0.99", "0.99"),
				}
			}
			a := &typedAsker{adapter: classifier.SystemOne(), answers: answers}
			h := mixedOpsHarness(t, cfg, a)
			m := meta("th", "t1")
			h.decide(mixedRequest(t, "Diagnose and fix the service"), m)
			if continuation {
				h.reply(m, cfg.RouteFor(config.TierMedium, config.ActivityOps).Model, 10_000, 0)
				h.decide(mixedRequest(t, "Diagnose and fix the service", "Continue the investigation"), meta("th", "t2"))
			}
			if d := h.last(); d.Route != target || d.EffectiveActivity != config.ActivityDebug {
				t.Fatalf("mode-owned reasoning target = %+v, want %+v", d, target)
			}
		})
	}
}
