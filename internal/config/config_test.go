package config

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestDefaultRoutesMatchPlan(t *testing.T) {
	c := Default()
	want := map[Tier]Route{
		TierHigh:   {Model: "gpt-6.1-sol", Effort: EffortHigh},
		TierMedium: {Model: "gpt-6.1-sol", Effort: EffortMedium},
		TierLow:    {Model: "gpt-6-luna", Effort: EffortHigh},
		TierMicro:  {Model: "gpt-6-luna", Effort: EffortMedium},
	}
	if !reflect.DeepEqual(c.Routes, want) {
		t.Errorf("routes = %+v, want %+v", c.Routes, want)
	}
	if c.Baseline != TierMedium {
		t.Errorf("baseline = %q, want medium", c.Baseline)
	}
	if got := c.RouteFor(TierMedium, ActivityDebug); got != (Route{Model: "gpt-6.1-sol", Effort: EffortHigh}) {
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

func TestLoadClassifiers(t *testing.T) {
	jev := Default().Classifiers["jev"]
	withFile := jev
	withFile.EnvFile = "~/.config/codex-router/.env"
	noKey := jev
	noKey.KeyEnv = ""
	cases := []struct {
		name           string
		body           string
		wantClassifier string
		wantJev        Classifier
	}{
		{"envFile keeps the other fields", `{"classifiers":{"jev":{"envFile":"~/.config/codex-router/.env"}}}`, "jev", withFile},
		{"an empty field clears it", `{"classifiers":{"jev":{"keyEnv":""}}}`, "jev", noKey},
		{"off needs no entry", `{"classifier":"off"}`, ClassifierOff, jev},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "config.json")
			if err := os.WriteFile(p, []byte(tc.body), 0o600); err != nil {
				t.Fatal(err)
			}
			cfg, err := Load(p)
			if err != nil {
				t.Fatal(err)
			}
			if cfg.Classifier != tc.wantClassifier || cfg.Classifiers["jev"] != tc.wantJev {
				t.Errorf("classifier %q, jev %+v; want %q, %+v", cfg.Classifier, cfg.Classifiers["jev"], tc.wantClassifier, tc.wantJev)
			}
			if cfg.Classifiers["ollama"] != Default().Classifiers["ollama"] {
				t.Errorf("untouched ollama changed: %+v", cfg.Classifiers["ollama"])
			}
		})
	}
}

