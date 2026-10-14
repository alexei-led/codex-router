package config

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestDefaultRoutesMatchPlan(t *testing.T) {
	c := Default()
	want := map[Tier]Route{
		TierHigh:   {Model: "gpt-6.1-sol", Effort: EffortHigh},
		TierMedium: {Model: "gpt-6-sol", Effort: EffortMedium},
		TierLow:    {Model: "gpt-6-luna", Effort: EffortMedium},
		TierMicro:  {Model: "gpt-6-luna", Effort: EffortLow},
	}
	if !reflect.DeepEqual(c.Routes, want) {
		t.Errorf("routes = %+v, want %+v", c.Routes, want)
	}
	if c.Baseline != TierMedium {
		t.Errorf("baseline = %q, want medium", c.Baseline)
	}
	if got := c.RouteFor(TierMedium, ActivityDebug); got != (Route{Model: "gpt-6-sol", Effort: EffortHigh}) {
		t.Errorf("debug@medium = %+v", got)
	}
	if err := c.Validate(); err != nil {
		t.Errorf("default invalid: %v", err)
	}
}

func TestDefaultGoldenRoundTrip(t *testing.T) {
	golden, err := os.ReadFile("testdata/default.json")
	if err != nil {
		t.Fatal(err)
	}
	got, err := json.MarshalIndent(Default(), "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(append(got, '\n'), golden) {
		t.Errorf("Default() JSON differs from testdata/default.json:\n%s", got)
	}
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, golden, 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(loaded, Default()) {
		t.Errorf("golden load = %+v, want defaults", loaded)
	}
}

func TestLoad(t *testing.T) {
	missing, err := Load(filepath.Join(t.TempDir(), "nope.json"))
	if err != nil || !reflect.DeepEqual(missing, Default()) {
		t.Fatalf("missing file: cfg equal=%v err=%v", reflect.DeepEqual(missing, Default()), err)
	}

	write := func(body string) string {
		p := filepath.Join(t.TempDir(), "config.json")
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}

	partial, err := Load(write(`{"preset":"thrifty","routes":{"micro":{"model":"gpt-6-luna"}},"proxy":{"port":8123}}`))
	if err != nil {
		t.Fatal(err)
	}
	if partial.Preset != PresetThrifty || partial.Proxy.Port != 8123 {
		t.Errorf("overrides not applied: %+v", partial)
	}
	if partial.Routes[TierMicro].Effort != "" {
		t.Errorf("micro route not replaced whole: %+v", partial.Routes[TierMicro])
	}
	if partial.Routes[TierHigh] != Default().Routes[TierHigh] {
		t.Errorf("untouched route changed: %+v", partial.Routes[TierHigh])
	}

	for name, body := range map[string]string{
		"unknown key":   `{"baselin":"low"}`,
		"unknown inner": `{"policy":{"votes":1}}`,
		"syntax":        `{"baseline":`,
		"trailing data": `{} {}`,
	} {
		if _, err := Load(write(body)); err == nil {
			t.Errorf("%s: want error", name)
		}
	}
}

func TestLoadErrorsNameField(t *testing.T) {
	cases := []struct{ name, body, want string }{
		{"bad tier", `{"routes":{"huge":{"model":"m"}}}`, "routes.huge"},
		{"bad effort", `{"routes":{"low":{"model":"m","effort":"max"}}}`, "routes.low.effort"},
		{"empty model", `{"routes":{"high":{"effort":"high"}}}`, "routes.high.model"},
		{"bad baseline", `{"baseline":"huge"}`, "baseline"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "config.json")
			if err := os.WriteFile(p, []byte(tc.body), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := Load(p)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want mention of %q", err, tc.want)
			}
		})
	}
}

func TestEffectivePresetValues(t *testing.T) {
	cases := []struct {
		preset Preset
		change func(*Policy)
	}{
		{PresetBalanced, func(*Policy) {}},
		{PresetThrifty, func(p *Policy) {
			p.UpgradeVotes, p.UpgradeBase, p.JumpConfidence = 3, 0.85, 0.98
			p.DowngradeVotes, p.DowngradeMass, p.DowngradeHorizonTurns = 1, 0.8, 8
			p.MinDwellRequests, p.EscalationHoldTurns = 4, 1
		}},
		{PresetQuality, func(p *Policy) {
			p.UpgradeVotes, p.UpgradeBase, p.JumpConfidence = 1, 0.6, 0.9
			p.DowngradeVotes, p.DowngradeMass, p.DowngradeHorizonTurns = 3, 0.95, 3
			p.MinDwellRequests, p.EscalationHoldTurns = 16, 4
		}},
	}
	for _, tc := range cases {
		want := Default().Policy
		tc.change(&want)
		c := Default()
		c.Preset = tc.preset
		if got := c.Effective(); got != want {
			t.Errorf("%s: got %+v, want %+v", tc.preset, got, want)
		}
	}
}

