package route

import (
	"cmp"
	"slices"

	"github.com/alexei-led/codex-router/internal/classifier"
	"github.com/alexei-led/codex-router/internal/config"
	"github.com/alexei-led/codex-router/internal/facts"
	"github.com/alexei-led/codex-router/internal/policy"
)

// cell is a tier and the activity whose route runs at it, under a routing
// mode, with the policy's reason and next state. Ported from
// claude-model-router lib/route.mjs (routeTurn, decideCell) with taxes in
// uncached tokens.
type cell struct {
	tier     config.Tier
	activity config.Activity
	mode     config.RoutingMode // the mode the cell's route is aimed with
	reason   policy.Reason
	state    policy.State
	estimate *policy.Estimate
	advice   *classifier.Advice // the answer this step used, if any
	route    config.Route       // immutable physical pair; empty before resolution
}

// running is the incumbent cell with its resolved route.
type running struct {
	tier     config.Tier
	activity config.Activity
	mode     config.RoutingMode
	route    config.Route
}

// incumbent is the route in use: aimed with the mode it was chosen under, not
// the thread's mode now, so a mode change waits for a gate.
func (e *Engine) incumbent(st *step) running {
	th := st.th
	r := th.native
	if th.hasSent {
		r = th.sent
	}
	return running{tier: th.tier, activity: th.activity, mode: th.routeMode, route: r}
}

// downAllowed is the gate for a cheaper route on the same task: the dwell has
// passed since the last model change and a check passed since then.
func downAllowed(st *step) bool {
	th := st.th
	return th.requests-th.switchedAt.requests >= st.policy.MinDwellRequests && st.facts.Verified
}

// choose runs the policy for the turn's activity, then, when the tier stays
// but the route would change, a gate: the tier-switch gate when a routing
// mode aimed the target or the incumbent off the configured route, else the
// lateral rule for activities.
func (e *Engine) choose(st *step, inc running, advice *classifier.Advice, midTurn bool) cell {
	cfg, p, th := st.cfg, st.policy, st.th
	down := downAllowed(st)
	act := chooseActivity(cfg, p, advice, inc, func(t config.Tier, a config.Activity) config.Route { return st.routeFor(t, a) }, midTurn, down)
	routeFor := func(t config.Tier) config.Route { return st.routeFor(t, act) }
	var escalate func(config.Tier) (config.Tier, bool)
	if act != "" {
		// With an activity, a failure moves to the lowest tier whose route is
		// stronger, or one that does not order against it but differs.
		escalate = func(t config.Tier) (config.Tier, bool) {
			for _, up := range config.Tiers[slices.Index(config.Tiers, t)+1:] {
				r := routeFor(up)
				if o, ok := compareRoutes(cfg, r, inc.route); (ok && o > 0) || (!ok && r != inc.route) {
					return up, true
				}
			}
			return "", false
		}
	}
	// Logical high cannot tier-escalate. Escape cost's physical target to
	// balanced high only when that pair is eligible and strictly stronger.
	recovery := st.routeIn(config.ModeBalanced, config.TierHigh, act)
	physical := st.routeMode == config.ModeCost && inc.tier == config.TierHigh &&
		recovery.Model != inc.route.Model && e.fits(st, recovery)
	if o, ordered := compareRoutes(cfg, recovery, inc.route); !ordered || o <= 0 {
		physical = false
	}
	if physical {
		escalate = func(config.Tier) (config.Tier, bool) { return config.TierHigh, true }
	}
	signature := ""
	if st.facts.Failure != nil {
		signature = st.facts.Failure.Signature
	}
	in := policy.Input{
		Policy: p, Baseline: cfg.Baseline, Incumbent: inc.tier, Advice: advice,
		FailureSignature: signature, MidTurn: midTurn, DownAllowed: down, Fresh: !th.measured, State: th.state,
		TaxTokens: func(t config.Tier) int { return e.ledger.SwitchTax(th.id, routeFor(t).Model, st.tokens, st.now) },
		Escalate:  escalate,
	}
	r := policy.Decide(in)
	c := cell{tier: r.Tier, activity: act, mode: st.routeMode, reason: r.Reason, state: r.State, estimate: r.Estimate, advice: advice}
	to := routeFor(r.Tier)
	c.route = to
	if physical && r.Reason == policy.ReasonEscalation {
		c.mode, c.route = config.ModeBalanced, recovery
		return c
	}
	if r.Tier != inc.tier || to == inc.route {
		return c
	}
	// A move is the mode's when its target or the incumbent sits off the
	// configured route, so a move back to balanced passes the gate too.
	base := st.routeIn(config.ModeBalanced, r.Tier, act)
	if to != base || inc.route != st.routeIn(config.ModeBalanced, inc.tier, inc.activity) {
		o, ok := modeOrder(cfg, to, inc.route)
		tax := e.ledger.SwitchTax(th.id, to.Model, st.tokens, st.now)
		move, reason, est := policy.Lateral(in, r, tax, ok && o < 0)
		switch {
		case move:
			reason = ReasonMode
		case reason == policy.ReasonUpgradePending || reason == policy.ReasonDowngradePending:
			reason = ReasonModePending
		}
		if move {
			c.reason, c.estimate = reason, est
			return c
		}
		// While the mode's move waits, the balanced pair still moves by
		// today's activity rule toward the mode's side, so quality never runs
		// weaker than balanced, nor cost stronger. Nothing else moves before
		// the gate: not quality down, not cost up, not a return to balanced.
		ob, okb := modeOrder(cfg, base, inc.route)
		toward := (st.routeMode == config.ModeQuality && ob > 0) || (st.routeMode == config.ModeCost && ob < 0)
		if ok && okb && toward && (ob > 0) == (o > 0) && !th.recovering {
			c.mode, c.route = config.ModeBalanced, base
			return e.activityMove(st, c, inc, base, act, advice, r.Reason == policy.ReasonHold)
		}
		c.activity, c.mode, c.route = inc.activity, inc.mode, inc.route
		c.reason, c.estimate = reason, est
		return c
	}
	return e.activityMove(st, c, inc, to, act, advice, r.Reason == policy.ReasonHold)
}

