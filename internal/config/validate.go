package config

import (
	"cmp"
	"errors"
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"strings"
)

// Validate checks the config and reports the first problem with its field path.
func (c Config) Validate() error {
	if !slices.Contains(Tiers, c.Baseline) {
		return fmt.Errorf("baseline: unknown tier %q", c.Baseline)
	}
	if err := c.validateRoutes(); err != nil {
		return err
	}
	if err := c.validateActivities(); err != nil {
		return err
	}
	if err := c.validatePrices(); err != nil {
		return err
	}
	if err := c.Policy.validate(); err != nil {
		return err
	}
	if !slices.Contains([]Preset{PresetThrifty, PresetBalanced, PresetQuality}, c.Preset) {
		return fmt.Errorf("preset: unknown preset %q", c.Preset)
	}
	if err := c.validateClassifiers(); err != nil {
		return err
	}
	if c.Context.RecentTurns < 1 || c.Context.MaxTextChars < 1 {
		return errors.New("context: recentTurns and maxTextChars must be positive")
	}
	if c.Cache.TTLMs < 1 || c.Cache.WarmMarginMs < 0 || c.Cache.WarmMarginMs >= c.Cache.TTLMs {
		return errors.New("cache: need ttlMs > 0 and 0 <= warmMarginMs < ttlMs")
	}
	if c.Proxy.Port < 0 || c.Proxy.Port > 65535 {
		return fmt.Errorf("proxy.port: %d out of range 0-65535", c.Proxy.Port)
	}
	return nil
}

func validRoute(path string, r Route, needModel bool) error {
	if needModel && r.Model == "" {
		return fmt.Errorf("%s.model: empty", path)
	}
	if r.Effort != "" && !slices.Contains(Efforts, r.Effort) {
		return fmt.Errorf("%s.effort: unknown effort %q", path, r.Effort)
	}
	return nil
}

func sortedKeys[K cmp.Ordered, V any](m map[K]V) []K {
	return slices.Sorted(maps.Keys(m))
}

func (c Config) validateRoutes() error {
	for _, t := range sortedKeys(c.Routes) {
		if !slices.Contains(Tiers, t) {
			return fmt.Errorf("routes.%s: unknown tier", t)
		}
	}
	for _, t := range Tiers {
		r, ok := c.Routes[t]
		if !ok {
			return fmt.Errorf("routes.%s: missing", t)
		}
		if err := validRoute("routes."+string(t), r, true); err != nil {
			return err
		}
	}
	return nil
}

func (c Config) validateActivities() error {
	for _, a := range sortedKeys(c.Activities) {
		if !slices.Contains(Activities, a) {
			return fmt.Errorf("activities.%s: unknown activity", a)
		}
		for _, t := range sortedKeys(c.Activities[a]) {
			path := fmt.Sprintf("activities.%s.%s", a, t)
			if !slices.Contains(Tiers, t) {
				return fmt.Errorf("%s: unknown tier", path)
			}
			if err := validRoute(path, c.Activities[a][t], false); err != nil {
				return err
			}
		}
	}
	return nil
}

func (c Config) validatePrices() error {
	for _, m := range sortedKeys(c.Prices) {
		p := c.Prices[m]
		if p.Input < 0 || p.CachedInput < 0 || p.Output < 0 {
			return fmt.Errorf("prices.%s: negative price", m)
		}
	}
	return nil
}

func (c Config) validateClassifiers() error {
	if _, ok := c.Classifiers[c.Classifier]; !ok {
		return fmt.Errorf("classifier: %q is not in classifiers", c.Classifier)
	}
	for _, name := range sortedKeys(c.Classifiers) {
		cl := c.Classifiers[name]
		path := "classifiers." + name
		if cl.API != APISystemOne && cl.API != APIOllama {
			return fmt.Errorf("%s.api: unknown api %q", path, cl.API)
		}
		if cl.Endpoint == "" {
			return fmt.Errorf("%s.endpoint: empty", path)
		}
		if cl.Model == "" {
			return fmt.Errorf("%s.model: empty", path)
		}
		if cl.TimeoutMs < 1 {
			return fmt.Errorf("%s.timeoutMs: must be positive", path)
		}
		if f := cl.EnvFile; f != "" && !filepath.IsAbs(f) && !strings.HasPrefix(f, "~/") {
			return fmt.Errorf("%s.envFile: %q is not an absolute or ~/ path", path, f)
		}
	}
	return nil
}

func (p Policy) validate() error {
	counts := []struct {
		name string
		v    int
		min  int
	}{
		{"upgradeVotes", p.UpgradeVotes, 1},
		{"downgradeVotes", p.DowngradeVotes, 1},
		{"upgradePivotTokens", p.UpgradePivotTokens, 1},
		{"downgradePivotTokens", p.DowngradePivotTokens, 1},
		{"downgradeHorizonTurns", p.DowngradeHorizonTurns, 1},
		{"escalationHoldTurns", p.EscalationHoldTurns, 0},
		{"reviewEveryRequests", p.ReviewEveryRequests, 0},
		{"minDwellRequests", p.MinDwellRequests, 0},
	}
	for _, f := range counts {
		if f.v < f.min {
			return fmt.Errorf("policy.%s: %d below minimum %d", f.name, f.v, f.min)
		}
	}
	unit := []struct {
		name string
		v    float64
	}{
		{"upgradeBase", p.UpgradeBase},
		{"jumpConfidence", p.JumpConfidence},
		{"downgradeMass", p.DowngradeMass},
		{"continuationMass", p.ContinuationMass},
		{"activityMass", p.ActivityMass},
		{"contextFill", p.ContextFill},
	}
	for _, f := range unit {
		if f.v <= 0 || f.v > 1 {
			return fmt.Errorf("policy.%s: %v outside (0, 1]", f.name, f.v)
		}
	}
	if p.UpgradeSlope < 0 || p.DowngradeSlope < 0 {
		return errors.New("policy: slopes must not be negative")
	}
	return nil
}
