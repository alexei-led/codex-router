package policy

import (
	"math"
	"slices"
	"testing"

	"github.com/alexei-led/codex-router/internal/classifier"
	"github.com/alexei-led/codex-router/internal/config"
)

var testPolicy = config.Policy{
	UpgradeVotes:         2,
	UpgradeBase:          0.75,
	UpgradeSlope:         0.15,
	UpgradePivotTokens:   100_000,
	JumpConfidence:       0.95,
	DowngradeVotes:       2,
	DowngradeMass:        0.9,
	DowngradeSlope:       0.08,
	DowngradePivotTokens: 100_000,
	ContinuationMass:     0.7,
	EscalationHoldTurns:  2,
}

type probs = map[classifier.Choice]float64

func advice(choice classifier.Choice, p probs) *classifier.Advice {
	return &classifier.Advice{Choice: choice, Probabilities: p}
}

func continuing(a *classifier.Advice, c float64) *classifier.Advice {
	a.Continuation = &c
	return a
}

func flatTax(tokens int) func(config.Tier) int {
	return func(config.Tier) int { return tokens }
}

type step struct {
	advice *classifier.Advice
	tier   config.Tier
	reason Reason
}

func input(incumbent config.Tier) Input {
	return Input{Policy: testPolicy, Baseline: config.TierLow, Incumbent: incumbent}
}

func run(t *testing.T, in Input, steps []step) Result {
	t.Helper()
	var r Result
	for i, s := range steps {
		in.Advice = s.advice
		r = Decide(in)
		if r.Tier != s.tier || r.Reason != s.reason {
			t.Fatalf("step %d: got %s/%s, want %s/%s", i, r.Tier, r.Reason, s.tier, s.reason)
		}
		in.State = r.State
	}
	return r
}

