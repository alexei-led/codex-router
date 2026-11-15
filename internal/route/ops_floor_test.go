package route

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/alexei-led/codex-router/internal/classifier"
	"github.com/alexei-led/codex-router/internal/config"
	"github.com/alexei-led/codex-router/internal/responses"
)

// These are synthetic compatible catalog entries, not claims that Codex can
// route to these providers. The weaker generic row reproduces a mixed profile.
func mixedOpsConfig() config.Config {
	c := testConfig()
	c.Routes = map[config.Tier]config.Route{
		config.TierMicro:  {Model: "glm-flash", Effort: config.EffortLow},
		config.TierLow:    {Model: "glm-flash", Effort: config.EffortHigh},
		config.TierMedium: {Model: "glm-flash", Effort: config.EffortHigh},
		config.TierHigh:   {Model: "opus", Effort: config.EffortHigh},
	}
	c.Activities = map[config.Activity]map[config.Tier]config.Route{
		config.ActivityOps: {
			config.TierMedium: {Model: "deepseek", Effort: config.EffortHigh},
			config.TierHigh:   {Model: "sonnet", Effort: config.EffortMedium},
		},
		config.ActivityDebug: {
			config.TierMedium: {Model: "deepseek", Effort: config.EffortHigh},
			config.TierHigh:   {Model: "opus", Effort: config.EffortHigh},
		},
		config.ActivityCode: {
			config.TierMedium: {Model: "deepseek", Effort: config.EffortHigh},
			config.TierHigh:   {Model: "opus", Effort: config.EffortHigh},
		},
	}
	return c
}

func mixedOpsHarness(t *testing.T, c config.Config, a *typedAsker) *harness {
	t.Helper()
	h := newHarness(t, c, func(d *Deps) { d.Classifier = a })
	cat := h.cat.Current()
	for _, name := range []string{"glm-flash", "deepseek", "sonnet", "opus"} {
		m := cat.Models[medium.Model]
		m.Slug = name
		cat.Models[name] = m
	}
	h.cat.Set(cat)
	return h
}

func opsAnswer(tier, phase, mass, continuation string) string {
	goal := `,"activity":{"type":"choice","choice":"ops","probabilities":{"ops":0.99}}`
	part := ""
	if phase != "" {
		part = fmt.Sprintf(`,"phase":{"type":"choice","choice":%q,"probabilities":{%q:%s}}`, phase, phase, mass)
	}
	return strings.Replace(systemAnswer(goal, part, continuation), `"choice":"medium","probabilities":{"medium":0.99}`,
		fmt.Sprintf(`"choice":%q,"probabilities":{%q:0.99}`, tier, tier), 1)
}

