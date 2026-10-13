package route

import (
	"fmt"
	"math"
	"strings"

	"github.com/alexei-led/codex-router/internal/classifier"
	"github.com/alexei-led/codex-router/internal/config"
	"github.com/alexei-led/codex-router/internal/policy"
)

// whyText is the one-line reason the TUI and the hook show.
func whyText(d Decision, cfg config.Config, tokens int) string {
	route := d.Route.Model + " · " + string(d.Route.Effort)
	switch d.Reason {
	case policy.ReasonPinned:
		return "pinned to " + string(d.Tier) + " for this turn"
	case policy.ReasonEscalation:
		return "escalate: same tool failure twice, edit in between"
	case policy.ReasonHold:
		return fmt.Sprintf("hold: escalation holds %d turns", cfg.Effective().EscalationHoldTurns)
	case policy.ReasonNoAdvice:
		return "no classifier answer; keeps " + string(d.Tier)
	case policy.ReasonUncertain:
		return "classifier unsure; keeps " + string(d.Tier)
	case policy.ReasonSameTier:
		return "classifier agrees with " + string(d.Tier)
	case policy.ReasonUpgrade, policy.ReasonJump:
		return "up to " + string(d.Tier)
	case policy.ReasonUpgradePending:
		return "waiting to go up: needs more support"
	case policy.ReasonDowngrade:
		return "down to " + string(d.Tier)
	case policy.ReasonDowngradeHeld:
		return "waiting to go down: same task, no passed check since the last switch"
	case policy.ReasonDowngradePending:
		return "waiting to go down: needs more support"
	case policy.ReasonFit:
		return fmt.Sprintf("context fit: ~%s tokens need a larger window", kilo(tokens))
	case policy.ReasonOutOfGroup:
		return "route outside " + d.Original.Model + "'s compatibility group; request kept"
	case policy.ReasonError:
		return "classifier failed; this turn is not routed"
	case ReasonActivityUp, ReasonActivityDown:
		return fmt.Sprintf("%s at %s runs on %s", d.Activity, d.Tier, route)
	case ReasonActivityPending:
		return "activity route waits: switch not worth it yet"
	case ReasonObserved:
		return fmt.Sprintf("tool calls show %s; runs on %s", d.Activity, route)
	case ReasonContinue:
		return "same turn"
	}
	return string(d.Reason)
}

// supportText is the evidence line: the support of the classifier answer
// this step used, against its bar, and the switch's uncached tokens.
func supportText(d Decision, a *classifier.Advice, cfg config.Config, tax int) string {
	var parts []string
	if a != nil {
		name := cfg.Classifiers[cfg.Classifier].Label
		if name == "" {
			name = "classifier"
		}
		part := fmt.Sprintf("%s gave %s %d%%", name, a.Choice, pct(a.Probabilities[a.Choice]))
		if d.Estimate != nil && d.Estimate.Threshold > 0 {
			part += fmt.Sprintf(" (needs %d%%)", pct(d.Estimate.Threshold))
		}
		parts = append(parts, part)
	}
	if d.Reason == policy.ReasonEscalation {
		parts = append(parts, "no classifier needed")
	}
	if tax > 0 {
		parts = append(parts, fmt.Sprintf("switch ≈ %s uncached tokens", kilo(tax)))
	}
	return strings.Join(parts, " · ")
}

func pct(f float64) int { return int(math.Round(f * 100)) }

func kilo(n int) string {
	if n < 1000 {
		return fmt.Sprint(n)
	}
	return fmt.Sprintf("%dk", int(math.Round(float64(n)/1000)))
}
