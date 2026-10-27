package config

import (
	"encoding/json"
	"testing"
)

func TestDefaultPreferenceOwnershipIsIndependentPerCell(t *testing.T) {
	c := Default()
	c.Activities[ActivityDocs] = map[Tier]Route{TierLow: {Model: modelLuna, Effort: EffortLow}}
	c.Activities[ActivityCode] = map[Tier]Route{TierMedium: {Model: modelLuna, Effort: EffortXHigh}}
	before, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		mode     RoutingMode
		tier     Tier
		activity Activity
		want     Route
		owned    bool
	}{
		{ModeCost, TierMedium, ActivityDocs, Route{Model: modelLuna, Effort: EffortHigh}, true},
		{ModeCost, TierHigh, ActivityCode, Route{Model: modelTerra, Effort: EffortXHigh}, true},
		{ModeQuality, TierLow, ActivityCode, Route{Model: modelTerra, Effort: EffortMedium}, true},
		{ModeBalanced, TierLow, ActivityDocs, Route{}, false},
		{ModeCost, TierLow, ActivityDocs, Route{}, false},
		{ModeBalanced, TierMedium, ActivityCode, Route{}, false},
	} {
		got, owned := c.Preferred(tc.mode, tc.tier, tc.activity)
		if owned != tc.owned || got != tc.want {
			t.Errorf("%s/%s/%s = %v,%v; want %v,%v", tc.mode, tc.tier, tc.activity, got, owned, tc.want, tc.owned)
		}
	}
	if got := c.RouteFor(TierLow, ActivityDocs); got != (Route{Model: modelLuna, Effort: EffortLow}) {
		t.Errorf("explicit docs/low overwritten: %v", got)
	}
	if got := c.RouteFor(TierMedium, ActivityCode); got != (Route{Model: modelLuna, Effort: EffortXHigh}) {
		t.Errorf("explicit code/medium overwritten: %v", got)
	}
	after, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatal("preference lookup mutated custom declarations")
	}
	c.Activities[ActivityCode][TierHigh] = Route{}
	if _, owned := c.Preferred(ModeCost, TierHigh, ActivityCode); owned {
		t.Fatal("explicit empty target cell retained automatic preference")
	}
}
