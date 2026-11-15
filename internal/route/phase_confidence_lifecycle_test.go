package route

import (
	"strings"
	"testing"

	"github.com/alexei-led/codex-router/internal/classifier"
	"github.com/alexei-led/codex-router/internal/config"
)

func TestOpsPhaseDoesNotReplaceAnOperatorModeGate(t *testing.T) {
	c := mixedOpsConfig()
	c.Policy.ReviewEveryRequests = 0
	c.Activities[config.ActivityDebug][config.TierMedium] = c.Routes[config.TierMedium]
	a := &typedAsker{adapter: classifier.SystemOne(), answers: []string{
		opsAnswer("medium", "reason", "0.99", "0"),
		opsAnswer("medium", "reason", "0.61", "0.99"),
	}}
	h := mixedOpsHarness(t, c, a)
	m := meta("th", "t1")
	h.decide(mixedRequest(t, "Operate the service"), m)
	h.reply(m, h.last().Route.Model, 10000, 0)
	h.apply(Command{Op: OpMode, Session: "th", Mode: config.ModeQuality})
	h.decide(mixedRequest(t, "Operate the service", "Continue"), meta("th", "t2"))
	want := config.Route{Model: "glm-flash", Effort: config.EffortXHigh}
	if d := h.last(); d.Route != want || d.Reason != ReasonMode {
		t.Fatalf("pure operator mode transform acquired a new phase bar: %+v", d)
	}
}

func TestAcceptedOpsFloorKeepsItsContinuationRefit(t *testing.T) {
	c := mixedOpsConfig()
	c.Mode, c.Policy.ReviewEveryRequests = config.ModeCost, 0
	c.Routes[config.TierHigh] = c.RouteFor(config.TierMedium, config.ActivityOps)
	delete(c.Activities[config.ActivityDebug], config.TierMedium)
	a := &typedAsker{adapter: classifier.SystemOne(), answers: []string{
		opsAnswer("medium", "reason", "0.99", "0"),
		opsAnswer("medium", "reason", "0.65", "0.99"),
	}}
	h := mixedOpsHarness(t, c, a)
	cat := h.cat.Current()
	model := cat.Models["deepseek"]
	model.ContextWindow = 3000
	cat.Models[model.Slug] = model
	h.cat.Set(cat)
	prompts := []string{"Operate the service"}
	m := meta("th", "t1")
	h.decide(projectionRequest(t, prompts, ""), m)
	want := h.last().Route
	h.reply(m, want.Model, 1000, 0)
	prompts = append(prompts, "Continue the operation")
	m = meta("th", "t2")
	h.decide(projectionRequest(t, prompts, ""), m)
	if d := h.last(); d.Route != want {
		t.Fatalf("accepted incumbent was re-gated: %+v", d)
	}
	h.decide(projectionRequest(t, prompts, strings.Repeat("source context ", 3000)), m)
	if d := h.last(); d.Route != (config.Route{Model: "opus", Effort: config.EffortMedium}) || d.EffectiveActivity != config.ActivityDebug {
		t.Fatalf("accepted semantic floor lost its valid context refit: %+v", d)
	}
}
