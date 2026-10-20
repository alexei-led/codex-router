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
// cell, the token sums give the cache reuse, and tail holds the last
// SessionStrip replies' tiers, unmerged.
type tally struct {
	Stats
	lastCell     int
	cachedTokens int
	inputTokens  int
	tail         []config.Tier
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
	t.tail = appendCapped(t.tail, d.Tier, SessionStrip)
	t.LastInputTokens = max(0, u.InputTokens)
	t.LastCachedTokens = min(max(0, u.CachedInputTokens), t.LastInputTokens)
	t.cachedTokens += t.LastCachedTokens
	t.inputTokens += t.LastInputTokens
	if t.inputTokens > 0 {
		t.CacheReusePct = t.cachedTokens * 100 / t.inputTokens
	}
	t.EligibleReplies++
	routed, okR := prices[d.Route.Model]
	baseline, okB := prices[native]
	if okR && okB {
		t.PricedReplies++
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

// SnapshotFor returns one thread's snapshot; an unknown thread gets the
// home's state with no thread data.
func (e *Engine) SnapshotFor(threadID string) Snapshot {
	return e.withHealth(e.lockedSnapshot(threadID))
}

// withHealth adds what needs the classifier's health, read outside e.mu:
// the health itself and the findings worth a look.
func (e *Engine) withHealth(s Snapshot) Snapshot {
	s.Classifier, s.ClassifierOff = e.health(), e.ask == nil
	if s.ClassifierOff {
		s.ClassifierOffReason = e.askOff
	}
	s.Worth = worth(s)
	return s
}

func (e *Engine) lockedSnapshot(threadID string) Snapshot {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.snapshot(e.lookup(threadID), e.now())
}

// latest is the thread with the newest activity, the most recently used on
// a tie; nil before any thread's first step.
func (e *Engine) latest() *thread {
	var best *thread
	for el := e.lru.Front(); el != nil; el = el.Next() {
		th := el.Value.(*thread)
		if !th.lastActive.IsZero() && (best == nil || th.lastActive.After(best.lastActive)) {
			best = th
		}
	}
	return best
}

// mode is th's routing mode; a home pause covers every thread.
func (e *Engine) mode(th *thread) Mode {
	switch {
	case e.paused || th.paused:
		return ModePaused
	case th.manual:
		return ModeManual
	}
	return ModeOn
}

// snapshot copies th's state under e.mu; a nil th gives the home's state
// alone. Baseline is the route Codex sends without the router: the thread's
// first request, else the baseline tier's route.
func (e *Engine) snapshot(th *thread, now time.Time) Snapshot {
	cfg := e.cfg
	e.seq++
	s := Snapshot{
		Seq:              e.seq,
		At:               now,
		Mode:             ModeOn,
		Preset:           cfg.Preset,
		Baseline:         cfg.RouteFor(cfg.Baseline, ""),
		FooterSynced:     true,
		ClassifierLabel:  cfg.Classifiers[cfg.Classifier].Label,
		ClassifierModel:  cfg.Classifiers[cfg.Classifier].Model,
		ClassifierMedian: median(e.latencies),
		Routes:           maps.Clone(cfg.Routes),
		Policy:           cfg.Effective(),
		Stats:            newTally().Stats,
	}
	if e.paused {
		s.Mode = ModePaused
	}
	s.StateLost = e.lost
	cat := e.cat.Current()
	s.CatalogAvailable = len(cat.Models) > 0
	model := s.Baseline.Model
	if th != nil {
		s.ThreadID, s.Mode = th.id, e.mode(th)
		s.Recent, s.Stats = slices.Clone(th.recent), th.stats.copy()
		if th.pin != nil {
			p := *th.pin
			s.Pin = &p
		}
		if th.last != nil {
			d := *th.last
			s.Last = &d
		}
		if th.native.Model != "" {
			s.Baseline, s.Footer, s.FooterSynced = th.native, th.footer, len(th.pending) == 0
			s.ApproxTokens = th.approx
			model = th.model
			if th.hasSent {
				model = th.sent.Model
			}
		}
	}
	s.Window, _ = cat.Window(model)
	s.Group, s.CurrentModelKnown = cat.GroupOf(model)
	currentKnown := s.CurrentModelKnown
	unknown := map[string]bool{}
	if model != "" && !currentKnown {
		unknown[model] = true
	}
	seen := map[string]bool{}
	for _, t := range config.Tiers {
		for _, a := range append([]config.Activity{""}, config.Activities...) {
			m := cfg.RouteFor(t, a).Model
			if m == "" || seen[m] {
				continue
			}
			_, targetKnown := cat.GroupOf(m)
			switch {
			case !targetKnown:
				unknown[m] = true
			case currentKnown && !cat.Compatible(model, m):
				s.Blocked = append(s.Blocked, m)
			}
			seen[m] = true
		}
	}
	for m := range unknown {
		s.CompatibilityUnknown = append(s.CompatibilityUnknown, m)
	}
	slices.Sort(s.Blocked)
	slices.Sort(s.CompatibilityUnknown)
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
