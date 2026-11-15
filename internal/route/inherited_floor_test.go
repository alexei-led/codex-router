package route

import (
	"testing"

	"github.com/alexei-led/codex-router/internal/classifier"
	"github.com/alexei-led/codex-router/internal/config"
)

func TestOpsInheritedFloorCannotWeakenDeclaredOps(t *testing.T) {
	for _, phase := range []string{"reason", "produce"} {
		for _, mode := range config.RoutingModes {
			for _, declaredRank := range []bool{false, true} {
				t.Run(phase+"/"+string(mode)+"/"+map[bool]string{false: "unknown", true: "ordered"}[declaredRank], func(t *testing.T) {
					c := mixedOpsConfig()
					c.Mode = mode
					if declaredRank {
						c.Routes[config.TierHigh] = c.RouteFor(config.TierMedium, config.ActivityOps)
					}
					delete(c.Activities[config.ActivityDebug], config.TierMedium)
					delete(c.Activities[config.ActivityCode], config.TierMedium)
					a := &typedAsker{adapter: classifier.SystemOne(), answers: []string{opsAnswer("medium", phase, "0.99", "0")}}
					h := mixedOpsHarness(t, c, a)
					h.decide(mixedRequest(t, "Continue the operation"), meta("th", "t1"))
					if d := h.last(); d.Route.Model != "deepseek" || d.EffectiveActivity != config.ActivityOps {
						t.Fatalf("inherited generic floor weakened ops: %+v", d)
					}
				})
			}
		}
	}
}

func TestOpsExplicitUseTierFloorRemainsUserOwned(t *testing.T) {
	for phase, act := range map[string]config.Activity{"reason": config.ActivityDebug, "produce": config.ActivityCode} {
		t.Run(phase, func(t *testing.T) {
			c := mixedOpsConfig()
			c.Activities[act][config.TierMedium] = config.Route{}
			a := &typedAsker{adapter: classifier.SystemOne(), answers: []string{opsAnswer("medium", phase, "0.99", "0")}}
			h := mixedOpsHarness(t, c, a)
			h.decide(mixedRequest(t, "Continue the operation"), meta("th", "t1"))
			if d := h.last(); d.Route != c.Routes[config.TierMedium] || d.EffectiveActivity != act {
				t.Fatalf("explicit use-tier floor overwritten: %+v", d)
			}
		})
	}
}

func TestOpsInheritedFloorSafetySurvivesContextFit(t *testing.T) {
	c := mixedOpsConfig()
	delete(c.Activities[config.ActivityDebug], config.TierMedium)
	a := &typedAsker{adapter: classifier.SystemOne(), answers: []string{opsAnswer("medium", "reason", "0.99", "0")}}
	h := mixedOpsHarness(t, c, a)
	cat := h.cat.Current()
	m := cat.Models["deepseek"]
	m.ContextWindow = 1000
	cat.Models[m.Slug] = m
	h.cat.Set(cat)
	r := mixedRequest(t, "Diagnose the operation")
	r.Bytes = 4 * 10000
	h.decide(r, meta("th", "t1"))
	if d := h.last(); d.Tier != config.TierHigh || d.Route.Model != "opus" || d.EffectiveActivity != config.ActivityDebug {
		t.Fatalf("context-fit fallback lost reasoning floor: %+v", d)
	}
}