func TestLoadErrorsNameField(t *testing.T) {
	cases := []struct{ name, body, want string }{
		{"bad tier", `{"routes":{"huge":{"model":"m"}}}`, "routes.huge"},
		{"bad effort", `{"routes":{"low":{"model":"m","effort":"max"}}}`, "routes.low.effort"},
		{"empty model", `{"routes":{"high":{"effort":"high"}}}`, "routes.high.model"},
		{"bad baseline", `{"baseline":"huge"}`, "baseline"},
		{"unknown classifier", `{"classifier":"nope"}`, "classifier"},
		{"new classifier without endpoint", `{"classifiers":{"x":{"api":"ollama","model":"m","timeoutMs":1}}}`, "classifiers.x.endpoint"},
		{"unknown classifier field", `{"classifiers":{"jev":{"keyFile":"~/.env"}}}`, "classifiers.jev"},
		{"relative envFile", `{"classifiers":{"jev":{"envFile":"key.env"}}}`, "classifiers.jev.envFile"},
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
			p.DowngradeVotes, p.DowngradeMass = 1, 0.8
			p.MinDwellRequests, p.EscalationHoldTurns = 4, 1
		}},
		{PresetQuality, func(p *Policy) {
			p.UpgradeVotes, p.UpgradeBase, p.JumpConfidence = 1, 0.6, 0.9
			p.DowngradeVotes, p.DowngradeMass = 3, 0.95
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

func TestLegacyDowngradeHorizonIsAccepted(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(p, []byte(`{"policy":{"downgradeHorizonTurns":5}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	c.Policy.DowngradeHorizonTurns = 0
	if !reflect.DeepEqual(c, Default()) {
		t.Errorf("legacy setting changed the active config: %+v", c)
	}
}

func setEndpoint(c *Config, name, endpoint string) {
	cl := c.Classifiers[name]
	cl.Endpoint = endpoint
	c.Classifiers[name] = cl
}

func TestDefaultClassifiers(t *testing.T) {
	const cloudflare = "https://api.cloudflare.com/client/v4/accounts/{CLOUDFLARE_ACCOUNT_ID}/ai/run/@cf/cloudflare/"
	want := map[string]Classifier{
		"jev":        {Label: "Jev", API: APISystemOne, Endpoint: "https://api.typesafe.ai/v1/systemone", Model: "jev-1.13.0", KeyEnv: "TYPESAFE_API_KEY", TimeoutMs: 1500},
		"clef":       {Label: "Clef", API: APISystemOne, Endpoint: cloudflare + "clef", Model: "clef", KeyEnv: "CLOUDFLARE_API_TOKEN", TimeoutMs: 3000},
		"clef-flash": {Label: "Clef Flash", API: APISystemOne, Endpoint: cloudflare + "clef-flash", Model: "clef-flash", KeyEnv: "CLOUDFLARE_API_TOKEN", TimeoutMs: 3000},
		"clef-omni":  {Label: "Clef Omni", API: APISystemOne, Endpoint: cloudflare + "clef-omni", Model: "clef-omni", KeyEnv: "CLOUDFLARE_API_TOKEN", TimeoutMs: 3000},
		"openai":     {Label: "OpenAI", API: APIOpenAIDecisions, Endpoint: "https://api.openai.com/v1/decisions", Model: "gpt-6-luna", KeyEnv: "OPENAI_API_KEY", TimeoutMs: 3000},
		"ollama":     {Label: "Ollama", API: APIOllama, Endpoint: "http://127.0.0.1:11434/api/chat", Model: "qwen3.5:9b", TimeoutMs: 5000},
	}
	if got := Default(); got.Classifier != "jev" || !reflect.DeepEqual(map[string]Classifier(got.Classifiers), want) {
		t.Errorf("classifier = %q, classifiers = %+v", got.Classifier, got.Classifiers)
	}
}

func TestEndpointPlaceholders(t *testing.T) {
	cases := []struct {
		endpoint string
		want     []string
		wantErr  bool
	}{
		{"https://x/y", nil, false},
		{"https://x/{ACCOUNT_ID}/run", []string{"ACCOUNT_ID"}, false},
		{"https://{HOST}/{A1}/{HOST}", []string{"HOST", "A1"}, false},
		{"https://x/{acct}", nil, true},
		{"https://x/{_A}", nil, true},
		{"https://x/{A B}", nil, true},
		{"https://x/{A", nil, true},
	}
	for _, tc := range cases {
		t.Run(tc.endpoint, func(t *testing.T) {
			got, err := EndpointPlaceholders(tc.endpoint)
			if (err != nil) != tc.wantErr || !slices.Equal(got, tc.want) {
				t.Errorf("EndpointPlaceholders = %v, %v; want %v, err %v", got, err, tc.want, tc.wantErr)
			}
		})
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
			cl.API = "bogus"
			c.Classifiers["jev"] = cl
		}, "classifiers.jev.api"},
		{"lowercase placeholder", func(c *Config) { setEndpoint(c, "clef", "https://x/{account}/run") }, "classifiers.clef.endpoint"},
		{"empty placeholder", func(c *Config) { setEndpoint(c, "clef", "https://x/{}/run") }, "classifiers.clef.endpoint"},
		{"digit-first placeholder", func(c *Config) { setEndpoint(c, "clef", "https://x/{1A}/run") }, "classifiers.clef.endpoint"},
		{"nested placeholder", func(c *Config) { setEndpoint(c, "clef", "https://x/{{A}}/run") }, "classifiers.clef.endpoint"},
		{"unclosed placeholder", func(c *Config) { setEndpoint(c, "clef", "https://x/{A/run") }, "classifiers.clef.endpoint"},
		{"stray closing brace", func(c *Config) { setEndpoint(c, "clef", "https://x/A}/run") }, "classifiers.clef.endpoint"},
		{"zero classifier timeout", func(c *Config) {
			cl := c.Classifiers["ollama"]
			cl.TimeoutMs = 0
			c.Classifiers["ollama"] = cl
		}, "classifiers.ollama.timeoutMs"},
		{"relative envFile", func(c *Config) {
			cl := c.Classifiers["jev"]
			cl.EnvFile = ".env"
			c.Classifiers["jev"] = cl
		}, "classifiers.jev.envFile"},
		{"tilde-user envFile", func(c *Config) {
			cl := c.Classifiers["jev"]
			cl.EnvFile = "~bob/.env"
			c.Classifiers["jev"] = cl
		}, "classifiers.jev.envFile"},
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
		ActivityReview: {TierLow: {Effort: EffortLow}},
		ActivityOps:    {TierMedium: {Model: "gpt-6-luna", Effort: EffortLow}},
	}
	cases := []struct {
		tier Tier
		act  Activity
		want Route
	}{
		{TierLow, ActivityCode, Route{Model: "gpt-6-sol", Effort: EffortHigh}},
		{TierLow, ActivityReview, Route{Model: "gpt-6-luna", Effort: EffortLow}},
		{TierMedium, ActivityOps, Route{Model: "gpt-6-luna", Effort: EffortLow}},
		{TierHigh, ActivityCode, Route{Model: "gpt-6.1-sol", Effort: EffortHigh}},
		{TierLow, ActivityUncertain, Route{Model: "gpt-6-luna", Effort: EffortHigh}},
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
			c := Config{Classifier: "k", Classifiers: Classifiers{"k": {TimeoutMs: tc.timeoutMs}}}
			if got := c.DecideTimeout(); got != tc.want {
				t.Errorf("DecideTimeout = %v, want %v", got, tc.want)
			}
		})
	}
	off := Default()
	off.Classifier = ClassifierOff
	if got := off.DecideTimeout(); got != 1700*time.Millisecond {
		t.Errorf("off: DecideTimeout = %v, want the fallback", got)
	}
}
