package route

import (
	"strings"
	"testing"

	"github.com/alexei-led/codex-router/internal/classifier"
	"github.com/alexei-led/codex-router/internal/config"
	"github.com/alexei-led/codex-router/internal/responses"
)

func TestProtectedOpsFloorSurvivesLaterContextGrowth(t *testing.T) {
	for phase, act := range map[string]config.Activity{"reason": config.ActivityDebug, "produce": config.ActivityCode} {
		for _, mode := range config.RoutingModes {
			for _, hold := range []bool{false, true} {
				t.Run(phase+"/"+string(mode)+map[bool]string{false: "", true: "/hold"}[hold], func(t *testing.T) {
					c := mixedOpsConfig()
					c.Mode, c.Policy.ReviewEveryRequests = mode, 0
					delete(c.Activities[act], config.TierMedium)
					a := &typedAsker{adapter: classifier.SystemOne(), answers: []string{opsAnswer("medium", phase, "0.99", "0")}}
					h := mixedOpsHarness(t, c, a)
					cat := h.cat.Current()
					model := cat.Models["deepseek"]
					model.ContextWindow = 3000
					cat.Models[model.Slug] = model
					h.cat.Set(cat)
					prompts := []string{"Operate the service"}
					m := meta("th", "t1")
					h.decide(projectionRequest(t, prompts, ""), m)
					if d := h.last(); d.Route.Model != "deepseek" || d.EffectiveActivity != config.ActivityOps {
						t.Fatalf("no inherited protection: %+v", d)
					}
					h.reply(m, "deepseek", 1000, 0)
					if hold {
						h.engine.mu.Lock()
						th := h.engine.lookup("th")
						th.state.HoldUntilTurn = th.state.Turn + 10
						h.engine.mu.Unlock()
					}
					r := projectionRequest(t, prompts, strings.Repeat("source context ", 3000))
					rw, ok := h.decide(r, m)
					wantEffort := map[config.RoutingMode]config.Effort{config.ModeBalanced: config.EffortHigh, config.ModeCost: config.EffortMedium, config.ModeQuality: config.EffortXHigh}[mode]
					want := config.Route{Model: "opus", Effort: wantEffort}
					if d := h.last(); d.Route != want || d.EffectiveActivity != act {
						t.Fatalf("later refit lost semantic floor: %+v", d)
					}
					out, err := r.Apply(rw)
					if err != nil || !ok {
						t.Fatalf("rewrite %v %v", ok, err)
					}
					wire, err := responses.Parse(out)
					if err != nil || wire.Model != want.Model || wire.Effort != want.Effort {
						t.Fatalf("wrong wire pair: %s %v", out, err)
					}
					if len(a.bodies) != 1 {
						t.Fatal("refit asked classifier again")
					}
				})
			}
		}
	}
}

func TestRefusedOpsNominationPreservesIncumbentProjection(t *testing.T) {
	for phase, incumbent := range map[string]config.Activity{"reason": config.ActivityCode, "produce": config.ActivityDebug} {
		for _, mode := range config.RoutingModes {
			for _, refusal := range []string{"confidence", "hold", "mode"} {
				if refusal == "mode" && mode != config.ModeCost {
					continue
				}
				t.Run(phase+"/"+string(mode)+"/"+refusal, func(t *testing.T) {
					c := mixedOpsConfig()
					c.Mode, c.Policy.ReviewEveryRequests = mode, 0
					delete(c.Activities[incumbent], config.TierMedium)
					goal := `,"activity":{"type":"choice","choice":"` + string(incumbent) + `","probabilities":{"` + string(incumbent) + `":0.99}}`
					mass := "0.99"
					if refusal == "confidence" {
						mass = "0.61"
					}
					a := &typedAsker{adapter: classifier.SystemOne(), answers: []string{
						systemAnswer(goal, "", "0"), opsAnswer("medium", phase, mass, "0.99"),
						opsAnswer("high", "uncertain", "0.99", "0.99"),
						opsAnswer("high", "uncertain", "0.99", "0.99"),
						opsAnswer("high", "uncertain", "0.99", "0.99"),
					}}
					h := mixedOpsHarness(t, c, a)
					m := meta("th", "t1")
					h.decide(mixedRequest(t, "Build the service"), m)
					want := h.last().Route
					h.reply(m, want.Model, 10000, 0)
					if refusal == "hold" {
						h.engine.mu.Lock()
						th := h.engine.lookup("th")
						th.state.HoldUntilTurn = th.state.Turn + 10
						h.engine.mu.Unlock()
					}
					h.decide(mixedRequest(t, "Build the service", "Continue the operation"), meta("th", "t2"))
					if d := h.last(); d.Route != want || d.EffectiveActivity != incumbent {
						t.Fatalf("refused nomination changed projection: %+v", d)
					}
					// End the synthetic hold; later tier changes still need
					// their normal confidence and mode vote streak.
					h.engine.mu.Lock()
					h.engine.lookup("th").state.HoldUntilTurn = 0
					h.engine.mu.Unlock()
					prompts := []string{"Build the service", "Continue the operation"}
					for _, turn := range []string{"t3", "t4", "t5"} {
						prompts = append(prompts, "Continue with the harder task")
						h.decide(mixedRequest(t, prompts...), meta("th", turn))
					}
					if d := h.last(); d.Route.Model != "opus" || d.EffectiveActivity != incumbent {
						t.Fatalf("later tier change followed rejected provenance: %+v", d)
					}
				})
			}
		}
	}
}
