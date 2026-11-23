package config

import (
	"encoding/json"
	"os"
	"path/filepath"
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

func TestModeTargetsInheritSparseFieldsAndNullMasksTierTarget(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	body := `{"modes":{"cost":{"routes":{"medium":{"model":"gpt-6.2-sol"},"high":{"model":"gpt-6.2-sol"}},"activities":{"code":{"medium":{"effort":"xhigh"},"high":null}}}}}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}

	got, source, ok := c.ModeTarget(ModeCost, TierMedium, ActivityCode)
	want := Route{Model: "gpt-6.2-sol", Effort: EffortXHigh}
	if !ok || source != "mode-activity" || got != want {
		t.Fatalf("code/medium target = %v, %q, %v; want %v, mode-activity, true", got, source, ok, want)
	}

	got, source, ok = c.ModeTarget(ModeCost, TierMedium, ActivityDocs)
	want = Route{Model: "gpt-6.2-sol", Effort: EffortMedium}
	if !ok || source != "mode-tier" || got != want {
		t.Fatalf("docs/medium target = %v, %q, %v; want %v, mode-tier, true", got, source, ok, want)
	}

	if generic, source, ok := c.ModeTarget(ModeCost, TierHigh, ""); !ok || source != "mode-tier" || generic.Model != modelSol {
		t.Fatalf("generic high target = %v, %q, %v; want the declared mode target", generic, source, ok)
	}
	if got, source, ok := c.ModeTarget(ModeCost, TierHigh, ActivityCode); ok || source != "" || got != (Route{}) {
		t.Fatalf("null code/high target = %v, %q, %v; want no explicit mode target", got, source, ok)
	}
	if got := c.RouteFor(TierHigh, ActivityCode); got != (Route{Model: modelSol, Effort: EffortHigh}) {
		t.Fatalf("null target changed balanced activity route: %v", got)
	}
}