func TestDecide(t *testing.T) {
	medium90 := advice("medium", probs{"medium": 0.9, "low": 0.1})
	low95 := advice("low", probs{"low": 0.95, "high": 0.05})
	micro99 := advice("micro", probs{"micro": 0.99})
	sig := "error: tests failed"

	cases := []struct {
		name  string
		in    Input
		steps []step
	}{
		{"no advice keeps the baseline", input(""), []step{{nil, "low", ReasonNoAdvice}}},
		{"an unknown incumbent falls back to the baseline", input("huge"), []step{{nil, "low", ReasonNoAdvice}}},
		{
			"an unknown incumbent and baseline stay put without advice",
			Input{Policy: testPolicy, Baseline: "huge", Incumbent: "tiny"},
			[]step{{nil, "huge", ReasonNoAdvice}},
		},
		{
			"an unknown incumbent and baseline do not escalate",
			Input{Policy: testPolicy, Baseline: "huge", Incumbent: "tiny", FailureSignature: sig},
			[]step{{nil, "huge", ReasonNoAdvice}},
		},
		{
			"an unknown incumbent and baseline jump to a well-supported choice",
			Input{Policy: testPolicy, Baseline: "huge", Incumbent: "tiny"},
			[]step{{medium90, "medium", ReasonJump}},
		},
		{
			"an unknown incumbent and baseline wait on a weakly supported choice",
			Input{Policy: testPolicy, Baseline: "huge", Incumbent: "tiny"},
			[]step{{advice("micro", probs{"micro": 0.5, "uncertain": 0.5}), "huge", ReasonUpgradePending}},
		},
		{"uncertain keeps the incumbent", input("high"), []step{{advice("uncertain", probs{"uncertain": 1}), "high", ReasonUncertain}}},
		{"a choice outside the tiers counts as uncertain", input("medium"), []step{{advice("huge", probs{}), "medium", ReasonUncertain}}},
		{"the incumbent's own tier stays", input("medium"), []step{{advice("medium", probs{"medium": 1}), "medium", ReasonSameTier}}},
		{
			"a one-tier upgrade needs two votes",
			input("low"),
			[]step{{medium90, "low", ReasonUpgradePending}, {medium90, "medium", ReasonUpgrade}},
		},
		{
			"a weak upgrade mass never switches",
			input("low"),
			[]step{
				{advice("medium", probs{"medium": 0.6, "low": 0.4}), "low", ReasonUpgradePending},
				{advice("medium", probs{"medium": 0.6, "low": 0.4}), "low", ReasonUpgradePending},
			},
		},
		{
			"a two-tier jump with high mass switches at once",
			input("low"),
			[]step{{advice("high", probs{"high": 0.96}), "high", ReasonJump}},
		},
		{
			"a two-tier move below the jump mass waits for the streak",
			input("low"),
			[]step{
				{advice("high", probs{"high": 0.9, "low": 0.1}), "low", ReasonUpgradePending},
				{advice("high", probs{"high": 0.9, "low": 0.1}), "high", ReasonUpgrade},
			},
		},
		{
			"a large switch tax holds an upgrade the base bar passes",
			Input{Policy: testPolicy, Baseline: "low", Incumbent: "low", TaxTokens: flatTax(400_000)},
			[]step{
				{advice("medium", probs{"medium": 0.8, "low": 0.2}), "low", ReasonUpgradePending},
				{advice("medium", probs{"medium": 0.8, "low": 0.2}), "low", ReasonUpgradePending},
			},
		},
		{
			"a free switch passes the base bar",
			input("low"),
			[]step{
				{advice("medium", probs{"medium": 0.8, "low": 0.2}), "low", ReasonUpgradePending},
				{advice("medium", probs{"medium": 0.8, "low": 0.2}), "medium", ReasonUpgrade},
			},
		},
		{
			"a fresh history upgrades on one vote",
			Input{Policy: testPolicy, Baseline: "low", Incumbent: "low", Fresh: true},
			[]step{{medium90, "medium", ReasonUpgrade}},
		},
		{
			"a fresh history still needs the upgrade mass",
			Input{Policy: testPolicy, Baseline: "low", Incumbent: "low", Fresh: true},
			[]step{{advice("medium", probs{"medium": 0.6, "low": 0.4}), "low", ReasonUpgradePending}},
		},
		{
			"a fresh history downgrades on one vote",
			Input{Policy: testPolicy, Baseline: "low", Incumbent: "high", Fresh: true},
			[]step{{low95, "low", ReasonDowngrade}},
		},
		{
			"a downgrade needs two votes with mass",
			input("high"),
			[]step{{low95, "high", ReasonDowngradePending}, {low95, "low", ReasonDowngrade}},
		},
		{
			"a weak downgrade mass never switches",
			input("high"),
			[]step{
				{advice("low", probs{"low": 0.6, "high": 0.4}), "high", ReasonDowngradePending},
				{advice("low", probs{"low": 0.6, "high": 0.4}), "high", ReasonDowngradePending},
			},
		},
		{
			"a continuation holds a downgrade without a checkpoint",
			input("high"),
			[]step{
				{continuing(advice("micro", probs{"micro": 0.99}), 0.9), "high", ReasonDowngradePending},
				{continuing(advice("micro", probs{"micro": 0.99}), 0.9), "high", ReasonDowngradeHeld},
			},
		},
		{
			"a continuation past a checkpoint downgrades",
			Input{Policy: testPolicy, Baseline: "low", Incumbent: "high", DownAllowed: true},
			[]step{
				{continuing(advice("micro", probs{"micro": 0.99}), 0.9), "high", ReasonDowngradePending},
				{continuing(advice("micro", probs{"micro": 0.99}), 0.9), "micro", ReasonDowngrade},
			},
		},
		{
			"a mid-turn review holds a downgrade without a checkpoint",
			Input{Policy: testPolicy, Baseline: "low", Incumbent: "high", MidTurn: true},
			[]step{{micro99, "high", ReasonDowngradePending}, {micro99, "high", ReasonDowngradeHeld}},
		},
		{
			"a weak continuation is a new task and downgrades",
			input("high"),
			[]step{
				{continuing(advice("micro", probs{"micro": 0.99}), 0.5), "high", ReasonDowngradePending},
				{continuing(advice("micro", probs{"micro": 0.99}), 0.5), "micro", ReasonDowngrade},
			},
		},
		{
			"a continuation never holds an upgrade",
			input("low"),
			[]step{
				{continuing(advice("medium", probs{"medium": 0.9}), 0.9), "low", ReasonUpgradePending},
				{continuing(advice("medium", probs{"medium": 0.9}), 0.9), "medium", ReasonUpgrade},
			},
		},
		{
			"a split answer lands on the cheapest tier it supports as a whole",
			input("high"),
			[]step{
				{advice("micro", probs{"micro": 0.63, "low": 0.3, "high": 0.07}), "high", ReasonDowngradePending},
				{advice("micro", probs{"micro": 0.63, "low": 0.3, "high": 0.07}), "low", ReasonDowngrade},
			},
		},
		{
			"nothing below the incumbent clears the downgrade bar",
			input("high"),
			[]step{
				{advice("micro", probs{"micro": 0.6, "low": 0.2, "high": 0.2}), "high", ReasonDowngradePending},
				{advice("micro", probs{"micro": 0.6, "low": 0.2, "high": 0.2}), "high", ReasonDowngradePending},
			},
		},
		{
			"a cold candidate's tax raises the downgrade bar",
			Input{Policy: testPolicy, Baseline: "low", Incumbent: "medium", TaxTokens: flatTax(100_000)},
			[]step{
				{advice("low", probs{"low": 0.92, "high": 0.08}), "medium", ReasonDowngradePending},
				{advice("low", probs{"low": 0.92, "high": 0.08}), "medium", ReasonDowngradePending},
			},
		},
		{
			"an opposite vote breaks the streak",
			input("medium"),
			[]step{
				{advice("high", probs{"high": 0.9}), "medium", ReasonUpgradePending},
				{advice("low", probs{"low": 0.95}), "medium", ReasonDowngradePending},
				{advice("high", probs{"high": 0.9}), "medium", ReasonUpgradePending},
			},
		},
		{
			"a repeated failure escalates one tier, holds, then votes again",
			Input{Policy: testPolicy, Baseline: "low", Incumbent: "low", FailureSignature: sig},
			[]step{
				{micro99, "medium", ReasonEscalation},
				{micro99, "low", ReasonHold},
				{micro99, "low", ReasonHold},
				{micro99, "low", ReasonDowngradePending},
			},
		},
		{
			"escalation goes where Escalate points",
			Input{
				Policy: testPolicy, Baseline: "low", Incumbent: "low", FailureSignature: sig,
				Escalate: func(config.Tier) (config.Tier, bool) { return config.TierHigh, true },
			},
			[]step{{advice("low", probs{"low": 1}), "high", ReasonEscalation}},
		},
		{
			"no escalation target lets the advice decide",
			Input{
				Policy: testPolicy, Baseline: "low", Incumbent: "low", FailureSignature: sig,
				Escalate: func(config.Tier) (config.Tier, bool) { return "", false },
			},
			[]step{{advice("low", probs{"low": 1}), "low", ReasonSameTier}},
		},
		{
			"the top tier has nowhere to escalate",
			Input{Policy: testPolicy, Baseline: "low", Incumbent: "high", FailureSignature: sig},
			[]step{{advice("high", probs{"high": 1}), "high", ReasonSameTier}},
		},
		{
			"an escalated signature does not escalate twice",
			Input{Policy: testPolicy, Baseline: "low", Incumbent: "low", FailureSignature: sig, State: State{EscalatedSignature: sig}},
			[]step{{advice("low", probs{"low": 1}), "low", ReasonSameTier}},
		},
		{
			"a new signature escalates again",
			Input{Policy: testPolicy, Baseline: "low", Incumbent: "medium", FailureSignature: "panic: nil map", State: State{EscalatedSignature: sig}},
			[]step{{nil, "high", ReasonEscalation}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) { run(t, tc.in, tc.steps) })
	}
}

