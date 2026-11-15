package route

import (
	"testing"

	"github.com/alexei-led/codex-router/internal/classifier"
	"github.com/alexei-led/codex-router/internal/config"
)

func TestPhaseRoutingHasNoLocalKeywordTier(t *testing.T) {
	for _, prompt := range []string{
		"Only run go test ./... and report status",
		"Diagnose the production outage, redesign the service and implement all fixes",
		"Продолжай работу по принятому плану",
	} {
		a := &typedAsker{adapter: classifier.SystemOne(), answers: []string{opsAnswer("medium", "reason", "0.99", "0")}}
		c := mixedOpsConfig()
		h := mixedOpsHarness(t, c, a)
		h.decide(mixedRequest(t, prompt), meta("th", "t1"))
		if d := h.last(); d.Tier != config.TierMedium || d.Route != c.RouteFor(config.TierMedium, config.ActivityDebug) {
			t.Fatalf("prompt overrode typed evidence: %q: %+v", prompt, d)
		}
	}
}

func TestOpsBoundedPhaseStillWaitsForDwellAndCheck(t *testing.T) {
	c := testConfig()
	ops := config.Route{Model: medium.Model, Effort: config.EffortLow}
	c.Activities[config.ActivityOps] = map[config.Tier]config.Route{config.TierMedium: ops}
	a := &typedAsker{adapter: classifier.SystemOne(), answers: []string{
		opsAnswer("medium", "reason", "0.99", "0"),
		opsAnswer("medium", "verify", "0.99", "0.99"),
	}}
	h := newHarness(t, c, func(d *Deps) { d.Classifier = a })
	m := meta("th", "t1")
	h.decide(goalRequest(t, "Operate the service"), m)
	h.reply(m, debug.Model, 10000, 0)
	h.decide(goalRequest(t, "Operate the service", "Only verify now"), meta("th", "t2"))
	if d := h.last(); d.Route != debug || d.EffectiveActivity != config.ActivityDebug {
		t.Fatalf("bounded phase bypassed dwell/check: %+v", d)
	}
}
