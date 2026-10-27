package route

import (
	"testing"

	"github.com/alexei-led/codex-router/internal/classifier"
	"github.com/alexei-led/codex-router/internal/config"
	"github.com/alexei-led/codex-router/internal/responses"
)

func TestBalancedDefaultActivitiesUseActivityGates(t *testing.T) {
	for _, tc := range []struct {
		name   string
		first  classifier.Advice
		next   classifier.Advice
		want   config.Route
		reason string
	}{
		{"uncertain tier confident debug", mediumTier, withActivity(classifier.Advice{Choice: "uncertain", Probabilities: map[classifier.Choice]float64{"uncertain": 1}}, config.ActivityDebug, 1), config.Route{Model: terra, Effort: config.EffortXHigh}, string(ReasonActivityUp)},
		{"new explore task after code", withActivity(mediumTier, config.ActivityCode, 1), withActivity(mediumTier, config.ActivityExplore, 1), config.Route{Model: terra, Effort: config.EffortMedium}, string(ReasonActivityDown)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := config.Default()
			cfg.Policy.ReviewEveryRequests = 0
			h := newHarness(t, cfg)
			h.asker.script(answer(tc.first), answer(tc.next))
			native := cfg.Routes[cfg.Baseline]
			items := []responses.Item{userMsg(secretPrompt)}
			h.decide(req(native, items...), meta("th", "t1"))
			running := h.last().Route
			h.reply(meta("th", "t1"), running.Model, 10000, 0)
			rw, ok := h.decide(req(running, append(items, userMsg("next task"))...), meta("th", "t2"))
			wantRoute(t, rw, ok, tc.want)
			if d := h.last(); d.Route != tc.want || string(d.Reason) != tc.reason {
				t.Fatalf("activity gate=%+v, want %v (%s)", d, tc.want, tc.reason)
			}
		})
	}
}