// activityMove is today's same-tier rule: c's route to moves on the
// activity's probability mass, or the incumbent's pair stays.
func (e *Engine) activityMove(st *step, c cell, inc running, to config.Route, act config.Activity, advice *classifier.Advice, hold bool) cell {
	mass := 1.0 // returning to the base route needs no evidence
	if act != "" {
		mass = 0
		if advice != nil && advice.Activity != nil {
			mass = advice.Activity.Probabilities[act]
		}
	}
	move, reason, est := e.lateral(st, inc.route, to, mass, hold)
	if !move {
		c.activity, c.mode, c.route = inc.activity, inc.mode, inc.route
	}
	c.reason, c.estimate = reason, est
	return c
}

// observedMove moves a turn up once its tool calls show an activity whose
// route, at the tier the context fits, is stronger than the running one. The
// tools are the evidence, so the move needs no classifier probability. Only
// up. It aims with the mode the running route was aimed with: a mode switched
// mid-turn moves the route only through the gate. Ported from lib/route.mjs
// observedMove.
func (e *Engine) observedMove(st *step, cur cell) cell {
	act := observedActivity(st.facts.Observed)
	if act == "" || act == cur.activity {
		return cur
	}
	from := cur.route
	if from.Model == "" {
		from = st.routeIn(cur.mode, cur.tier, cur.activity)
	}
	routeFor := func(t config.Tier) config.Route { return st.routeIn(cur.mode, t, act) }
	tier, fits := policy.FitTier(cur.tier, st.tokens, e.window(st, routeFor), st.policy.ContextFill)
	if r := routeFor(cur.tier); e.effortOnly(st, r) {
		tier, fits = cur.tier, true
	}
	if !fits {
		return cur
	}
	to := routeFor(tier)
	if o, ok := compareRoutes(st.cfg, to, from); !ok || o <= 0 {
		return cur
	}
	// Before a measured reply the size is an estimate: a smaller window
	// might overflow.
	if !st.th.measured && smallerWindow(st, to, from) {
		return cur
	}
	move, _, est := e.lateral(st, from, to, 1, false)
	if !move {
		return cur
	}
	cur.tier, cur.activity, cur.reason, cur.estimate, cur.route = tier, act, ReasonObserved, est, to
	return cur
}

func smallerWindow(st *step, a, b config.Route) bool {
	wa, okA := st.cat.Window(a.Model)
	wb, okB := st.cat.Window(b.Model)
	return okA && okB && wa < wb
}

// lateral decides a route change inside one tier. Up (or unordered) on
// evidence: mass must clear the upgrade bar for the switch tax. Down on
// economics: a cheaper route earns no tokens back, so it moves only when the
// switch writes nothing extra, or before the first measured reply. During an
// escalation hold only a stronger route is taken.
func (e *Engine) lateral(st *step, from, to config.Route, mass float64, hold bool) (bool, policy.Reason, *policy.Estimate) {
	o, ok := compareRoutes(st.cfg, to, from)
	stronger := ok && o > 0
	if hold && !stronger {
		return false, policy.ReasonHold, nil
	}
	p := st.policy
	tax := e.ledger.SwitchTax(st.th.id, to.Model, st.tokens, st.now)
	est := &policy.Estimate{TaxTokens: tax, Mass: mass, Streak: 1}
	if ok && o < 0 {
		if tax > 0 && st.th.measured {
			return false, ReasonActivityPending, est
		}
		return true, ReasonActivityDown, est
	}
	est.Threshold = policy.UpgradeBar(p, tax)
	if mass < est.Threshold {
		return false, ReasonActivityPending, est
	}
	return true, ReasonActivityUp, est
}

