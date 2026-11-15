package route

import (
	"testing"

	"github.com/alexei-led/codex-router/internal/classifier"
	"github.com/alexei-led/codex-router/internal/config"
)

func TestOpsFloorCannotRemapARejectedContinuationActivity(t *testing.T) {
	c := mixedOpsConfig()
	delete(c.Activities[config.ActivityDebug], config.TierMedium)
	goal := `,"activity":{"type":"choice","choice":"debug","probabilities":{"debug":0.99}}`
	a := &typedAsker{adapter: classifier.SystemOne(), answers: []string{
		systemAnswer(goal, "", "0"),
		opsAnswer("medium", "reason", "0.99", "0.99"),
	}}
	h := mixedOpsHarness(t, c, a)
	m := meta("th", "t1")
	h.decide(mixedRequest(t, "Diagnose a service"), m)
	h.reply(m, "glm-flash", 10000, 0)
	h.decide(mixedRequest(t, "Diagnose a service", "Continue the operation"), meta("th", "t2"))
	if d := h.last(); d.Route != c.Routes[config.TierMedium] {
		t.Fatalf("floor remapped a rejected unknown-order continuation: %+v", d)
	}
}

func TestOpsEqualPairFloorFollowsReasoningOnTierUpgrade(t *testing.T) {
	c := mixedOpsConfig()
	c.Routes[config.TierLow] = c.RouteFor(config.TierMedium, config.ActivityOps)
	a := &typedAsker{adapter: classifier.SystemOne(), answers: []string{
		opsAnswer("medium", "execute", "0.99", "0"),
		opsAnswer("high", "reason", "0.99", "0.99"),
		opsAnswer("high", "reason", "0.99", "0.99"),
	}}
	h := mixedOpsHarness(t, c, a)
	m := meta("th", "t1")
	h.decide(mixedRequest(t, "Operate the service"), m)
	h.reply(m, "deepseek", 10000, 0)
	h.decide(mixedRequest(t, "Operate the service", "Investigate a harder failure"), meta("th", "t2"))
	h.decide(mixedRequest(t, "Operate the service", "Investigate a harder failure", "Continue"), meta("th", "t3"))
	if d := h.last(); d.Route != c.RouteFor(config.TierHigh, config.ActivityDebug) {
		t.Fatalf("equal medium pair lost reasoning floor on tier upgrade: %+v", d)
	}
}
