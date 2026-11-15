package route

import (
	"context"
	"testing"

	"github.com/alexei-led/codex-router/internal/classifier"
	"github.com/alexei-led/codex-router/internal/config"
)

func TestOpsExplicitUnorderedNominationUsesAllModeGates(t *testing.T) {
	for _, phase := range []string{"reason", "produce"} {
		for _, mode := range config.RoutingModes {
			t.Run(phase+"/"+string(mode), func(t *testing.T) {
				c := mixedOpsConfig()
				c.Mode = mode
				a := &typedAsker{adapter: classifier.SystemOne(), answers: []string{
					opsAnswer("high", "execute", "0.99", "0"),
					opsAnswer("high", phase, "0.99", "0.99"),
					opsAnswer("high", phase, "0.99", "0.99"),
				}}
				h := mixedOpsHarness(t, c, a)
				m := meta("th", "t1")
				h.decide(mixedRequest(t, "Operate the service"), m)
				h.reply(m, "sonnet", 10000, 0)
				h.decide(mixedRequest(t, "Operate the service", "Diagnose and fix"), meta("th", "t2"))
				if mode == config.ModeCost && h.last().Route.Model != "sonnet" {
					t.Fatal("nomination bypassed cost-mode vote streak")
				}
				h.decide(mixedRequest(t, "Operate the service", "Diagnose and fix", "Continue"), meta("th", "t3"))
				wantEffort := map[config.RoutingMode]config.Effort{
					config.ModeBalanced: config.EffortHigh, config.ModeCost: config.EffortMedium, config.ModeQuality: config.EffortXHigh,
				}[mode]
				if d := h.last(); d.Route.Model != "opus" || d.Route.Effort != wantEffort {
					t.Fatalf("explicit floor did not pass existing gates: %+v", d)
				}
			})
		}
	}
}

func TestOpsExplicitUnorderedNominationRefusals(t *testing.T) {
	for _, mode := range config.RoutingModes {
		for _, refusal := range []string{"weak phase", "below upgrade bar", "hold", "cancel", "unavailable"} {
			t.Run(string(mode)+"/"+refusal, func(t *testing.T) {
				c := mixedOpsConfig()
				c.Mode = mode
				mass := "0.99"
				if refusal == "weak phase" {
					mass = "0.2"
				}
				if refusal == "below upgrade bar" {
					mass = "0.61"
				}
				a := &typedAsker{adapter: classifier.SystemOne(), answers: []string{
					opsAnswer("high", "execute", "0.99", "0"),
					opsAnswer("high", "reason", mass, "0.99"),
				}}
				h := mixedOpsHarness(t, c, a)
				m := meta("th", "t1")
				h.decide(mixedRequest(t, "Operate the service"), m)
				want := h.last().Route
				h.reply(m, want.Model, 10000, 0)
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				switch refusal {
				case "hold":
					h.engine.mu.Lock()
					th := h.engine.lookup("th")
					th.state.HoldUntilTurn = th.state.Turn + 3
					h.engine.mu.Unlock()
				case "cancel":
					a.after = cancel
				case "unavailable":
					cat := h.cat.Current()
					delete(cat.Models, "opus")
					h.cat.Set(cat)
				}
				before := len(h.sink.Decisions())
				_, ok := h.engine.Decide(ctx, mixedRequest(t, "Operate the service", "Investigate the result"), meta("th", "t2"))
				if refusal == "cancel" {
					if ok || len(h.sink.Decisions()) != before {
						t.Fatal("cancelled nomination committed")
					}
				} else if refusal == "unavailable" {
					if d := h.last(); d.Route.Model == "opus" {
						t.Fatalf("unavailable floor selected: %+v", d)
					}
				} else if d := h.last(); d.Route != want {
					t.Fatalf("%s bypassed for mode %s: %+v", refusal, mode, d)
				}
			})
		}
	}
}
