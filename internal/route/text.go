package route

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

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
	case policy.ReasonNoClassifier:
		return "classifier off; request keeps Codex's model"
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
		parts = append(parts, adviceSupport(d, a, labelOr(cfg.Classifiers[cfg.Classifier].Label)))
	}
	if d.Reason == policy.ReasonEscalation {
		parts = append(parts, "no classifier needed")
	}
	if tax > 0 {
		parts = append(parts, fmt.Sprintf("switch ≈ %s uncached tokens", kilo(tax)))
	}
	return strings.Join(parts, " · ")
}

// adviceSupport is the support the policy weighed against its bar, as
// claude-model-router words it: "Jev gave stronger tiers 88% (needs 82%)".
// A tier move weighs the mass above or at and below; an activity move
// weighs the activity's probability. Without a bar it names the answer.
func adviceSupport(d Decision, a *classifier.Advice, name string) string {
	est := d.Estimate
	if est == nil || est.Threshold <= 0 {
		return fmt.Sprintf("%s gave %s %d%%", name, a.Choice, pct(a.Probabilities[a.Choice]))
	}
	var what string
	switch d.Reason {
	case policy.ReasonUpgrade, policy.ReasonJump, policy.ReasonUpgradePending:
		what = "stronger tiers"
	case policy.ReasonDowngrade, policy.ReasonDowngradeHeld, policy.ReasonDowngradePending:
		what = "cheaper tiers"
	default:
		what = string(d.Activity)
		if a.Activity != nil {
			what = string(a.Activity.Choice)
		}
	}
	return fmt.Sprintf("%s gave %s %d%% (needs %d%%)", name, what, pct(est.Mass), pct(est.Threshold))
}

func labelOr(label string) string {
	if label == "" {
		return "classifier"
	}
	return label
}

// Thresholds of the cache finding: reuse below lowReusePct after a switch,
// once insightReplies replies give the figure weight (claude-model-router
// INSIGHT_REPLIES).
const (
	insightReplies = 20
	lowReusePct    = 70
)

// worth lists the session facts worth a look, most urgent first. Ported from
// claude-model-router's WORTH A LOOK where its rules map to a Codex session;
// its cost-share rules need per-cell prices and cross-session records.
func worth(s Snapshot) []Finding {
	var out []Finding
	add := func(k FindingKind, format string, args ...any) {
		out = append(out, Finding{Kind: k, Text: fmt.Sprintf(format, args...)})
	}
	if s.StateLost {
		add(FindingState, "state lost: saved pause, pin and preset unreadable; routing paused until resume")
	}
	name, h := labelOr(s.ClassifierLabel), s.Classifier
	switch {
	case h.PausedUntil.After(s.At):
		add(FindingClassifier, "%s paused until %s: turns go unrouted", name, h.PausedUntil.Format(time.TimeOnly))
	case h.Failures > 0:
		add(FindingClassifier, "%s failed %s in a row: a failed turn goes unrouted", name, plural(h.Failures, "call", "calls"))
	}
	if s.Mode == ModeManual {
		add(FindingManual, "footer picked with /model: routing holds %s until resume", routeName(s.Footer))
	}
	if n := s.Stats.FitHeld; n > 0 {
		add(FindingContext, "context guard kept %s on a larger window than the policy chose", plural(n, "step", "steps"))
	}
	if len(s.Blocked) > 0 {
		add(FindingBlocked, "routes to %s never run: outside %s's compatibility group", strings.Join(s.Blocked, ", "), s.Baseline.Model)
	}
	if st := s.Stats; st.Replies >= insightReplies && st.Switches > 0 && st.CacheReusePct < lowReusePct {
		add(FindingCache, "cache reuse %d%% after %s; switches wrote %s uncached tokens",
			st.CacheReusePct, plural(st.Switches, "switch", "switches"), kilo(st.UncachedSwitchTokens))
	}
	return out
}

func routeName(r config.Route) string {
	if r.Effort == "" {
		return r.Model
	}
	return r.Model + " · " + string(r.Effort)
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}

func pct(f float64) int { return int(math.Round(f * 100)) }

func kilo(n int) string {
	if n < 1000 {
		return strconv.Itoa(n)
	}
	return fmt.Sprintf("%dk", int(math.Round(float64(n)/1000)))
}
