package control

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/alexei-led/codex-router/internal/config"
	"github.com/alexei-led/codex-router/internal/policy"
	"github.com/alexei-led/codex-router/internal/route"
)

// Decision.SupportParts is an additive protocol-v2 field: an engine that sends
// it also sends Support, so an older client reads the same words, and an older
// engine's payload decodes here with the parts absent. These fixtures are the
// literal record of both directions; the middle dot inside the first fragment
// is the classifier's own label, not the engine's separator, and no reader may
// split it out again.

// wireDecisionSupport is the decision the support fixtures freeze.
func wireDecisionSupport(support string, parts []string) route.Decision {
	return route.Decision{
		ThreadID: "t-1", TurnID: "turn-1", Step: 2,
		Tier: config.TierHigh, Activity: config.ActivityReview,
		Route:     config.Route{Model: "gpt-5-codex", Effort: config.EffortHigh},
		Original:  config.Route{Model: "gpt-5-mini"},
		Rewritten: true, Reason: policy.ReasonUpgrade,
		Why:          "the upgrade mass cleared the bar",
		Support:      support,
		Estimate:     &policy.Estimate{TaxTokens: 41000, Threshold: 0.55, Mass: 0.91, Streak: 2},
		MidTurn:      true,
		At:           time.Date(2024, 3, 4, 5, 6, 7, 0, time.UTC),
		SupportParts: parts,
	}
}

// wireSupportText is the words both producers send: the fragments joined with
// the separator Support has always used.
const wireSupportText = "Jev · fast gave stronger tiers 91% (needs 55%) · switch ≈ 41k uncached tokens"

func wireSupportParts() []string {
	return []string{"Jev · fast gave stronger tiers 91% (needs 55%)", "switch ≈ 41k uncached tokens"}
}

// TestDecisionSupportWireIsFrozen publishes what this build sends: the parts
// next to the unchanged Support, in the fixture's names and order.
func TestDecisionSupportWireIsFrozen(t *testing.T) {
	want := wireFixture(t, "decision-support.json")
	d := wireDecisionSupport(wireSupportText, wireSupportParts())
	got, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Fatalf("decision JSON changed:\ngot:\n%s\nfixture decision-support.json:\n%s", got, want)
	}
	var back route.Decision
	if err := json.Unmarshal([]byte(want), &back); err != nil {
		t.Fatalf("fixture does not decode into a decision: %v", err)
	}
	if !reflect.DeepEqual(back, d) {
		t.Errorf("decoded decision = %+v, want %+v", back, d)
	}
	again, err := json.MarshalIndent(back, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if string(again) != want {
		t.Fatalf("fixture does not round-trip:\ngot:\n%s\nfixture:\n%s", again, want)
	}
}

// TestLegacyDecisionSupportDecodes is the old-producer direction: a decision
// without the field decodes with no fragments and its Support untouched, and
// this build does not add the name when it re-publishes it.
func TestLegacyDecisionSupportDecodes(t *testing.T) {
	want := wireFixture(t, "decision-support-legacy.json")
	if strings.Contains(want, "SupportParts") {
		t.Fatal("the legacy fixture carries SupportParts; it must show the older payload")
	}
	var d route.Decision
	if err := json.Unmarshal([]byte(want), &d); err != nil {
		t.Fatalf("a decision without SupportParts does not decode: %v", err)
	}
	if d.SupportParts != nil {
		t.Errorf("SupportParts = %q, want none", d.SupportParts)
	}
	if d.Support != wireSupportText {
		t.Errorf("Support = %q, want %q", d.Support, wireSupportText)
	}
	again, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if string(again) != want {
		t.Fatalf("an old payload re-encodes differently:\ngot:\n%s\nwant:\n%s", again, want)
	}
}

// TestSupportPartsHoldTheirSeparator reads the fragments a newer engine sends:
// they stay whole, the middle dot inside the label included, and they spell the
// same words an older client reads in Support.
func TestSupportPartsHoldTheirSeparator(t *testing.T) {
	var d route.Decision
	if err := json.Unmarshal([]byte(wireFixture(t, "decision-support.json")), &d); err != nil {
		t.Fatal(err)
	}
	if want := wireSupportParts(); !reflect.DeepEqual(d.SupportParts, want) {
		t.Fatalf("SupportParts = %q, want %q", d.SupportParts, want)
	}
	if !strings.Contains(d.SupportParts[0], " · ") {
		t.Errorf("fragment %q lost the label's separator on the wire", d.SupportParts[0])
	}
	if joined := strings.Join(d.SupportParts, " · "); joined != d.Support {
		t.Errorf("Support = %q, want the fragments joined: %q", d.Support, joined)
	}

	// An older client that reads only Support sees what the fragments spell.
	var legacy route.Decision
	if err := json.Unmarshal([]byte(wireFixture(t, "decision-support-legacy.json")), &legacy); err != nil {
		t.Fatal(err)
	}
	if legacy.Support != d.Support {
		t.Errorf("old and new producers disagree on Support: %q, %q", legacy.Support, d.Support)
	}
}

// TestEmptySupportPartsAreLeftOut covers the empty cases: no evidence and an
// empty list both leave the name off the wire, while Support keeps its name
// and its zero value.
func TestEmptySupportPartsAreLeftOut(t *testing.T) {
	for _, tt := range []struct {
		name  string
		parts []string
	}{
		{"no fragments", nil},
		{"an empty list", []string{}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			b, err := json.Marshal(wireDecisionSupport("", tt.parts))
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(b), "SupportParts") {
				t.Errorf("%s published SupportParts: %s", tt.name, b)
			}
			if !strings.Contains(string(b), `"Support":""`) {
				t.Errorf("%s dropped Support, which has no omitempty: %s", tt.name, b)
			}
			var d route.Decision
			if err := json.Unmarshal(b, &d); err != nil {
				t.Fatal(err)
			}
			if d.SupportParts != nil || d.Support != "" {
				t.Errorf("decision = %+v, want no support", d)
			}
		})
	}
}

// TestSnapshotCarriesBothProducers reads the populated snapshot: a decision
// with fragments and one without, in the payload this build publishes.
func TestSnapshotCarriesBothProducers(t *testing.T) {
	var s route.Snapshot
	if err := json.Unmarshal([]byte(wireFixture(t, "snapshot-populated.json")), &s); err != nil {
		t.Fatal(err)
	}
	if s.Last == nil || s.Last.SupportParts != nil {
		t.Errorf("Last.SupportParts = %v, want none: the fixture's last decision is an older producer's", s.Last)
	}
	if len(s.Recent) != 1 || !reflect.DeepEqual(s.Recent[0].SupportParts, []string{"medium 0.42"}) {
		t.Errorf("Recent = %+v, want one decision with its fragment", s.Recent)
	}
	if s.Recent[0].Support != "medium 0.42" {
		t.Errorf("Recent[0].Support = %q, want it kept", s.Recent[0].Support)
	}
}
