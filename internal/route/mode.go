package route

import (
	"cmp"
	"slices"

	"github.com/alexei-led/codex-router/internal/catalog"
	"github.com/alexei-led/codex-router/internal/config"
)

// target is the route a routing mode aims at for tier t and activity a: the
// configured pair P(t), moved by at most one lever. Cost takes a cheaper
// model one tier down (never below low), else one effort step down; quality
// takes one effort step up, else the route one tier up. Micro and balanced
// keep P(t). It always starts from the configured pairs, never from a route
// already moved, so repeated steps do not compound. Only a configured effort
// on the low–xhigh ladder has an effort lever: an empty one follows the
// request, which carries the router's last write, so moving it would
// compound. levels lists the efforts a model accepts; none means every
// config.Efforts. The rules are fixed: router-modes design §2.
func target(cfg config.Config, mode config.RoutingMode, t config.Tier, a config.Activity, sent config.Effort, levels func(model string) []config.Effort) config.Route {
	pair := func(t config.Tier) config.Route { return resolve(cfg.RouteFor(t, a), sent) }
	base, own := pair(t), cfg.RouteFor(t, a).Effort
	rank := slices.Index(config.Tiers, t)
	if rank < 1 {
		return base
	}
	switch mode {
	case config.ModeCost:
		down := pair(config.Tiers[max(1, rank-1)])
		if down.Model != base.Model && cheaperInput(cfg, down.Model, base.Model) {
			return down
		}
		if e, ok := proj(own, -1, levels(base.Model)); ok && effortRank(e) < effortRank(own) {
			return config.Route{Model: base.Model, Effort: e}
		}
	case config.ModeQuality:
		if e, ok := proj(own, 1, levels(base.Model)); ok && effortRank(e) > effortRank(own) {
			return config.Route{Model: base.Model, Effort: e}
		}
		// An effort Codex sent can put P(t) above P(t+1); quality never
		// aims weaker than balanced.
		up := pair(config.Tiers[min(len(config.Tiers)-1, rank+1)])
		if o, ok := compareRoutes(cfg, up, base); ok && o < 0 {
			return base
		}
		return up
	}
	return base
}

// cheaperInput says model a has a lower input price than b, both priced.
// Input and cache tokens dominate an agent turn's cost.
func cheaperInput(cfg config.Config, a, b string) bool {
	pa, pb := cfg.Prices[a].Input, cfg.Prices[b].Input
	return pa > 0 && pb > 0 && pa < pb
}

// modeOrder orders a and b as the transform picks targets: by input price
// when both models are priced and differ, else as compareRoutes. The mode
// gate uses it, so a move cost picked as cheaper is gated as cheaper.
func modeOrder(cfg config.Config, a, b config.Route) (int, bool) {
	if a.Model != b.Model {
		if pa, pb := cfg.Prices[a.Model].Input, cfg.Prices[b.Model].Input; pa > 0 && pb > 0 && pa != pb {
			return cmp.Compare(pa, pb), true
		}
	}
	return compareRoutes(cfg, a, b)
}

// levelsIn lists, per model, the efforts cat says it accepts; none when cat
// does not list them.
func levelsIn(cat catalog.Catalog) func(model string) []config.Effort {
	return func(model string) []config.Effort {
		var out []config.Effort
		for _, l := range cat.Models[model].ReasoningLevels {
			out = append(out, l.Effort)
		}
		return out
	}
}

func effortRank(e config.Effort) int { return slices.Index(config.Efforts, e) }

// proj maps the effort delta steps from e onto the supported levels: the
// requested level, else the next higher supported, else the highest lower
// supported. False when e is off config.Efforts or the request falls off it,
// which keeps low the floor of every routed tier. A projection that lands on
// e or past it is no move: target checks the direction.
func proj(e config.Effort, delta int, supported []config.Effort) (config.Effort, bool) {
	i := slices.Index(config.Efforts, e)
	want := i + delta
	if i < 0 || want < 0 || want >= len(config.Efforts) {
		return "", false
	}
	ok := func(x config.Effort) bool { return len(supported) == 0 || slices.Contains(supported, x) }
	for _, x := range config.Efforts[want:] {
		if ok(x) {
			return x, true
		}
	}
	for j := want - 1; j >= 0; j-- {
		if ok(config.Efforts[j]) {
			return config.Efforts[j], true
		}
	}
	return "", false
}
