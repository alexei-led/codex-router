package policy

import (
	"slices"

	"github.com/alexei-led/codex-router/internal/classifier"
	"github.com/alexei-led/codex-router/internal/config"
)

// Vote is one classifier answer counted toward a streak.
type Vote struct {
	Tier config.Tier
}

// State is carried from one decision to the next for one thread.
type State struct {
	Turn               int
	Votes              []Vote
	HoldUntilTurn      int
	EscalatedSignature string
}

// Reason names why a decision landed where it did.
type Reason string

const (
	ReasonEscalation       Reason = "escalation"
	ReasonHold             Reason = "hold"
	ReasonNoAdvice         Reason = "no-advice"
	ReasonUncertain        Reason = "uncertain"
	ReasonSameTier         Reason = "same-tier"
	ReasonUpgrade          Reason = "upgrade"
	ReasonJump             Reason = "jump"
	ReasonUpgradePending   Reason = "upgrade-pending"
	ReasonDowngrade        Reason = "downgrade"
	ReasonDowngradeHeld    Reason = "downgrade-held"
	ReasonDowngradePending Reason = "downgrade-pending"
	ReasonPinned           Reason = "pinned"
	ReasonPaused           Reason = "paused"
	ReasonManual           Reason = "manual"
	ReasonFit              Reason = "fit"
	ReasonOutOfGroup       Reason = "out-of-group"
	ReasonPassthrough      Reason = "passthrough"
	ReasonError            Reason = "error"
)

// Estimate explains a switch bar: the tax it priced, the bar, the support
// and the vote streak.
type Estimate struct {
	TaxTokens int
	Threshold float64
	Mass      float64
	Streak    int
}

// Input is one decision's facts. TaxTokens prices a candidate tier's next
// request in uncached tokens; Escalate names the tier a repeated failure moves
// to, or false when none helps.
type Input struct {
	Policy           config.Policy
	Baseline         config.Tier
	Incumbent        config.Tier
	Advice           *classifier.Advice
	FailureSignature string
	MidTurn          bool
	DownAllowed      bool
	Fresh            bool
	State            State
	TaxTokens        func(candidate config.Tier) int
	Escalate         func(incumbent config.Tier) (config.Tier, bool)
}

// Result is the decided tier with its reason, the next State and the
// estimate behind a bar, when one was priced.
type Result struct {
	Tier     config.Tier
	Reason   Reason
	State    State
	Estimate *Estimate
}

// VotesKept is how many trailing votes a State remembers.
const VotesKept = 3

// Decide returns the tier for one step. The incumbent is in.Incumbent, or
// in.Baseline when that is not a tier. A repeated failure signature escalates
// once per signature and holds; otherwise the advice votes, and a switch must
// clear a bar that rises with the candidate's tax. A cheaper tier on the same
// task also waits for in.DownAllowed.
//
// Unlike the Claude router there is no cash gate: Codex bills the plan's
// limits, and a downgrade tax has no later-turn savings in tokens, so the
// downgrade bar prices only the next request.
func Decide(in Input) Result {
	p := in.Policy
	next := in.State
	next.Turn++
	next.Votes = append([]Vote(nil), in.State.Votes...)

	incumbent := in.Incumbent
	if rank(incumbent) < 0 {
		incumbent = in.Baseline
	}
	stay := func(reason Reason, est *Estimate) Result {
		return Result{Tier: incumbent, Reason: reason, State: next, Estimate: est}
	}

	if in.FailureSignature != "" && in.FailureSignature != next.EscalatedSignature {
		escalate := in.Escalate
		if escalate == nil {
			escalate = oneUp
		}
		if target, ok := escalate(incumbent); ok {
			next.EscalatedSignature = in.FailureSignature
			next.HoldUntilTurn = next.Turn + p.EscalationHoldTurns
			next.Votes = nil
			return Result{Tier: target, Reason: ReasonEscalation, State: next}
		}
	}
	if next.Turn <= next.HoldUntilTurn {
		return stay(ReasonHold, nil)
	}
	if in.Advice == nil {
		return stay(ReasonNoAdvice, nil)
	}
	choice := config.Tier(in.Advice.Choice)
	if rank(choice) < 0 {
		return stay(ReasonUncertain, nil)
	}

	next.Votes = append(next.Votes, Vote{Tier: choice})
	if len(next.Votes) > VotesKept {
		next.Votes = next.Votes[len(next.Votes)-VotesKept:]
	}
	if rank(choice) == rank(incumbent) {
		return stay(ReasonSameTier, nil)
	}
	// Before the first measured reply there is no cache to protect, so one
	// vote is enough.
	upgradeVotes, downgradeVotes := p.UpgradeVotes, p.DowngradeVotes
	if in.Fresh {
		upgradeVotes, downgradeVotes = 1, 1
	}
	probs := in.Advice.Probabilities

	if rank(choice) > rank(incumbent) {
		tax := taxOf(in.TaxTokens, choice)
		est := &Estimate{
			TaxTokens: tax,
			Threshold: UpgradeBar(p, tax),
			Mass:      massAbove(probs, incumbent),
			Streak:    trailing(next.Votes, func(v Vote) bool { return rank(v.Tier) > rank(incumbent) }),
		}
		if rank(choice)-rank(incumbent) >= 2 && est.Mass >= p.JumpConfidence {
			return Result{Tier: choice, Reason: ReasonJump, State: next, Estimate: est}
		}
		if est.Streak >= upgradeVotes && est.Mass >= est.Threshold {
			return Result{Tier: choice, Reason: ReasonUpgrade, State: next, Estimate: est}
		}
		return stay(ReasonUpgradePending, est)
	}

	// The move lands on the cheapest tier the answer supports as a whole: from
	// the choice up to just below the incumbent, the first tier whose mass at or
	// below it clears its bar. A pending stay reports the choice's estimate.
	var pending *Estimate
	for _, target := range config.Tiers[rank(choice):rank(incumbent)] {
		tax := taxOf(in.TaxTokens, target)
		est := &Estimate{
			TaxTokens: tax,
			Threshold: p.DowngradeMass + p.DowngradeSlope*share(tax, p.DowngradePivotTokens),
			Mass:      massAtOrBelow(probs, target),
			Streak:    trailing(next.Votes, func(v Vote) bool { return rank(v.Tier) <= rank(target) }),
		}
		if pending == nil {
			pending = est
		}
		if est.Mass < est.Threshold || est.Streak < downgradeVotes {
			continue
		}
		if SameTask(p, in.Advice, in.MidTurn) && !in.DownAllowed {
			return stay(ReasonDowngradeHeld, est)
		}
		return Result{Tier: target, Reason: ReasonDowngrade, State: next, Estimate: est}
	}
	return stay(ReasonDowngradePending, pending)
}

