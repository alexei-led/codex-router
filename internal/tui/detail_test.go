package tui

import (
	"testing"
	"time"

	"github.com/alexei-led/codex-router/internal/config"
	"github.com/alexei-led/codex-router/internal/policy"
	"github.com/alexei-led/codex-router/internal/route"
)

func decided(turn string, t config.Tier, r policy.Reason, minute int) route.Decision {
	return route.Decision{
		TurnID: turn, Tier: t, Route: config.Route{Model: "m-" + string(t)}, Reason: r,
		Why: string(r), At: time.Date(2026, 10, 15, 17, minute, 0, 0, time.UTC),
	}
}

func TestRecentGroups(t *testing.T) {
	upgrade := decided("t1", config.TierHigh, policy.ReasonUpgrade, 40)
	step := decided("t1", config.TierHigh, route.ReasonContinue, 41)
	escalated := decided("t1", config.TierHigh, policy.ReasonEscalation, 42)
	next := decided("t2", config.TierLow, policy.ReasonDowngrade, 43)
	cases := []struct {
		name   string
		recent []route.Decision
		n      int
		want   []recentGroup
	}{
		{"steps of a turn fold under its own reason", []route.Decision{upgrade, step, step, step}, 5,
			[]recentGroup{{decision: upgrade, steps: 4}}},
		{"newest first across turns", []route.Decision{upgrade, step, next}, 5,
			[]recentGroup{{decision: next, steps: 1}, {decision: upgrade, steps: 2}}},
		{"a route change inside a turn is its own row", []route.Decision{
			decided("t1", config.TierMedium, policy.ReasonSameTier, 40), escalated, step}, 5,
			[]recentGroup{{decision: escalated, steps: 2}, {decision: decided("t1", config.TierMedium, policy.ReasonSameTier, 40), steps: 1}}},
		{"capped at n groups", []route.Decision{upgrade, next, upgrade, next}, 2,
			[]recentGroup{{decision: next, steps: 1}, {decision: upgrade, steps: 1}}},
		{"no turn id never folds", []route.Decision{decided("", config.TierLow, policy.ReasonPinned, 40), decided("", config.TierLow, policy.ReasonPinned, 41)}, 5,
			[]recentGroup{{decision: decided("", config.TierLow, policy.ReasonPinned, 41), steps: 1}, {decision: decided("", config.TierLow, policy.ReasonPinned, 40), steps: 1}}},
		{"empty", nil, 5, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := recentGroups(tc.recent, tc.n)
			if len(got) != len(tc.want) {
				t.Fatalf("groups = %d, want %d: %+v", len(got), len(tc.want), got)
			}
			for i := range got {
				if got[i].steps != tc.want[i].steps || got[i].decision.Reason != tc.want[i].decision.Reason || got[i].decision.At != tc.want[i].decision.At {
					t.Errorf("group %d = %s ×%d at %s, want %s ×%d at %s", i,
						got[i].decision.Reason, got[i].steps, got[i].decision.At.Format("15:04"),
						tc.want[i].decision.Reason, tc.want[i].steps, tc.want[i].decision.At.Format("15:04"))
				}
			}
		})
	}
}

func TestTurnDecision(t *testing.T) {
	head := decided("t1", config.TierHigh, policy.ReasonDowngradePending, 40)
	head.Support = "cheaper tiers 62% (needs 93%)"
	step := decided("t1", config.TierHigh, route.ReasonContinue, 41)
	moved := decided("t1", config.TierMedium, route.ReasonContinue, 41)
	cases := []struct {
		name   string
		recent []route.Decision
		last   route.Decision
		want   policy.Reason
		why    string
	}{
		{"a later step shows its turn's reason", []route.Decision{head, step}, step, policy.ReasonDowngradePending, "cheaper tiers 62% (needs 93%)"},
		{"a turn's own decision stays", []route.Decision{head}, head, policy.ReasonDowngradePending, "cheaper tiers 62% (needs 93%)"},
		{"a step on another route keeps its own reason", []route.Decision{head, moved}, moved, route.ReasonContinue, ""},
		{"no head in recent keeps the step", []route.Decision{step}, step, route.ReasonContinue, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := turnDecision(tc.recent, tc.last)
			if got.Reason != tc.want || got.Support != tc.why {
				t.Errorf("turnDecision = %s %q, want %s %q", got.Reason, got.Support, tc.want, tc.why)
			}
			if got.At != tc.last.At || got.Route != tc.last.Route {
				t.Errorf("turnDecision changed the step's time or route: %+v", got)
			}
		})
	}
}
