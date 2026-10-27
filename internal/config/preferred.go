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
