package policy

import (
	"github.com/alexei-led/codex-router/internal/classifier"
	"github.com/alexei-led/codex-router/internal/config"
)

// Vote is one classifier answer counted toward a streak.
type Vote struct {
	Tier config.Tier
	Turn int
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
