package route

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/alexei-led/codex-router/internal/classifier"
	"github.com/alexei-led/codex-router/internal/config"
	"github.com/alexei-led/codex-router/internal/responses"
)

func projectionRequest(t *testing.T, prompts []string, progress string) *responses.Request {
	t.Helper()
	var items []map[string]string
	for _, prompt := range prompts {
		items = append(items, map[string]string{"type": "message", "role": "user", "content": prompt})
	}
	if progress != "" {
		items = append(items, map[string]string{"type": "message", "role": "assistant", "content": progress})
	}
	body, err := json.Marshal(map[string]any{"model": "glm-flash", "reasoning": map[string]string{"effort": "low"}, "input": items})
	if err != nil {
		t.Fatal(err)
	}
	r, err := responses.Parse(body)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestRefusedNominationRestoresAnExistingSemanticFloor(t *testing.T) {
	for _, mode := range config.RoutingModes {
		t.Run(string(mode), func(t *testing.T) {
			c := mixedOpsConfig()
			c.Mode, c.Policy.ReviewEveryRequests = mode, 0
			delete(c.Activities[config.ActivityDebug], config.TierMedium)
			c.Activities[config.ActivityCode][config.TierMedium] = config.Route{Model: "sonnet", Effort: config.EffortHigh}
			c.Activities[config.ActivityCode][config.TierHigh] = c.Routes[config.TierMedium]
			a := &typedAsker{adapter: classifier.SystemOne(), answers: []string{
				opsAnswer("medium", "reason", "0.99", "0"),
				opsAnswer("medium", "produce", "0.6", "0.99"),
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
			if d := h.last(); d.Route != want || d.EffectiveActivity != config.ActivityOps {
				t.Fatalf("refusal did not retain incumbent: %+v", d)
			}
			h.decide(projectionRequest(t, prompts, strings.Repeat("source context ", 3000)), m)
			if d := h.last(); d.Route.Model != "opus" || d.EffectiveActivity != config.ActivityDebug {
				t.Fatalf("incumbent reasoning provenance was not restored: %+v", d)
			}
		})
	}
}

func TestAcceptedBoundedPhaseReplacesAnEqualPhysicalFloor(t *testing.T) {
	c := mixedOpsConfig()
	c.Policy.ReviewEveryRequests = 0
	delete(c.Activities[config.ActivityDebug], config.TierMedium)
	a := &typedAsker{adapter: classifier.SystemOne(), answers: []string{
		opsAnswer("medium", "reason", "0.99", "0"),
		opsAnswer("medium", "verify", "0.99", "0.99"),
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
	h.reply(m, "deepseek", 1000, 0)
	prompts = append(prompts, "Only run the bounded checks")
	m = meta("th", "t2")
	h.decide(projectionRequest(t, prompts, ""), m)
	h.decide(projectionRequest(t, prompts, strings.Repeat("source context ", 3000)), m)
	if d := h.last(); d.Route.Model != "sonnet" || d.EffectiveActivity != config.ActivityOps {
		t.Fatalf("accepted bounded phase retained previous reasoning intent: %+v", d)
	}
}

func TestModeFenceClearsThePreviousOpsProjection(t *testing.T) {
	c := mixedOpsConfig()
	c.Policy.ReviewEveryRequests = 0
	delete(c.Activities[config.ActivityDebug], config.TierMedium)
	a := &typedAsker{adapter: classifier.SystemOne(), answers: []string{opsAnswer("medium", "reason", "0.99", "0")}}
	h := mixedOpsHarness(t, c, a)
	cat := h.cat.Current()
	model := cat.Models["deepseek"]
	model.ContextWindow = 3000
	cat.Models[model.Slug] = model
	h.cat.Set(cat)
	m := meta("th", "t1")
	prompts := []string{"Operate the service"}
	h.decide(projectionRequest(t, prompts, ""), m)
	h.reply(m, "deepseek", 1000, 0)
	h.apply(Command{Op: OpMode, Session: "th", Mode: config.ModeQuality})
	h.decide(projectionRequest(t, prompts, strings.Repeat("source context ", 3000)), m)
	if d := h.last(); d.Route.Model != "sonnet" || d.EffectiveActivity != config.ActivityOps {
		t.Fatalf("mode fence reused old semantic projection: %+v", d)
	}
}
