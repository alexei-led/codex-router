package route

import (
	"testing"

	"github.com/alexei-led/codex-router/internal/classifier"
	"github.com/alexei-led/codex-router/internal/config"
)

func TestOpsUnorderedExplicitFloorCanNominateAContinuation(t *testing.T) {
	c := mixedOpsConfig()
	a := &typedAsker{adapter: classifier.SystemOne(), answers: []string{
		opsAnswer("high", "execute", "0.99", "0"),
		opsAnswer("high", "reason", "0.99", "0.99"),
	}}
	h := mixedOpsHarness(t, c, a)
	m := meta("th", "t1")
	h.decide(mixedRequest(t, "Operate the service"), m)
	ops := c.RouteFor(config.TierHigh, config.ActivityOps)
	h.reply(m, ops.Model, 10000, 0)
	h.decide(mixedRequest(t, "Operate the service", "Investigate the unexpected result"), meta("th", "t2"))
	if d := h.last(); d.Route != c.RouteFor(config.TierHigh, config.ActivityDebug) ||
		d.Reading.Activity != config.ActivityOps || d.Reading.Phase != classifier.PhaseReason {
		t.Fatalf("explicit reasoning preference was not nominated through the gate: %+v", d)
	}
}

func TestOpsOrderedFloorCanUpgradeAContinuation(t *testing.T) {
	c := mixedOpsConfig()
	c.Routes[config.TierMedium] = c.RouteFor(config.TierHigh, config.ActivityOps)
	a := &typedAsker{adapter: classifier.SystemOne(), answers: []string{
		opsAnswer("high", "execute", "0.99", "0"),
		opsAnswer("high", "reason", "0.99", "0.99"),
	}}
	h := mixedOpsHarness(t, c, a)
	m := meta("th", "t1")
	h.decide(mixedRequest(t, "Operate the service"), m)
	h.reply(m, "sonnet", 10000, 0)
	h.decide(mixedRequest(t, "Operate the service", "Investigate the unexpected result"), meta("th", "t2"))
	if d := h.last(); d.Route != c.RouteFor(config.TierHigh, config.ActivityDebug) || d.EffectiveActivity != config.ActivityDebug {
		t.Fatalf("declared ordered reasoning floor did not pass existing gate: %+v", d)
	}
}
