package route

import (
	"testing"

	"github.com/alexei-led/codex-router/internal/catalog"
	"github.com/alexei-led/codex-router/internal/config"
	"github.com/alexei-led/codex-router/internal/responses"
)

func TestUnavailableModelIsNotReportedAsAContextOverflow(t *testing.T) {
	cfg := config.Default()
	cfg.Mode = config.ModeCost
	h := newHarness(t, cfg)
	c := h.cat.Current()
	delete(c.Models, luna)
	h.cat.Set(c)
	h.asker.script(answer(tier("low", 1)))
	native := cfg.Routes[cfg.Baseline]
	h.decide(req(native, userMsg(secretPrompt)), meta("th", "turn"))
	if d := h.last(); d.Reason != ReasonUnavailable || d.Preferred == nil || d.Preferred.Model != luna {
		t.Fatalf("missing model diagnosis = %+v", d)
	}
}

func TestMappedPhysicalRecoveryKeepsItsFullDownGates(t *testing.T) {
	cfg := config.Default()
	cfg.Mode = config.ModeCost
	cfg.Policy.ReviewEveryRequests = 0
	h := newHarness(t, cfg)
	c := h.cat.Current()
	m := c.Models[sol]
	m.ReasoningLevels = []catalog.ReasoningLevel{{Effort: config.EffortMedium}, {Effort: config.EffortHigh}}
	c.Models[sol] = m
	h.cat.Set(c)
	h.asker.script(answer(withActivity(tier("high", 1), config.ActivityDebug, 1)))
	native := cfg.Routes[cfg.Baseline]
	items := repeatedCheck(nil, "r")
	h.decide(req(native, userMsg(secretPrompt)), meta("th", "turn"))
	h.decide(req(native, append([]responses.Item{userMsg(secretPrompt)}, items...)...), meta("th", "turn"))
	if d := h.last(); d.Route != (config.Route{Model: sol, Effort: config.EffortHigh}) || d.Reason != ReasonUnavailable {
		t.Fatalf("mapped recovery = %+v", d)
	}
	h.engine.mu.Lock()
	defer h.engine.mu.Unlock()
	if !h.engine.lookup("th").recovering {
		t.Fatal("mapped recovery lost its down gates")
	}
}