func mixedRequest(t *testing.T, prompts ...string) *responses.Request {
	t.Helper()
	var input []map[string]string
	for _, p := range prompts {
		input = append(input, map[string]string{"type": "message", "role": "user", "content": p})
	}
	body, err := json.Marshal(map[string]any{
		"model": "glm-flash", "reasoning": map[string]string{"effort": "low", "summary": "auto"},
		"input": input, "store": false,
	})
	if err != nil {
		t.Fatal(err)
	}
	r, err := responses.Parse(body)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestOpsPhaseGoldenRewrites(t *testing.T) {
	c := mixedOpsConfig()
	tests := []struct {
		name, tier, phase, mass string
		act                     config.Activity
		want                    config.Route
	}{
		{"bounded verify", "medium", "verify", "0.99", config.ActivityOps, c.RouteFor(config.TierMedium, config.ActivityOps)},
		{"bounded execute", "medium", "execute", "0.99", config.ActivityOps, c.RouteFor(config.TierMedium, config.ActivityOps)},
		{"reason preserves DeepSeek over generic GLM", "medium", "reason", "0.99", config.ActivityDebug, c.RouteFor(config.TierMedium, config.ActivityDebug)},
		{"produce uses implementation", "medium", "produce", "0.99", config.ActivityCode, c.RouteFor(config.TierMedium, config.ActivityCode)},
		{"hard reason uses Opus not bounded Sonnet", "high", "reason", "0.99", config.ActivityDebug, c.RouteFor(config.TierHigh, config.ActivityDebug)},
		{"hard produce uses Opus", "high", "produce", "0.99", config.ActivityCode, c.RouteFor(config.TierHigh, config.ActivityCode)},
		{"fresh report uses baseline", "medium", "report", "0.99", "", c.Routes[config.TierMedium]},
		{"fresh unknown uses baseline", "medium", "uncertain", "0.99", "", c.Routes[config.TierMedium]},
		{"missing phase uses baseline", "medium", "", "", "", c.Routes[config.TierMedium]},
		{"weak reason uses baseline", "medium", "reason", "0.2", "", c.Routes[config.TierMedium]},
		{"weak verify uses baseline", "medium", "verify", "0.2", "", c.Routes[config.TierMedium]},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			a := &typedAsker{adapter: classifier.SystemOne(), answers: []string{opsAnswer(tc.tier, tc.phase, tc.mass, "0")}}
			h := mixedOpsHarness(t, c, a)
			r := mixedRequest(t, "Deploy the release, diagnose failures and implement corrections; continue until done")
			rw, ok := h.decide(r, meta("th", "t1"))
			if !ok {
				t.Fatal("expected effort/model rewrite")
			}
			out, err := r.Apply(rw)
			if err != nil {
				t.Fatal(err)
			}
			var wire struct {
				Model     string
				Reasoning struct {
					Effort  config.Effort
					Summary string
				}
				Store bool
			}
			if err := json.Unmarshal(out, &wire); err != nil {
				t.Fatal(err)
			}
			if got := (config.Route{Model: wire.Model, Effort: wire.Reasoning.Effort}); got != tc.want {
				t.Fatalf("transmitted JSON route %v, want %v: %s", got, tc.want, out)
			}
			if wire.Reasoning.Summary != "auto" || wire.Store {
				t.Fatalf("other JSON changed: %s", out)
			}
			d := h.last()
			if d.EffectiveActivity != tc.act || d.Reading == nil || d.Reading.Activity != config.ActivityOps {
				t.Fatalf("goal/effective activity = %+v, want durable ops / %q", d, tc.act)
			}
		})
	}
}

func TestOpsUnknownContinuationKeepsEligibleIncumbent(t *testing.T) {
	for _, phase := range []string{"report", "uncertain", "", "reason"} {
		t.Run(phase, func(t *testing.T) {
			mass := "0.99"
			if phase == "reason" {
				mass = "0.2"
			}
			a := &typedAsker{adapter: classifier.SystemOne(), answers: []string{
				opsAnswer("medium", "reason", "0.99", "0"),
				opsAnswer("medium", phase, mass, "0.99"),
			}}
			c := mixedOpsConfig()
			h := mixedOpsHarness(t, c, a)
			m := meta("th", "t1")
			h.decide(mixedRequest(t, "Deploy the release"), m)
			want := c.RouteFor(config.TierMedium, config.ActivityDebug)
			h.reply(m, want.Model, 10000, 0)
			h.decide(mixedRequest(t, "Deploy the release", "Continue"), meta("th", "t2"))
			if d := h.last(); d.Route != want || d.EffectiveActivity != config.ActivityDebug {
				t.Fatalf("uncertainty caused cold/discounted hop: %+v", d)
			}
		})
	}
}

func TestOpsFloorRespectsPinAndCancellation(t *testing.T) {
	c := mixedOpsConfig()
	t.Run("pin", func(t *testing.T) {
		a := &typedAsker{adapter: classifier.SystemOne(), answers: []string{opsAnswer("high", "reason", "0.99", "0")}}
		h := mixedOpsHarness(t, c, a)
		h.apply(Command{Op: OpPin, Session: "th", Tier: config.TierLow})
		h.decide(mixedRequest(t, "Diagnose the rollout"), meta("th", "t1"))
		if d := h.last(); d.Route != c.Routes[config.TierLow] {
			t.Fatalf("floor bypassed pin: %+v", d)
		}
	})
	t.Run("cancel during classification", func(t *testing.T) {
		a := &typedAsker{adapter: classifier.SystemOne(), answers: []string{opsAnswer("high", "reason", "0.99", "0")}}
		h := mixedOpsHarness(t, c, a)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		a.after = cancel
		_, ok := h.engine.Decide(ctx, mixedRequest(t, "Diagnose the rollout"), meta("th", "t1"))
		if ok || len(h.sink.Decisions()) != 0 {
			t.Fatal("cancelled floor committed a decision")
		}
	})
}