// FitTier returns the lowest tier at or above tier whose window holds tokens
// at fill. Routes need not grow in window with rank, so the search falls back
// to any tier that fits from the bottom. When none fits it returns the tier
// with the largest known window (tier itself on a tie or when no window is
// known) and false. An unknown window never fits.
func FitTier(tier config.Tier, tokens int, window func(config.Tier) (int, bool), fill float64) (config.Tier, bool) {
	fits := func(t config.Tier) bool {
		w, ok := window(t)
		return ok && w > 0 && float64(tokens) <= float64(w)*fill
	}
	if fits(tier) {
		return tier, true
	}
	if r := rank(tier); r >= 0 {
		for _, t := range config.Tiers[r:] {
			if fits(t) {
				return t, true
			}
		}
	}
	for _, t := range config.Tiers {
		if fits(t) {
			return t, true
		}
	}
	best := tier
	bestWindow, _ := window(tier)
	for _, t := range config.Tiers {
		if w, ok := window(t); ok && w > bestWindow {
			best, bestWindow = t, w
		}
	}
	return best, false
}

// SameTask reports whether the answer is about the task already running: a
// review inside a turn, or a new prompt scored as a continuation.
func SameTask(p config.Policy, a *classifier.Advice, midTurn bool) bool {
	return midTurn || (a.Continuation != nil && *a.Continuation >= p.ContinuationMass)
}

func oneUp(t config.Tier) (config.Tier, bool) {
	r := rank(t)
	if r < 0 || r+1 >= len(config.Tiers) {
		return "", false
	}
	return config.Tiers[r+1], true
}

func taxOf(f func(config.Tier) int, t config.Tier) int {
	if f == nil {
		return 0
	}
	return max(0, f(t))
}

// UpgradeBar is the probability mass an upgrade needs when it costs tax
// tokens of cache: the base, plus the slope scaled by the tax share.
func UpgradeBar(p config.Policy, tax int) float64 {
	return p.UpgradeBase + p.UpgradeSlope*share(tax, p.UpgradePivotTokens)
}

// share is tax/(tax+pivot): 0 for no tax, half the slope at the pivot.
func share(tax, pivot int) float64 {
	if tax <= 0 {
		return 0
	}
	return float64(tax) / float64(tax+max(0, pivot))
}

func trailing(votes []Vote, pred func(Vote) bool) int {
	n := 0
	for i := len(votes) - 1; i >= 0 && pred(votes[i]); i-- {
		n++
	}
	return n
}

func rank(t config.Tier) int {
	return slices.Index(config.Tiers, t)
}

// massAbove is the probability mass strictly above tier; uncertain supports
// neither direction.
func massAbove(probs map[classifier.Choice]float64, tier config.Tier) float64 {
	sum := 0.0
	for _, t := range config.Tiers[rank(tier)+1:] {
		sum += probs[classifier.Choice(t)]
	}
	return sum
}

func massAtOrBelow(probs map[classifier.Choice]float64, tier config.Tier) float64 {
	sum := 0.0
	for _, t := range config.Tiers[:rank(tier)+1] {
		sum += probs[classifier.Choice(t)]
	}
	return sum
}