// chooseActivity is the activity a turn runs with. Without an activity
// answer the incumbent's stays. On a new task the answer applies at
// ActivityMass. On the same task (a review inside a turn, or a continuation
// prompt) a stronger route is taken at once, a cheaper one only at
// DowngradeMass once the dwell and a passed check allow it.
func chooseActivity(cfg config.Config, p config.Policy, a *classifier.Advice, inc running, routeFor func(config.Tier, config.Activity) config.Route, midTurn, down bool) config.Activity {
	if a == nil || a.Activity == nil {
		return inc.activity
	}
	label := a.Activity.Choice
	if !slices.Contains(config.Activities, label) {
		if policy.SameTask(p, a, midTurn) {
			return inc.activity
		}
		return ""
	}
	mass := a.Activity.Probabilities[label]
	if !policy.SameTask(p, a, midTurn) {
		if mass >= p.ActivityMass {
			return label
		}
		return ""
	}
	o, ok := compareRoutes(cfg, routeFor(inc.tier, label), inc.route)
	if ok && o > 0 {
		return label
	}
	if ok && o < 0 && down && mass >= p.DowngradeMass {
		return label
	}
	return inc.activity
}

// compareRoutes orders two resolved routes: 1 when a is stronger, -1 when
// cheaper, 0 when equal, false when they do not order. Models order by
// configured output price, then by the highest tier whose base route runs
// them; the same model orders by effort.
func compareRoutes(cfg config.Config, a, b config.Route) (int, bool) {
	if a.Model != b.Model {
		pa, pb := cfg.Prices[a.Model].Output, cfg.Prices[b.Model].Output
		if pa > 0 && pb > 0 && pa != pb {
			return cmp.Compare(pa, pb), true
		}
		ra, rb := modelRank(cfg, a.Model), modelRank(cfg, b.Model)
		if ra < 0 || rb < 0 || ra == rb {
			return 0, false
		}
		return cmp.Compare(ra, rb), true
	}
	if a.Effort == b.Effort {
		return 0, true
	}
	ia, ib := slices.Index(config.Efforts, a.Effort), slices.Index(config.Efforts, b.Effort)
	if ia < 0 || ib < 0 {
		return 0, false
	}
	return cmp.Compare(ia, ib), true
}

func modelRank(cfg config.Config, model string) int {
	rank := -1
	for i, t := range config.Tiers {
		if cfg.Routes[t].Model == model {
			rank = i
		}
	}
	return rank
}

// resolve fills a route's empty effort with the one Codex sent.
func resolve(r config.Route, sent config.Effort) config.Route {
	if r.Effort == "" {
		r.Effort = sent
	}
	return r
}

// cellForModel is the tier and activity whose route runs r: an exact route
// first, then the model alone; base routes before activity overrides, the
// baseline tier first. Unknown models start on the baseline.
func cellForModel(cfg config.Config, r config.Route) (config.Tier, config.Activity) {
	order := []config.Tier{cfg.Baseline}
	for _, t := range config.Tiers {
		if t != cfg.Baseline {
			order = append(order, t)
		}
	}
	for _, exact := range []bool{true, false} {
		runs := func(c config.Route) bool { return c.Model == r.Model && (!exact || c.Effort == r.Effort) }
		for _, t := range order {
			if runs(cfg.Routes[t]) {
				return t, ""
			}
		}
		for _, t := range order {
			for _, a := range config.Activities {
				if _, ok := cfg.Activities[a][t]; ok && runs(cfg.RouteFor(t, a)) {
					return t, a
				}
			}
		}
	}
	return cfg.Baseline, ""
}

// observedActivity maps the turn's tool calls to the activity they show;
// reads and talk show none that could move a route up.
func observedActivity(o facts.Observed) config.Activity {
	switch o {
	case facts.ObservedCode:
		return config.ActivityCode
	case facts.ObservedDocs:
		return config.ActivityDocs
	case facts.ObservedOps:
		return config.ActivityOps
	}
	return ""
}

// label is the activity a decision shows: the applied one, else the
// classifier's routable answer.
func label(applied config.Activity, a *classifier.Advice) config.Activity {
	if applied != "" {
		return applied
	}
	if a != nil && a.Activity != nil && slices.Contains(config.Activities, a.Activity.Choice) {
		return a.Activity.Choice
	}
	return ""
}