func TestDecideEscalationState(t *testing.T) {
	in := Input{
		Policy: testPolicy, Baseline: "low", Incumbent: "low", FailureSignature: "sig",
		State: State{Turn: 4, Votes: []Vote{{Tier: "micro"}}},
	}
	r := Decide(in)
	want := State{Turn: 5, HoldUntilTurn: 7, EscalatedSignature: "sig"}
	if r.State.Turn != want.Turn || r.State.HoldUntilTurn != want.HoldUntilTurn ||
		r.State.EscalatedSignature != want.EscalatedSignature || len(r.State.Votes) != 0 {
		t.Fatalf("state = %+v, want %+v", r.State, want)
	}
}

func TestDecideKeepsAFailureNoTierHelps(t *testing.T) {
	in := Input{
		Policy: testPolicy, Baseline: "low", Incumbent: "high", FailureSignature: "sig",
	}
	if r := Decide(in); r.State.EscalatedSignature != "" || r.State.HoldUntilTurn != 0 {
		t.Fatalf("state = %+v, want the failure unconsumed", r.State)
	}
}

func TestDecideKeepsTheLastVotesWithoutTouchingInput(t *testing.T) {
	old := []Vote{{"low"}, {"low"}, {"micro"}}
	in := input("medium")
	in.State = State{Turn: 3, Votes: old}
	in.Advice = advice("high", probs{"high": 0.5})

	r := Decide(in)

	want := []Vote{{"low"}, {"micro"}, {"high"}}
	if !slices.Equal(r.State.Votes, want) {
		t.Fatalf("votes = %v, want %v", r.State.Votes, want)
	}
	if !slices.Equal(old, []Vote{{"low"}, {"low"}, {"micro"}}) {
		t.Fatalf("input votes changed: %v", old)
	}
}

