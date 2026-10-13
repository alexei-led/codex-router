package route

import (
	"maps"
	"slices"
	"time"

	"github.com/alexei-led/codex-router/internal/config"
	"github.com/alexei-led/codex-router/internal/cost"
	"github.com/alexei-led/codex-router/internal/responses"
)

// tally accumulates Stats; lastCell counts the replies in the strip's last
// cell, and the token sums give the cache reuse.
type tally struct {
	Stats
	lastCell     int
	cachedTokens int
	inputTokens  int
}

func newTally() tally {
	return tally{Stats: Stats{
		ByTier: map[config.Tier]int{}, ByActivity: map[config.Activity]int{}, RepliesPerCell: 1,
	}}
}

// switched records a route change and the uncached tokens it writes.
func (t *tally) switched(s Switch, tax int) {
	t.Switches++
	t.LastSwitch = &s
	t.UncachedSwitchTokens += tax
}

// reply records a routed reply. USD sums only replies whose routed and
// native models are both priced.
func (t *tally) reply(d Decision, u responses.Usage, at time.Time, prices map[string]config.Price, native string) {
	if t.Since.IsZero() {
		t.Since = at
	}
	t.Replies++
	t.ByTier[d.Tier]++
	if d.Activity != "" {
		t.ByActivity[d.Activity]++
	}
	t.push(d.Tier)
	t.cachedTokens += min(max(0, u.CachedInputTokens), u.InputTokens)
	t.inputTokens += max(0, u.InputTokens)
	if t.inputTokens > 0 {
		t.CacheReusePct = t.cachedTokens * 100 / t.inputTokens
	}
	routed, okR := prices[d.Route.Model]
	baseline, okB := prices[native]
	if okR && okB {
		t.RoutedUSD += cost.USD(routed, u)
		t.BaselineUSD += cost.USD(baseline, u)
	}
}

// push adds a reply's tier to the strip. A cell holds RepliesPerCell replies
// and shows the last; past StripCells, pairs of cells merge.
func (t *tally) push(tier config.Tier) {
	if n := len(t.Strip); n > 0 && t.lastCell < t.RepliesPerCell {
		t.Strip[n-1] = tier
		t.lastCell++
		return
	}
	t.Strip = append(t.Strip, tier)
	t.lastCell = 1
	if len(t.Strip) <= StripCells {
		return
	}
	merged := make([]config.Tier, 0, StripCells)
	for i := 1; i < len(t.Strip); i += 2 {
		merged = append(merged, t.Strip[i])
	}
	if len(t.Strip)%2 == 1 {
		merged = append(merged, t.Strip[len(t.Strip)-1])
	}
	t.Strip = merged
	t.RepliesPerCell *= 2
}

// Snapshot returns the full state for the control socket and the TUI.
func (e *Engine) Snapshot() Snapshot {
	s := e.lockedSnapshot()
	s.Classifier = e.health()
	return s
}

func (e *Engine) lockedSnapshot() Snapshot {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.snapshot(e.now())
}

// snapshot copies the state under e.mu. The focus thread is the one whose
// user step came last. Baseline is the route Codex sends without the router:
// the focus thread's first request, else the baseline tier's route.
func (e *Engine) snapshot(now time.Time) Snapshot {
	cfg := e.cfg
	s := Snapshot{
		At:              now,
		Mode:            ModeOn,
		Preset:          cfg.Preset,
		Baseline:        cfg.RouteFor(cfg.Baseline, ""),
		FooterSynced:    true,
		Recent:          slices.Clone(e.recent),
		ClassifierLabel: cfg.Classifiers[cfg.Classifier].Label,
		Routes:          maps.Clone(cfg.Routes),
		Policy:          cfg.Effective(),
		Stats:           e.stats.copy(),
	}
	if e.pin != nil {
		p := *e.pin
		s.Pin = &p
	}
	if e.last != nil {
		d := *e.last
		s.Last = &d
	}
	cat := e.cat.Current()
	model := s.Baseline.Model
	if th := e.focus; th != nil {
		s.Baseline, s.Footer, s.FooterSynced = th.native, th.footer, len(th.pending) == 0
		s.ApproxTokens = th.approx
		model = th.model
		if th.hasSent {
			model = th.sent.Model
		}
		if th.manual {
			s.Mode = ModeManual
		}
	}
	if e.paused {
		s.Mode = ModePaused
	}
	s.Window, _ = cat.Window(model)
	s.Group, _ = cat.GroupOf(model)
	seen := map[string]bool{}
	for _, t := range config.Tiers {
		for _, a := range append([]config.Activity{""}, config.Activities...) {
			m := cfg.RouteFor(t, a).Model
			if m != "" && !seen[m] && !cat.Compatible(model, m) {
				s.Blocked = append(s.Blocked, m)
			}
			seen[m] = true
		}
	}
	slices.Sort(s.Blocked)
	return s
}

func (t *tally) copy() Stats {
	s := t.Stats
	s.ByTier = maps.Clone(t.ByTier)
	s.ByActivity = maps.Clone(t.ByActivity)
	s.Strip = slices.Clone(t.Strip)
	if t.LastSwitch != nil {
		ls := *t.LastSwitch
		s.LastSwitch = &ls
	}
	return s
}
