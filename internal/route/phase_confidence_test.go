package route

import (
	"fmt"
	"testing"

	"github.com/alexei-led/codex-router/internal/classifier"
	"github.com/alexei-led/codex-router/internal/config"
)

func TestNewSameLabelOpsProjectionUsesPhaseConfidence(t *testing.T) {
	for phase, act := range map[string]config.Activity{"reason": config.ActivityDebug, "produce": config.ActivityCode} {
		for _, mode := range config.RoutingModes {
			for _, hold := range []bool{false, true} {
				t.Run(phase+"/"+string(mode)+map[bool]string{false: "", true: "/hold"}[hold], func(t *testing.T) {
					c := mixedOpsConfig()
					c.Mode, c.Policy.ReviewEveryRequests = mode, 0
					c.Routes[config.TierHigh] = c.RouteFor(config.TierMedium, config.ActivityOps)
					delete(c.Activities[act], config.TierMedium)
					weakMass := 0.65
					if mode == config.ModeQuality {
						weakMass = 0.61
					}
					goal := fmt.Sprintf(`,"activity":{"type":"choice","choice":%q,"probabilities":{%q:0.99}}`, act, act)
					a := &typedAsker{adapter: classifier.SystemOne(), answers: []string{
						systemAnswer(goal, "", "0"),
						opsAnswer("medium", phase, fmt.Sprint(weakMass), "0.99"),
						opsAnswer("medium", phase, fmt.Sprint(weakMass), "0.99"),
						opsAnswer("medium", phase, fmt.Sprint(weakMass), "0.99"),
						opsAnswer("medium", phase, "0.99", "0.99"),
						opsAnswer("medium", phase, "0.99", "0.99"),
					}}
					h := mixedOpsHarness(t, c, a)
					prompts := []string{"Diagnose the service"}
					m := meta("th", "t1")
					h.decide(mixedRequest(t, prompts...), m)
					incumbent := h.last().Route
					h.reply(m, incumbent.Model, 10000, 0)
					if hold {
						h.engine.mu.Lock()
						th := h.engine.lookup("th")
						th.state.HoldUntilTurn = th.state.Turn + 10
						h.engine.mu.Unlock()
					}
					for _, turn := range []string{"t2", "t3", "t4"} {
						prompts = append(prompts, "Continue the operation")
						h.decide(mixedRequest(t, prompts...), meta("th", turn))
						d := h.last()
						if d.Route != incumbent || d.EffectiveActivity != act {
							t.Fatalf("weak phase changed pair/provenance: phase=%g decision=%+v", weakMass, d)
						}
						if d.Estimate == nil || d.Estimate.Mass != weakMass {
							t.Fatalf("gate did not use actual phase support %g: %+v", weakMass, d.Estimate)
						}
					}
					if hold {
						h.engine.mu.Lock()
						h.engine.lookup("th").state.HoldUntilTurn = 0
						h.engine.mu.Unlock()
					}
					for _, turn := range []string{"t5", "t6"} {
						prompts = append(prompts, "Continue with the accepted engineering phase")
						h.decide(mixedRequest(t, prompts...), meta("th", turn))
					}
					wantEffort := map[config.RoutingMode]config.Effort{
						config.ModeBalanced: config.EffortHigh, config.ModeCost: config.EffortMedium, config.ModeQuality: config.EffortXHigh,
					}[mode]
					if d := h.last(); d.Route != (config.Route{Model: "deepseek", Effort: wantEffort}) || d.EffectiveActivity != config.ActivityOps {
						t.Fatalf("strong phase did not pass existing gates: %+v", d)
					}
				})
			}
		}
	}
}
