package route

import (
	"cmp"
	"maps"
	"slices"
	"time"

	"github.com/alexei-led/codex-router/internal/classifier"
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
	t.LastInputTokens = max(0, u.InputTokens)
	t.LastCachedTokens = min(max(0, u.CachedInputTokens), t.LastInputTokens)
	t.cachedTokens += t.LastCachedTokens
	t.inputTokens += t.LastInputTokens
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
	return e.withHealth(e.lockedSnapshot())
}

// withHealth adds what needs the classifier's health, read outside e.mu:
// the health itself and the findings worth a look.
func (e *Engine) withHealth(s Snapshot) Snapshot {
	s.Classifier, s.ClassifierOff = e.health(), e.ask == nil
	s.Worth = worth(s)
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
	e.seq++
	s := Snapshot{
		Seq:              e.seq,
		At:               now,
		Mode:             ModeOn,
		Preset:           cfg.Preset,
		Baseline:         cfg.RouteFor(cfg.Baseline, ""),
		FooterSynced:     true,
		Recent:           slices.Clone(e.recent),
		ClassifierLabel:  cfg.Classifiers[cfg.Classifier].Label,
		ClassifierMedian: median(e.latencies),
		Routes:           maps.Clone(cfg.Routes),
		Policy:           cfg.Effective(),
		Stats:            e.stats.copy(),
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

// median is the middle latency, the mean of the two middle ones for an even
// count, and zero for none.
func median(ds []time.Duration) time.Duration {
	if len(ds) == 0 {
		return 0
	}
	s := slices.Clone(ds)
	slices.Sort(s)
	n := len(s)
	if n%2 == 1 {
		return s[n/2]
	}
	return (s[n/2-1] + s[n/2]) / 2
}

// alternatesKept is how many activity alternates a Reading lists.
const alternatesKept = 2

// reading is the display copy of the turn's classifier answer; nil without one.
func reading(a *classifier.Advice) *Reading {
	if a == nil {
		return nil
	}
	r := &Reading{Tiers: map[config.Tier]float64{}}
	for _, t := range config.Tiers {
		if p, ok := a.Probabilities[classifier.Choice(t)]; ok {
			r.Tiers[t] = p
		}
	}
	if a.Activity == nil {
		return r
	}
	r.Activity = a.Activity.Choice
	r.ActivityP = a.Activity.Probabilities[r.Activity]
	for act, p := range a.Activity.Probabilities {
		if act != r.Activity && p > 0 {
			r.Alternates = append(r.Alternates, Share{Activity: act, P: p})
		}
	}
	slices.SortFunc(r.Alternates, func(x, y Share) int {
		if c := cmp.Compare(y.P, x.P); c != 0 {
			return c
		}
		return cmp.Compare(x.Activity, y.Activity)
	})
	r.Alternates = r.Alternates[:min(alternatesKept, len(r.Alternates))]
	return r
}