func TestValidateRejects(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Config)
		want   string
	}{
		{"bad baseline", func(c *Config) { c.Baseline = "huge" }, "baseline"},
		{"unknown route tier", func(c *Config) { c.Routes["huge"] = Route{Model: "m"} }, "routes.huge"},
		{"missing route tier", func(c *Config) { delete(c.Routes, TierLow) }, "routes.low"},
		{"no routes", func(c *Config) { c.Routes = map[Tier]Route{} }, "routes."},
		{"empty route model", func(c *Config) { c.Routes[TierHigh] = Route{Effort: EffortHigh} }, "routes.high.model"},
		{"bad route effort", func(c *Config) { c.Routes[TierMicro] = Route{Model: "m", Effort: "max"} }, "routes.micro.effort"},
		{"unknown activity", func(c *Config) { c.Activities["uncertain"] = nil }, "activities.uncertain"},
		{"bad activity tier", func(c *Config) { c.Activities[ActivityCode] = map[Tier]Route{"x": {}} }, "activities.code.x"},
		{"bad activity effort", func(c *Config) {
			c.Activities[ActivityOps] = map[Tier]Route{TierLow: {Effort: "turbo"}}
		}, "activities.ops.low.effort"},
		{"bad preset", func(c *Config) { c.Preset = "lavish" }, "preset"},
		{"classifier not listed", func(c *Config) { c.Classifier = "ghost" }, "classifier"},
		{"bad classifier api", func(c *Config) {
			cl := c.Classifiers["jev"]
			cl.API = "openai-decisions"
			c.Classifiers["jev"] = cl
		}, "classifiers.jev.api"},
		{"zero classifier timeout", func(c *Config) {
			cl := c.Classifiers["ollama"]
			cl.TimeoutMs = 0
			c.Classifiers["ollama"] = cl
		}, "classifiers.ollama.timeoutMs"},
		{"policy mass above one", func(c *Config) { c.Policy.DowngradeMass = 1.5 }, "policy.downgradeMass"},
		{"policy zero votes", func(c *Config) { c.Policy.UpgradeVotes = 0 }, "policy.upgradeVotes"},
		{"warm margin over ttl", func(c *Config) { c.Cache.WarmMarginMs = c.Cache.TTLMs }, "cache"},
		{"port out of range", func(c *Config) { c.Proxy.Port = 70000 }, "proxy.port"},
		{"negative price", func(c *Config) { c.Prices = map[string]Price{"gpt-6-sol": {Input: -1}} }, "prices.gpt-6-sol"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := Default()
			tc.mutate(&c)
			err := c.Validate()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want mention of %q", err, tc.want)
			}
		})
	}
}

func TestEffective(t *testing.T) {
	base := Default().Policy
	cases := []struct {
		preset   Preset
		wantUp   int
		wantDown int
	}{
		{PresetThrifty, 3, 1},
		{PresetBalanced, base.UpgradeVotes, base.DowngradeVotes},
		{PresetQuality, 1, 3},
	}
	for _, tc := range cases {
		c := Default()
		c.Preset = tc.preset
		p := c.Effective()
		if p.UpgradeVotes != tc.wantUp || p.DowngradeVotes != tc.wantDown {
			t.Errorf("%s: votes up=%d down=%d, want %d/%d", tc.preset, p.UpgradeVotes, p.DowngradeVotes, tc.wantUp, tc.wantDown)
		}
		if p.ContextFill != base.ContextFill || p.ReviewEveryRequests != base.ReviewEveryRequests {
			t.Errorf("%s: preset changed a field it does not own", tc.preset)
		}
	}
	thrifty, quality := Default(), Default()
	thrifty.Preset, quality.Preset = PresetThrifty, PresetQuality
	if thrifty.Effective().UpgradeBase <= quality.Effective().UpgradeBase {
		t.Error("thrifty must demand more confidence to upgrade than quality")
	}
	if thrifty.Effective().DowngradeMass >= quality.Effective().DowngradeMass {
		t.Error("thrifty must accept less mass to downgrade than quality")
	}
}

func TestRouteFor(t *testing.T) {
	c := Default()
	c.Activities = map[Activity]map[Tier]Route{
		ActivityCode:   {TierLow: {Model: "gpt-6-sol"}},
		ActivityReview: {TierLow: {Effort: EffortHigh}},
		ActivityOps:    {TierMedium: {Model: "gpt-6-luna", Effort: EffortLow}},
	}
	cases := []struct {
		tier Tier
		act  Activity
		want Route
	}{
		{TierLow, ActivityCode, Route{Model: "gpt-6-sol", Effort: EffortMedium}},
		{TierLow, ActivityReview, Route{Model: "gpt-6-luna", Effort: EffortHigh}},
		{TierMedium, ActivityOps, Route{Model: "gpt-6-luna", Effort: EffortLow}},
		{TierHigh, ActivityCode, Route{Model: "gpt-6.1-sol", Effort: EffortHigh}},
		{TierLow, ActivityUncertain, Route{Model: "gpt-6-luna", Effort: EffortMedium}},
	}
	for _, tc := range cases {
		if got := c.RouteFor(tc.tier, tc.act); got != tc.want {
			t.Errorf("RouteFor(%s,%s) = %+v, want %+v", tc.tier, tc.act, got, tc.want)
		}
	}
}

func TestPath(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/xdg")
	if got, _ := Path(); got != filepath.Join("/xdg", "codex-router", "config.json") {
		t.Errorf("xdg path = %q", got)
	}
	t.Setenv("XDG_CONFIG_HOME", "relative/dir")
	t.Setenv("HOME", "/home/u")
	if got, _ := Path(); got != filepath.Join("/home/u", ".config", "codex-router", "config.json") {
		t.Errorf("fallback path = %q", got)
	}
}

func TestDecideTimeout(t *testing.T) {
	for _, tc := range []struct {
		name      string
		timeoutMs int
		want      time.Duration
	}{
		{"configured", 900, 1100 * time.Millisecond},
		{"unset falls back", 0, 1700 * time.Millisecond},
		{"negative falls back", -5, 1700 * time.Millisecond},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := Config{Classifier: "k", Classifiers: map[string]Classifier{"k": {TimeoutMs: tc.timeoutMs}}}
			if got := c.DecideTimeout(); got != tc.want {
				t.Errorf("DecideTimeout = %v, want %v", got, tc.want)
			}
		})
	}
}
