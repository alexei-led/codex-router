package route

import (
	"context"
	"testing"

	"github.com/alexei-led/codex-router/internal/catalog"
	"github.com/alexei-led/codex-router/internal/classifier"
	"github.com/alexei-led/codex-router/internal/config"
	"github.com/alexei-led/codex-router/internal/policy"
	"github.com/alexei-led/codex-router/internal/responses"
)

func TestPhysicalRecoveryRequiresAnEligibleStrongerPair(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*catalog.Catalog)
	}{
		{"missing model", func(c *catalog.Catalog) { delete(c.Models, sol) }},
		{"missing group", func(c *catalog.Catalog) { m := c.Models[sol]; m.CompHash = ""; c.Models[sol] = m }},
		{"unequal group", func(c *catalog.Catalog) { m := c.Models[sol]; m.CompHash = "other"; c.Models[sol] = m }},
		{"unsupported effort", func(c *catalog.Catalog) {
			m := c.Models[sol]
			m.ReasoningLevels = []catalog.ReasoningLevel{{Effort: config.EffortLow}}
			c.Models[sol] = m
		}},
		{"context", func(c *catalog.Catalog) { m := c.Models[sol]; m.ContextWindow = 10; c.Models[sol] = m }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := config.Default()
			cfg.Mode = config.ModeCost
			cfg.Policy.ReviewEveryRequests = 0
			h := newHarness(t, cfg)
			h.asker.script(answer(withActivity(tier("high", 1), config.ActivityDebug, 1)))
			native := cfg.Routes[cfg.Baseline]
			items := []responses.Item{userMsg(secretPrompt)}
			m := meta("th", "turn")
			h.decide(req(native, items...), m)
			h.reply(m, terra, 10000, 0)
			c := h.cat.Current()
			tc.mutate(&c)
			h.cat.Set(c)
			h.decide(req(native, repeatedCheck(items, "r")...), m)
			if d := h.last(); d.Route.Model == sol || d.Reason == policy.ReasonEscalation {
				t.Fatalf("ineligible recovery = %+v", d)
			}
			h.engine.mu.Lock()
			state := h.engine.lookup("th").state
			h.engine.mu.Unlock()
			if state.EscalatedSignature != "" {
				t.Fatal("ineligible target consumed recovery signature")
			}
		})
	}
}

func TestRecoveryCannotOverrideUserIntentOrCancellation(t *testing.T) {
	for _, intent := range []string{"pause", "manual", "pin", "mode", "cancel"} {
		t.Run(intent, func(t *testing.T) {
			cfg := config.Default()
			cfg.Mode = config.ModeCost
			cfg.Policy.ReviewEveryRequests = 0
			h := newHarness(t, cfg)
			h.asker.script(answer(withActivity(tier("high", 1), config.ActivityDebug, 1)))
			native := cfg.Routes[cfg.Baseline]
			items := []responses.Item{userMsg(secretPrompt)}
			m := meta("th", "turn")
			h.decide(req(native, items...), m)
			before := len(h.sink.Decisions())
			ctx := context.Background()
			switch intent {
			case "pause":
				h.apply(Command{Op: OpPause, Session: "th"})
			case "manual":
				h.engine.SettingsUpdated("th", config.Route{Model: luna, Effort: config.EffortLow})
			case "pin":
				h.apply(Command{Op: OpPin, Session: "th", Tier: config.TierLow})
				m = meta("th", "pinned")
			case "mode":
				h.apply(Command{Op: OpMode, Session: "th", Mode: config.ModeQuality})
			case "cancel":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			_, _ = h.engine.Decide(ctx, req(native, repeatedCheck(items, "r")...), m)
			ds := h.sink.Decisions()
			for _, d := range ds[before:] {
				if d.Reason == policy.ReasonEscalation {
					t.Fatalf("recovery overrode %s: %+v", intent, d)
				}
			}
			if intent == "pin" {
				if d := h.last(); d.Reason != policy.ReasonPinned || d.Route != cfg.Routes[config.TierLow] {
					t.Fatalf("pin = %+v", d)
				}
			}
		})
	}
}

func TestNewModeEpochDoesNotReuseEarlierFailures(t *testing.T) {
	cfg := config.Default()
	cfg.Mode = config.ModeCost
	cfg.Policy.ReviewEveryRequests = 0
	h := newHarness(t, cfg)
	h.asker.script(answer(classifier.Advice{Choice: "high", Probabilities: map[classifier.Choice]float64{"high": 1}}))
	native := cfg.Routes[cfg.Baseline]
	items := []responses.Item{userMsg(secretPrompt)}
	m := meta("th", "turn")
	h.decide(req(native, items...), m)
	items = repeatedCheck(items, "r")
	h.decide(req(native, items...), m)
	h.apply(Command{Op: OpMode, Session: "th", Mode: config.ModeQuality})
	h.apply(Command{Op: OpMode, Session: "th", Mode: config.ModeCost})
	items = append(items, call("read", "ls"), result("read", 0, ""))
	h.decide(req(native, items...), m)
	if d := h.last(); d.Reason == policy.ReasonEscalation {
		t.Fatalf("new mode inherited failure: %+v", d)
	}
	h.engine.mu.Lock()
	defer h.engine.mu.Unlock()
	if h.engine.lookup("th").recovering {
		t.Fatal("mode epoch retained recovery")
	}
}