func TestDecideEstimate(t *testing.T) {
	cases := []struct {
		name      string
		incumbent config.Tier
		advice    *classifier.Advice
		tax       int
		want      Estimate
	}{
		{
			"upgrade without tax sits on the base bar",
			"low", advice("medium", probs{"medium": 0.8, "low": 0.2}), 0,
			Estimate{TaxTokens: 0, Threshold: 0.75, Mass: 0.8, Streak: 1},
		},
		{
			"upgrade tax at the pivot adds half the slope",
			"low", advice("medium", probs{"medium": 0.8, "low": 0.2}), 100_000,
			Estimate{TaxTokens: 100_000, Threshold: 0.825, Mass: 0.8, Streak: 1},
		},
		{
			"a negative tax counts as none",
			"low", advice("medium", probs{"medium": 0.8, "low": 0.2}), -5_000,
			Estimate{TaxTokens: 0, Threshold: 0.75, Mass: 0.8, Streak: 1},
		},
		{
			"a pending downgrade reports the choice's estimate",
			"high", advice("micro", probs{"micro": 0.5, "low": 0.3, "uncertain": 0.2}), 100_000,
			Estimate{TaxTokens: 100_000, Threshold: 0.94, Mass: 0.5, Streak: 1},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := input(tc.incumbent)
			in.Advice = tc.advice
			in.TaxTokens = flatTax(tc.tax)
			r := Decide(in)
			got := r.Estimate
			if got == nil {
				t.Fatalf("no estimate for %s", r.Reason)
			}
			if got.TaxTokens != tc.want.TaxTokens || got.Streak != tc.want.Streak ||
				!near(got.Threshold, tc.want.Threshold) || !near(got.Mass, tc.want.Mass) {
				t.Fatalf("estimate = %+v, want %+v", *got, tc.want)
			}
		})
	}
}

func TestDecideAsksTaxPerCandidateTier(t *testing.T) {
	in := input("high")
	in.Advice = advice("micro", probs{"micro": 0.63, "low": 0.3, "high": 0.07})
	in.State = State{Votes: []Vote{{"micro"}}}
	in.TaxTokens = func(t config.Tier) int {
		if t == config.TierLow {
			return 1_000_000
		}
		return 0
	}

	r := Decide(in)

	if r.Tier != config.TierMedium || r.Reason != ReasonDowngrade {
		t.Fatalf("got %s/%s, want medium/downgrade", r.Tier, r.Reason)
	}
}

func TestMass(t *testing.T) {
	p := probs{"micro": 0.1, "low": 0.2, "medium": 0.3, "high": 0.3, "uncertain": 0.1}
	if got := massAbove(p, config.TierLow); !near(got, 0.6) {
		t.Errorf("massAbove(low) = %v, want 0.6", got)
	}
	if got := massAtOrBelow(p, config.TierLow); !near(got, 0.3) {
		t.Errorf("massAtOrBelow(low) = %v, want 0.3", got)
	}
	if got := massAbove(p, config.TierHigh); got != 0 {
		t.Errorf("massAbove(high) = %v, want 0", got)
	}
}

