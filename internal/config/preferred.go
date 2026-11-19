package config

import "maps"

// Preferred returns the shipped mode preference only for the exact default
// ladder and an activity cell the user has not declared. Custom declarations
// retain the generic mode transforms; no model or effort is injected into them.
func (c Config) Preferred(mode RoutingMode, t Tier, a Activity) (Route, bool) {
	if mode == "" {
		mode = ModeBalanced
	}
	if c.Activities == nil {
		a = ""
	}
	if activity, declared := c.Activities[a]; declared && activity == nil {
		a = ""
	}
	d := defaultRoutes()
	_, customActivity := c.Activities[a][t]
	if !maps.Equal(c.Routes, d) || customActivity {
		return Route{}, false
	}
	r := balancedPreference(d, t, a)
	if t == TierMicro {
		return r, true
	}
	switch mode {
	case ModeCost:
		switch t {
		case TierLow:
			r.Effort = EffortMedium
		case TierMedium:
			r = d[TierLow]
		case TierHigh:
			r = balancedPreference(d, TierMedium, a)
		}
	case ModeQuality:
		switch t {
		case TierLow:
			r = d[TierMedium]
		case TierMedium:
			r = Route{Model: modelSol, Effort: EffortMedium}
			switch a {
			case ActivityCode, ActivityDebug, ActivityPlan, ActivityReview:
				r.Effort = EffortHigh
			}
		case TierHigh:
			r.Effort = EffortXHigh
		}
	}
	return r, true
}

// ModeTarget returns a mode-specific route overlay for a tier and activity.
// An activity target takes precedence over the generic tier target. A null
// activity target suppresses that generic target for the cell. Unspecified
// model and effort fields inherit from the configured activity route.
func (c Config) ModeTarget(mode RoutingMode, t Tier, a Activity) (Route, string, bool) {
	if mode == "" {
		mode = ModeBalanced
	}
	base := c.RouteFor(t, a)
	m := c.Modes[mode]
	if cells, ok := m.Activities[a]; ok {
		if target, exists := cells[t]; exists {
			if target == nil {
				return Route{}, "", false
			}
			if generic := m.Routes[t]; generic != nil {
				base = applyTarget(base, *generic)
			}
			return applyTarget(base, *target), "mode-activity", true
		}
	}
	if target, exists := m.Routes[t]; exists && target != nil {
		return applyTarget(base, *target), "mode-tier", true
	}
	return Route{}, "", false
}

// ModeActivityDeclared reports whether a non-null mode activity target owns
// this cell. A null target explicitly clears inheritance but does not declare
// a route preference of its own.
func (c Config) ModeActivityDeclared(mode RoutingMode, t Tier, a Activity) bool {
	if mode == "" {
		mode = ModeBalanced
	}
	target, ok := c.Modes[mode].Activities[a][t]
	return ok && target != nil
}

func applyTarget(base, target Route) Route {
	if target.Model != "" {
		base.Model = target.Model
	}
	if target.Effort != "" {
		base.Effort = target.Effort
	}
	return base
}

func defaultRoutes() map[Tier]Route {
	return map[Tier]Route{
		TierHigh: {Model: modelSol, Effort: EffortHigh}, TierMedium: {Model: modelTerra, Effort: EffortMedium},
		TierLow: {Model: modelLuna, Effort: EffortHigh}, TierMicro: {Model: modelLuna, Effort: EffortMedium},
	}
}

func balancedPreference(d map[Tier]Route, t Tier, a Activity) Route {
	r := d[t]
	if t == TierMedium && (a == ActivityCode || a == ActivityDebug) ||
		t == TierHigh && (a == ActivityDebug || a == ActivityPlan || a == ActivityReview) {
		r.Effort = EffortXHigh
	}
	return r
}