func TestFitTier(t *testing.T) {
	windows := func(w map[config.Tier]int) func(config.Tier) (int, bool) {
		return func(t config.Tier) (int, bool) {
			n, ok := w[t]
			return n, ok
		}
	}
	growing := windows(map[config.Tier]int{"micro": 200_000, "low": 400_000, "medium": 400_000, "high": 1_000_000})
	smallTop := windows(map[config.Tier]int{"micro": 200_000, "low": 1_000_000, "medium": 200_000, "high": 200_000})
	unknownMicro := windows(map[config.Tier]int{"low": 400_000, "medium": 400_000, "high": 400_000})
	tie := windows(map[config.Tier]int{"micro": 100_000, "low": 100_000, "medium": 100_000, "high": 100_000})
	none := windows(nil)

	cases := []struct {
		name   string
		tier   config.Tier
		tokens int
		window func(config.Tier) (int, bool)
		want   config.Tier
		fits   bool
	}{
		{"fits where it is", "micro", 100_000, growing, "micro", true},
		{"exactly at the fill line fits", "micro", 160_000, growing, "micro", true},
		{"climbs past an overflowing window", "micro", 170_000, growing, "low", true},
		{"climbs several tiers", "low", 500_000, growing, "high", true},
		{"looks down when no higher tier fits", "high", 500_000, smallTop, "low", true},
		{"an unknown window never fits", "micro", 1_000, unknownMicro, "low", true},
		{"nothing fits: the largest window", "micro", 5_000_000, smallTop, "low", false},
		{"nothing fits on a tie: the tier itself", "medium", 5_000_000, tie, "medium", false},
		{"no window known: the tier itself", "medium", 1_000, none, "medium", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, fits := FitTier(tc.tier, tc.tokens, tc.window, 0.8)
			if got != tc.want || fits != tc.fits {
				t.Fatalf("FitTier = %s, %v; want %s, %v", got, fits, tc.want, tc.fits)
			}
		})
	}
}

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestLateral(t *testing.T) {
	medium := probs{"low": 0.01, "medium": 0.93, "high": 0.06}
	votes := func(ts ...config.Tier) State {
		var s State
		for _, t := range ts {
			s.Votes = append(s.Votes, Vote{Tier: t})
		}
		return s
	}
	kept := func(reason Reason, s State) Result { return Result{Tier: config.TierMedium, Reason: reason, State: s} }
	cases := []struct {
		name    string
		advice  *classifier.Advice
		midTurn bool
		down    bool
		fresh   bool
		r       Result
		tax     int
		cheaper bool
		want    bool
		reason  Reason
	}{
		{"cheaper on a new task", advice("medium", medium), false, false, false, kept(ReasonSameTier, votes("medium", "medium")), 0, true, true, ReasonDowngrade},
		{"cheaper on the same task waits for dwell and a check", continuing(advice("medium", medium), 0.9), false, false, false, kept(ReasonSameTier, votes("medium", "medium")), 0, true, false, ReasonDowngradeHeld},
		{"cheaper mid-turn waits too", advice("medium", medium), true, false, false, kept(ReasonSameTier, votes("medium", "medium")), 0, true, false, ReasonDowngradeHeld},
		{"cheaper on the same task after dwell and a check", continuing(advice("medium", medium), 0.9), false, true, false, kept(ReasonSameTier, votes("medium", "medium")), 0, true, true, ReasonDowngrade},
		{"cheaper needs the streak", advice("medium", medium), false, false, false, kept(ReasonSameTier, votes("high", "medium")), 0, true, false, ReasonDowngradePending},
		{"a fresh thread needs one vote", advice("medium", medium), false, false, true, kept(ReasonSameTier, votes("medium")), 0, true, true, ReasonDowngrade},
		{"tax raises the downgrade bar", advice("medium", medium), false, false, false, kept(ReasonSameTier, votes("medium", "medium")), 1_000_000, true, false, ReasonDowngradePending},
		{"cheaper needs mass at or below the tier", advice("medium", probs{"medium": 0.6, "high": 0.4}), false, true, false, kept(ReasonSameTier, votes("medium", "medium")), 0, true, false, ReasonDowngradePending},
		{"stronger with the upgrade bar", advice("medium", medium), true, false, false, kept(ReasonSameTier, votes("medium", "medium")), 0, false, true, ReasonUpgrade},
		{"stronger needs the streak", advice("medium", medium), false, false, false, kept(ReasonSameTier, votes("low", "medium")), 0, false, false, ReasonUpgradePending},
		{"stronger needs mass at or above the tier", advice("medium", probs{"low": 0.5, "medium": 0.5}), false, false, false, kept(ReasonSameTier, votes("medium", "medium")), 0, false, false, ReasonUpgradePending},
		{"a hold refuses a cheaper route", advice("medium", medium), false, true, false, kept(ReasonHold, votes("medium", "medium")), 0, true, false, ReasonHold},
		{"a hold takes a stronger route", advice("medium", medium), false, false, false, kept(ReasonHold, votes("medium", "medium")), 0, false, true, ReasonUpgrade},
		{"no answer keeps the route", nil, true, true, false, kept(ReasonSameTier, votes("medium", "medium")), 0, true, false, ReasonNoAdvice},
		{"an uncertain answer keeps the route", advice("uncertain", probs{"uncertain": 1}), false, true, false, kept(ReasonUncertain, votes("medium", "medium")), 0, false, false, ReasonUncertain},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := input(config.TierMedium)
			in.Advice, in.MidTurn, in.DownAllowed, in.Fresh = tc.advice, tc.midTurn, tc.down, tc.fresh
			ok, reason, _ := Lateral(in, tc.r, tc.tax, tc.cheaper)
			if ok != tc.want || reason != tc.reason {
				t.Errorf("Lateral = %v/%s, want %v/%s", ok, reason, tc.want, tc.reason)
			}
		})
	}
}
