package route

import (
	"container/list"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"runtime"
	"slices"
	"sync"
	"time"

	"github.com/alexei-led/codex-router/internal/catalog"
	"github.com/alexei-led/codex-router/internal/classifier"
	"github.com/alexei-led/codex-router/internal/config"
	"github.com/alexei-led/codex-router/internal/cost"
	"github.com/alexei-led/codex-router/internal/facts"
	"github.com/alexei-led/codex-router/internal/home"
	"github.com/alexei-led/codex-router/internal/policy"
	"github.com/alexei-led/codex-router/internal/responses"
)

// Reasons the engine adds to policy's: activity moves inside a tier, a move
// up on the turn's own tool calls, a later step keeping the turn's route, and
// a routing mode's move inside a tier.
const (
	ReasonMode            policy.Reason = "mode"
	ReasonModePending     policy.Reason = "mode-pending"
	ReasonActivityUp      policy.Reason = "activity-up"
	ReasonActivityDown    policy.Reason = "activity-down"
	ReasonActivityPending policy.Reason = "activity-pending"
	ReasonObserved        policy.Reason = "observed"
	ReasonContinue        policy.Reason = "continue"
)

// Bounds on per-thread memory.
const (
	retriesKept   = 8
	pendingWrites = 8
	// latenciesKept bounds the classifier answers ClassifierMedian spans.
	latenciesKept = 32
)

var (
	errNoClassifier    = errors.New("no classifier")
	errClassifierPanic = errors.New("classifier panic")
)

// Engine routes user steps. All state lives under mu; step serializes the
// requests of one thread so a classifier call never runs under mu. Routing
// state, tallies, decisions and routing modes are per thread; paused and lost
// are the home's.
type Engine struct {
	cat          catalog.Source
	ask          classifier.Asker
	askOff       string // why ask is nil
	now          func() time.Time
	sink         Sink
	log          *slog.Logger
	ledger       *cost.Ledger
	home         home.Home
	cwd          func(threadID string) string
	saveIntent   func(Intent)
	holdUnseeded bool
	projectMode  func(threadID string) (config.RoutingMode, error)
	loadMode     func(threadID string) config.RoutingMode
	saveMode     func(threadID string, m config.RoutingMode)
	// reviews counts background classifier reviews in flight.
	reviews sync.WaitGroup

	mu      sync.Mutex
	cfg     config.Config
	threads map[string]*list.Element
	lru     *list.List
	paused  bool // the whole home
	lost    bool // the saved intent was unreadable; a home resume clears it
	// latencies are the recent classifier answers' latencies.
	latencies []time.Duration
	seq       uint64 // the last snapshot's Seq
	intentRev uint64 // the last exported Intent's Rev
}

// thread is one Codex thread's routing state. step is held for a whole
// Decide; every other field is guarded by Engine.mu.
type thread struct {
	id   string
	step sync.Mutex

	native   config.Route // Codex's own route: what it sent first, or the user's /model pick
	model    string       // the last request's model
	tier     config.Tier
	activity config.Activity // the activity whose route runs, "" for the base route
	// routeMode is the routing mode the running route was chosen under. The
	// route in use is its target for tier and activity; it changes only when
	// a gate passes, not when the thread's mode does.
	routeMode config.RoutingMode
	state     policy.State
	measured  bool // a reply on this thread was observed

	codexTurn  string // the turn of the last user step, routed or not
	turn       string
	stepIndex  int
	turnPinned bool
	turnOff    bool // the classifier failed on the turn's first step, or the turn started held
	heldTurn   bool // the turn started held: its steps keep the held route
	advice     *classifier.Advice
	retries    []retry
	review     *review

	requests     int // decided steps
	switchedAt   mark
	reviewedAt   int
	observedFrom int
	sent         config.Route
	hasSent      bool
	awaiting     *Decision // the decision whose reply is next
	lastItems    int
	lastTotal    int
	approx       int

	manual      bool
	wrote       bool
	lastWritten config.Route
	pending     []config.Route // writes not echoed yet
	footer      config.Route

	paused     bool               // routing paused for this thread alone
	mode       config.RoutingMode // the session's routing mode override, "" for none
	project    config.RoutingMode // the mode the project file set at the last turn's start
	projectErr string             // why the project file was ignored, "" when it was not
	held       bool               // seeded: keeps its route until a reply measures the cache
	account    string             // the account hash the thread is bound to, "" until one is seen
	pin        *config.Tier       // the next turn's tier; its first step consumes it
	lastActive time.Time          // the last user step or reply; zero until the first step
	stats      tally
	recent     []Decision
	last       *Decision
}

// mark is where the running route started: the request count and the item
// count then.
type mark struct{ requests, items int }

type retry struct {
	fingerprint string
	decision    Decision
}

type review struct {
	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}
	upTo   int
	ready  bool
	answer *classifier.Advice
}

// New returns an engine started from d.Intent. A nil Now uses time.Now, a
// nil Logger discards.
func New(d Deps) *Engine {
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.Logger == nil {
		d.Logger = slog.New(slog.DiscardHandler)
	}
	if d.Catalog == nil {
		d.Catalog = &catalog.Store{}
	}
	ms := func(n int) time.Duration { return time.Duration(n) * time.Millisecond }
	e := &Engine{
		cat:          d.Catalog,
		ask:          d.Classifier,
		askOff:       d.ClassifierOffReason,
		now:          d.Now,
		sink:         d.Sink,
		log:          d.Logger,
		ledger:       cost.NewLedger(cost.Cache{TTL: ms(d.Config.Cache.TTLMs), WarmMargin: ms(d.Config.Cache.WarmMarginMs)}),
		home:         d.Home,
		cwd:          d.Cwd,
		saveIntent:   d.SaveIntent,
		holdUnseeded: d.HoldUnseeded,
		projectMode:  d.ProjectMode,
		loadMode:     d.LoadMode,
		saveMode:     d.SaveMode,
		cfg:          d.Config,
		threads:      map[string]*list.Element{},
		lru:          list.New(),
	}
	e.restore(d.Intent, d.IntentLost)
	return e
}

// Resumed takes r, the route Codex reports for a thread on thread/resume (an
// empty effort is the model's default), and reports whether the router's
// last route should be written to the footer again.
//
// A thread the engine has seen no request on (a brain start) is seeded at
// r. It keeps r until a reply measures the cache: with an empty ledger the
// engine takes every cache for cold and would switch on one vote. The turn
// that reply ends stays on r too.
//
// On a known thread r is checked as thread/settings/updated would be, since
// a /model pick made while the brain was not subscribed sent it nothing: a
// route the router wrote, or the thread's native one (a restarted daemon
// reloads that and lost the router's write), is written again; any other is
// the user's pick, and the thread goes manual. A pick back to the native
// route then looks like a restarted daemon and is overwritten.
func (e *Engine) Resumed(threadID string, r config.Route) (resync bool) {
	defer func() {
		if p := recover(); p != nil {
			e.log.Error("route resume failed", "thread", threadID, "panic", panicText(p))
			resync = false
		}
	}()
	if threadID == "" || r.Model == "" {
		return true
	}
	s, resync, manual := e.resumed(threadID, r)
	if manual {
		e.persist()
		e.emitSnapshot(s)
	}
	return resync
}

func (e *Engine) resumed(threadID string, r config.Route) (s Snapshot, resync, manual bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	th := e.lookup(threadID)
	if th == nil {
		th = e.add(threadID)
	}
	if th.native.Model == "" {
		th.native, th.lastWritten, th.footer = r, r, r
		th.tier, th.activity = cellForModel(e.cfg, r)
		th.held = true
		e.log.Info("route seeded", "thread", threadID, "tier", th.tier, "model", r.Model, "effort", r.Effort)
		return Snapshot{}, true, false
	}
	th.footer = r
	switch i := th.pendingIndex(r, e.sameSetting); {
	case i >= 0:
		th.pending = slices.Clone(th.pending[i+1:])
		return Snapshot{}, true, false
	case th.manual:
		th.lastWritten, th.native = r, r
		return Snapshot{}, false, false
	case e.sameSetting(r, th.lastWritten) || e.sameSetting(r, th.native):
		return Snapshot{}, true, false
	}
	e.pick(th, r)
	return e.snapshot(th, e.now()), false, true
}

// pick puts th on the user's /model pick r (plan D12) under e.mu. The pick
// is now what Codex would use without the router, so pause restores it
// rather than the route the session started on.
func (e *Engine) pick(th *thread, r config.Route) {
	th.manual, th.pending, th.lastWritten, th.native = true, nil, r, r
	e.log.Info("route manual", "thread", th.id, "model", r.Model, "effort", r.Effort)
}

// step is one request's inputs, gathered under the lock.
type step struct {
	th       *thread
	meta     responses.Meta
	req      *responses.Request
	fp       string
	cfg      config.Config
	policy   config.Policy
	cat      catalog.Catalog
	now      time.Time
	first    bool
	pin      *config.Tier
	original config.Route
	opts     facts.Options
	facts    facts.Facts
	tokens   int
	// mode is the thread's routing mode and source where it comes from;
	// routeMode is the mode routes are aimed with: balanced on a pinned turn,
	// since a pin bypasses the mode.
	mode      config.RoutingMode
	source    ModeSource
	routeMode config.RoutingMode
}

func (s *step) sent() config.Effort { return s.req.Effort }

// routeFor is the route the step's routing mode aims at for tier t and
// activity a.
func (s *step) routeFor(t config.Tier, a config.Activity) config.Route {
	return s.routeIn(s.routeMode, t, a)
}

// routeIn is mode's target for tier t and activity a, over the catalog's
// efforts per model.
func (s *step) routeIn(mode config.RoutingMode, t config.Tier, a config.Activity) config.Route {
	return target(s.cfg, mode, t, a, s.sent(), levelsIn(s.cat))
}

// Decide returns the rewrite for one request, or false to forward it
// unchanged. Any panic or error forwards unchanged. Once ctx is done the
// caller forwards the request unchanged, so a step whose ctx ends before it
// commits records nothing: no decision, no footer write, no pin spent.
func (e *Engine) Decide(ctx context.Context, req *responses.Request, m responses.Meta) (rw responses.Rewrite, ok bool) {
	defer func() {
		if r := recover(); r != nil {
			e.log.Error("route decide failed", "thread", m.ThreadID, "turn", m.TurnID, "panic", panicText(r))
			rw, ok = responses.Rewrite{}, false
		}
	}()
	if req == nil {
		return responses.Rewrite{}, false
	}
	key := threadKey(m)
	if responses.Classify(m) != responses.KindUserStep || key == "" {
		e.passThrough(key)
		return responses.Rewrite{}, false
	}
	th := e.thread(key)
	th.step.Lock()
	defer th.step.Unlock()
	if ctx.Err() != nil {
		return responses.Rewrite{}, false
	}

	st, retried, old, picked := e.begin(th, req, m)
	if picked {
		e.persist()
		e.emitSnapshot(e.lockedSnapshot(key))
	}
	if retried != nil {
		return rewriteOf(*retried)
	}
	if old != nil {
		old.stop(ctx)
	}
	if st == nil {
		return responses.Rewrite{}, false
	}
	st.facts = facts.Extract(req, st.opts)
	st.tokens = max(st.tokens, st.facts.ApproxTokens)
	var advice *classifier.Advice
	var askErr error
	if st.first && st.pin == nil {
		advice, askErr = e.classify(ctx, st)
	}
	if ctx.Err() != nil {
		e.abandon(st)
		return responses.Rewrite{}, false
	}
	if st.pin != nil {
		e.persist() // the pin is spent; a restart must not run it again
	}

	var d Decision
	if st.first {
		var ok bool
		if d, ok = e.decideFirst(st, advice, askErr); !ok {
			if st.pin != nil {
				e.persist() // the pin was given back
			}
			return responses.Rewrite{}, false
		}
	} else {
		var job *review
		d, job = e.decideLater(st)
		if job != nil {
			e.startReview(th, job, classifierInput(st.facts, st.cfg))
		}
	}
	e.publish(d)
	return rewriteOf(d)
}

// begin reads the thread under the lock. It returns a fixed decision (a
// retry's, or a held turn's route), nil step when the request passes unchanged, a review the new turn
// replaced, for the caller to stop outside the lock, and whether the request
// showed a /model pick, for the caller to save.
func (e *Engine) begin(th *thread, req *responses.Request, m responses.Meta) (*step, *Decision, *review, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	cfg := e.cfg
	now := e.now()
	original := config.Route{Model: req.Model, Effort: req.Effort}
	e.bind(th, m.Account)
	// seen: the engine saw a request on this thread before. A brain that
	// restarted mid-turn sees that turn's later steps first; they still
	// carry the settings the turn started with, not the seeded ones.
	seen, newTurn := th.codexTurn != "", th.codexTurn != m.TurnID
	th.codexTurn = m.TurnID
	picked := false
	switch {
	case th.native.Model == "":
		th.native, th.lastWritten, th.footer = original, original, original
		th.tier, th.activity = cellForModel(cfg, original)
		if e.holdUnseeded && inConversation(req.Input) {
			th.held = true
			e.log.Info("route held unseeded", "thread", th.id, "tier", th.tier, "model", original.Model, "effort", original.Effort)
		}
	case seen && newTurn && !th.manual && !th.knows(original):
		// A turn runs on the settings it started with, so only its first
		// step can show a /model pick. This one catches the picks
		// thread/settings/updated did not report: the brain was not
		// subscribed to the thread.
		e.pick(th, original)
		picked = true
	}
	th.model, th.lastActive = req.Model, now
	if e.paused || th.paused || th.manual {
		th.awaiting = nil
		return nil, nil, nil, picked
	}
	if th.held && th.pin == nil {
		// Unrouted until a reply ends the hold, and for the rest of that
		// turn. A pin is the user's word and ends the hold.
		th.awaiting = nil
		th.turn, th.turnOff, th.heldTurn = m.TurnID, true, true
		return nil, e.holdStep(th, original), nil, false
	}
	th.held = false
	fp := req.Fingerprint(m)
	for _, r := range th.retries {
		if r.fingerprint == fp {
			d := r.decision
			return nil, &d, nil, false
		}
	}
	th.awaiting = nil
	var old *review
	first := th.turn != m.TurnID
	if first {
		old, th.review = th.review, nil
		th.turn, th.stepIndex, th.turnPinned, th.turnOff, th.heldTurn, th.advice = m.TurnID, 0, false, false, false, nil
		th.retries, th.observedFrom = nil, 0
	} else if th.turnOff {
		return nil, e.holdStep(th, original), nil, false
	}
	if n := len(req.Input); n < th.lastItems {
		// Compaction or a rewind replaced the history: cached prefixes and
		// item cursors past its end are gone, and so is the vote streak.
		if old == nil {
			old, th.review = th.review, nil
		}
		th.switchedAt.items = min(th.switchedAt.items, n)
		th.observedFrom = min(th.observedFrom, n)
		th.state = policy.State{Turn: th.state.Turn}
		th.measured, th.lastTotal = false, 0
		e.ledger.Forget(th.id)
	}
	th.lastItems = len(req.Input)
	cat := e.cat.Current()
	if _, ok := cat.GroupOf(req.Model); !ok {
		return nil, nil, old, false
	}
	if first {
		e.readProject(th)
	}
	mode, source := e.routingMode(th)
	st := &step{
		th: th, meta: m, req: req, fp: fp, cfg: cfg, policy: cfg.Effective(mode), cat: cat, now: now,
		first: first, original: original, tokens: th.lastTotal,
		mode: mode, source: source, routeMode: mode,
		opts: facts.Options{
			RecentTurns:  cfg.Context.RecentTurns,
			MaxTextChars: cfg.Context.MaxTextChars,
			ObservedFrom: th.observedFrom,
			VerifiedFrom: th.switchedAt.items,
		},
	}
	if first {
		// Consumed here, under the lock, so exactly one turn takes it.
		st.pin, th.pin = th.pin, nil
	}
	if st.pin != nil || !first && th.turnPinned {
		st.routeMode = config.ModeBalanced
	}
	return st, nil, old, false
}

// routingMode is th's routing mode and where it comes from: the session
// override, the project file, the config, else balanced. Under e.mu.
func (e *Engine) routingMode(th *thread) (config.RoutingMode, ModeSource) {
	switch {
	case th != nil && th.mode != "":
		return th.mode, SourceSession
	case th != nil && th.project != "":
		return th.project, SourceProject
	case e.cfg.Mode != "":
		return e.cfg.Mode, SourceConfig
	}
	return config.ModeBalanced, SourceDefault
}

// readProject reads the mode th's project file sets. An invalid file is
// ignored, logged when its error changes, and shown as a finding. Under e.mu.
func (e *Engine) readProject(th *thread) {
	if e.projectMode == nil {
		return
	}
	mode, err := e.projectMode(th.id)
	msg := ""
	if err != nil {
		msg = err.Error()
		if msg != th.projectErr {
			e.log.Warn("route project file ignored", "thread", th.id, "err", msg)
		}
	}
	th.project, th.projectErr = mode, msg
}

// abandon drops a step whose ctx ended before it committed: the caller
// forwards the request unchanged. A first step leaves its turn unrouted, as
// a classifier failure does, and gives back the pin it took.
func (e *Engine) abandon(st *step) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.abandonLocked(st)
}

// abandonLocked is abandon under e.mu.
func (e *Engine) abandonLocked(st *step) {
	if !st.first {
		return
	}
	st.th.turnOff = true
	if st.pin != nil && st.th.pin == nil {
		st.th.pin = st.pin
	}
}

// bind binds th to its first account (plan M12). Another account resets
// what the engine learned on the old one: the cache ledger (prompt caches
// are per account), votes and escalation, the turn in progress and its
// retries and review. Intent (pause, manual, pin), the footer, the running
// tier and the tallies stay. A missing account changes nothing. Under e.mu.
func (e *Engine) bind(th *thread, account string) {
	if account == "" || account == th.account {
		return
	}
	if th.account == "" {
		th.account = account
		return
	}
	th.account = account
	if th.review != nil {
		th.review.cancel()
	}
	th.review, th.retries, th.advice, th.awaiting = nil, nil, nil, nil
	th.turn, th.turnOff = "", false
	th.state = policy.State{Turn: th.state.Turn}
	th.measured, th.lastTotal = false, 0
	e.ledger.Forget(th.id)
	e.log.Info("route account changed", "thread", th.id)
}

// classify asks the classifier for the turn's first step; a panic counts as
// a failure.
func (e *Engine) classify(ctx context.Context, st *step) (advice *classifier.Advice, err error) {
	if e.ask == nil {
		return nil, errNoClassifier
	}
	defer func() {
		if r := recover(); r != nil {
			e.log.Error("classifier failed", "thread", st.meta.ThreadID, "turn", st.meta.TurnID, "panic", panicText(r))
			advice, err = nil, errClassifierPanic
		}
	}()
	ctx, cancel := context.WithTimeout(ctx, st.cfg.DecideTimeout())
	defer cancel()
	a, err := e.ask.Ask(ctx, classifierInput(st.facts, st.cfg))
	if err != nil {
		e.log.Warn("classifier failed", "thread", st.meta.ThreadID, "turn", st.meta.TurnID, "code", err.Error())
		return nil, err
	}
	return &a, nil
}

// decideFirst decides a turn's first step. A classifier failure leaves the
// whole turn unrouted, unless the request's model cannot hold the thread and
// the baseline can (plan D6). It returns false, committing nothing, when a
// pause or a /model pick arrived while the classifier ran: the step was
// begun before them, and its decision would overwrite the footer they set.
func (e *Engine) decideFirst(st *step, advice *classifier.Advice, askErr error) (Decision, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	th := st.th
	e.noteLatency(advice)
	if e.paused || th.paused || th.manual {
		e.abandonLocked(st)
		return Decision{}, false
	}
	if askErr != nil {
		if _, fix := e.upward(st); fix {
			th.reviewedAt = th.requests
			return e.finish(st, cell{tier: st.cfg.Baseline, reason: policy.ReasonFit, state: th.state}), true
		}
		th.turnOff = true
		d := e.base(st)
		d.Tier, d.Route, d.Reason = th.tier, st.original, policy.ReasonError
		if errors.Is(askErr, errNoClassifier) {
			d.Reason = policy.ReasonNoClassifier
		}
		d.Why = whyText(d, st.cfg, st.tokens)
		th.retries = appendCapped(th.retries, retry{st.fp, d}, retriesKept)
		th.remember(d)
		return d, true
	}
	var c cell
	if st.pin != nil {
		state := th.state
		state.Turn++
		c = cell{tier: *st.pin, reason: policy.ReasonPinned, state: state, mode: st.routeMode}
		th.turnPinned = true
	} else {
		c = e.choose(st, e.incumbent(st), advice, false)
	}
	th.advice = advice
	th.state = c.state
	th.reviewedAt = th.requests
	return e.finish(st, c), true
}

// decideLater decides a later step of the turn: an arrived review answer, an
// escalation on a new repeated failure, or a move up on the turn's tool
// calls; otherwise the turn's route, refitted. It may return a review to
// start outside the lock.
func (e *Engine) decideLater(st *step) (Decision, *review) {
	e.mu.Lock()
	defer e.mu.Unlock()
	th := st.th
	cur := cell{tier: th.tier, activity: th.activity, mode: th.routeMode, reason: ReasonContinue, state: th.state}
	reviewed := false
	if r := th.review; r != nil && r.ready {
		th.review = nil
		if r.answer != nil && !th.turnPinned {
			cur = keepTurn(e.choose(st, e.incumbent(st), r.answer, true), th.state.Turn)
			th.state, th.advice, th.observedFrom, th.reviewedAt = cur.state, r.answer, r.upTo, th.requests
			reviewed = true
		}
	}
	if !reviewed && !th.turnPinned && st.facts.Failure != nil && st.facts.Failure.Signature != th.state.EscalatedSignature {
		if c := keepTurn(e.choose(st, e.incumbent(st), nil, true), th.state.Turn); c.reason == policy.ReasonEscalation {
			cur, th.state = c, c.state
		}
	}
	if !reviewed && !th.turnPinned && cur.reason != policy.ReasonEscalation {
		cur = e.observedMove(st, cur)
	}
	var job *review
	every := st.policy.ReviewEveryRequests
	if every > 0 && e.ask != nil && !th.turnPinned && th.review == nil && th.requests-th.reviewedAt >= every {
		ctx, cancel := context.WithTimeout(context.Background(), st.cfg.DecideTimeout())
		job = &review{ctx: ctx, cancel: cancel, done: make(chan struct{}), upTo: st.facts.Items}
		th.review, th.reviewedAt = job, th.requests
	}
	return e.finish(st, cur), job
}

// keepTurn undoes policy.Decide's turn count for a later step: only a turn's
// first step counts, so an escalation hold spans turns, not mid-turn reviews.
// An escalation on a later step holds from its own turn.
func keepTurn(c cell, turn int) cell {
	if c.reason == policy.ReasonEscalation {
		c.state.HoldUntilTurn -= c.state.Turn - turn
	}
	c.state.Turn = turn
	return c
}

func (e *Engine) startReview(th *thread, job *review, in classifier.Input) {
	e.reviews.Add(1)
	go func() {
		defer e.reviews.Done()
		defer close(job.done)
		defer job.cancel()
		var advice *classifier.Advice
		func() {
			defer func() {
				if r := recover(); r != nil {
					e.log.Error("classifier review failed", "thread", th.id, "panic", panicText(r))
				}
			}()
			if a, err := e.ask.Ask(job.ctx, in); err == nil {
				advice = &a
			} else {
				e.log.Warn("classifier review failed", "thread", th.id, "code", err.Error())
			}
		}()
		e.mu.Lock()
		defer e.mu.Unlock()
		e.noteLatency(advice)
		if th.review != job {
			return
		}
		if advice == nil {
			th.review = nil
			return
		}
		job.answer, job.ready = advice, true
	}()
}

// stop cancels a review and waits for it to settle, so its classifier call
// does not hold the client's slot when the next turn asks.
func (r *review) stop(ctx context.Context) {
	r.cancel()
	select {
	case <-r.done:
	case <-ctx.Done():
	}
}

// finish turns a cell into the step's decision: the group and context-fit
// guard, the upward fix (plan D6), then the thread's bookkeeping.
func (e *Engine) finish(st *step, c cell) Decision {
	th := st.th
	cfg := st.cfg
	routeFor := func(t config.Tier) config.Route { return st.routeIn(c.mode, t, c.activity) }
	window := e.window(st, routeFor)
	d := e.base(st)
	d.Tier, d.Reason, d.Estimate, d.Activity = c.tier, c.reason, c.estimate, label(c.activity, th.advice)
	d.Reading = reading(th.advice)
	refused := policy.ReasonFit
	if !st.cat.Compatible(st.original.Model, routeFor(c.tier).Model) {
		refused = policy.ReasonOutOfGroup
	}
	if tier, ok := policy.FitTier(c.tier, st.tokens, window, st.policy.ContextFill); ok {
		if tier != c.tier {
			d.Tier, d.Reason, d.Estimate = tier, refused, nil
		}
		d.Route = routeFor(tier)
	} else if fix, ok := e.upward(st); ok {
		d.Tier, d.Route, d.Reason, d.Estimate = cfg.Baseline, fix, policy.ReasonFit, nil
	} else {
		d.Route, d.Reason, d.Estimate = st.original, refused, nil
	}
	d.Rewritten = d.Route != st.original
	if d.Reason == policy.ReasonFit {
		th.stats.FitHeld++
	}

	prevTier, prev := th.tier, st.original
	if th.hasSent {
		prev = th.sent
	}
	th.tier, th.activity, th.routeMode = d.Tier, c.activity, c.mode
	th.requests++
	tax := 0
	if d.Route != prev {
		tax = e.ledger.SwitchTax(th.id, d.Route.Model, st.tokens, st.now)
		th.switchedAt = mark{requests: th.requests, items: len(st.req.Input)}
		th.stats.switched(Switch{
			At: st.now, From: prevTier, To: d.Tier,
			Activity: d.Activity, MidTurn: d.MidTurn,
		}, tax)
	} else if !th.hasSent {
		th.switchedAt = mark{requests: th.requests, items: len(st.req.Input)}
	}
	th.sent, th.hasSent = d.Route, true
	th.approx = st.tokens
	th.stepIndex++
	d.Why = whyText(d, cfg, st.tokens)
	d.SupportParts = supportParts(d, c.advice, cfg, tax)
	d.Support = supportLine(d.SupportParts)
	dd := d
	th.awaiting = &dd
	th.retries = appendCapped(th.retries, retry{st.fp, d}, retriesKept)
	if d.Route != th.lastWritten {
		th.pending = appendCapped(th.pending, d.Route, pendingWrites)
		th.lastWritten, th.wrote = d.Route, true
	}
	th.remember(d)
	classified := ""
	if c.advice != nil {
		classified = string(c.advice.Choice)
	}
	e.log.Info("route decision", "thread", d.ThreadID, "turn", d.TurnID, "step", d.Step, "tier", d.Tier,
		"activity", d.Activity, "reason", d.Reason, "model", d.Route.Model, "effort", d.Route.Effort, "rewritten", d.Rewritten,
		"mode", d.RoutingMode, "modeSource", d.RoutingModeSource, "classified", classified)
	return d
}

// window is a tier's context window under routeFor; a route the request may
// not use has none.
func (e *Engine) window(st *step, routeFor func(config.Tier) config.Route) func(config.Tier) (int, bool) {
	return func(t config.Tier) (int, bool) {
		r := routeFor(t)
		if !e.usable(st, r) {
			return 0, false
		}
		return st.cat.Window(r.Model)
	}
}

func (e *Engine) base(st *step) Decision {
	return Decision{
		ThreadID: st.th.id, TurnID: st.meta.TurnID, Step: st.th.stepIndex,
		Original: st.original, MidTurn: st.th.stepIndex > 0, At: st.now,
		RoutingMode: st.mode, RoutingModeSource: st.source,
	}
}

// usable says a route may replace the request's model: same compatibility
// group, and an effort the model accepts when the catalog lists them.
func (e *Engine) usable(st *step, r config.Route) bool {
	if !st.cat.Compatible(st.original.Model, r.Model) {
		return false
	}
	levels := st.cat.Models[r.Model].ReasoningLevels
	return r.Effort == "" || len(levels) == 0 ||
		slices.ContainsFunc(levels, func(l catalog.ReasoningLevel) bool { return l.Effort == r.Effort })
}

// upward is plan D6's fix: when the request's own model cannot hold the
// thread but the baseline route can, the request moves up to the baseline.
func (e *Engine) upward(st *step) (config.Route, bool) {
	fill := st.policy.ContextFill
	fits := func(model string) bool {
		w, ok := st.cat.Window(model)
		return ok && w > 0 && float64(st.tokens) <= float64(w)*fill
	}
	if fits(st.original.Model) {
		return config.Route{}, false
	}
	r := resolve(st.cfg.RouteFor(st.cfg.Baseline, ""), st.sent())
	return r, e.usable(st, r) && fits(r.Model)
}

// noteLatency keeps a classifier answer's latency for the median.
func (e *Engine) noteLatency(a *classifier.Advice) {
	if a != nil && a.Latency > 0 {
		e.latencies = appendCapped(e.latencies, a.Latency, latenciesKept)
	}
}

// knows says Codex may run on r without a /model pick: the route the
// app-server last reported, one the router wrote, echoed or not, or the
// thread's native route.
func (th *thread) knows(r config.Route) bool {
	return sameRoute(r, th.footer) || sameRoute(r, th.lastWritten) || sameRoute(r, th.native) || th.pendingIndex(r, sameRoute) >= 0
}

// pendingIndex is the first pending write that r matches under same, or -1.
func (th *thread) pendingIndex(r config.Route, same func(a, b config.Route) bool) int {
	return slices.IndexFunc(th.pending, func(p config.Route) bool { return same(r, p) })
}

// sameRoute matches a request's route to a settings route. An empty effort
// is the model's default, which a request may name.
func sameRoute(a, b config.Route) bool {
	return a.Model == b.Model && (a.Effort == b.Effort || a.Effort == "" || b.Effort == "")
}

// sameSetting matches a route the app-server reports to one the thread's
// settings held. An empty effort is the model's default: it matches the
// default the catalog lists for the model and no other effort, so a /model
// pick of another effort is not taken for an echo. Under e.mu.
func (e *Engine) sameSetting(a, b config.Route) bool {
	def := e.cat.Current().Models[a.Model].DefaultEffort
	orDefault := func(x config.Effort) config.Effort {
		if x == "" {
			return def
		}
		return x
	}
	return a.Model == b.Model && orDefault(a.Effort) == orDefault(b.Effort)
}

// holdStep keeps a step of a turn that started held on the route the
// thread is held on: the seeded route its footer shows (M11). After a brain
// restart mid-turn the turn's steps still carry the settings the turn
// started with, while the old brain routed them; forwarding them unchanged
// would switch models mid-turn and write a cold prefix. Nil when the step
// already has that route or the route cannot replace its model. An empty
// effort (the model's default) keeps the effort Codex sent. Under e.mu.
func (e *Engine) holdStep(th *thread, original config.Route) *Decision {
	r := th.lastWritten
	if !th.heldTurn || r.Model == "" || sameRoute(original, r) || !e.cat.Current().Compatible(original.Model, r.Model) {
		return nil
	}
	e.log.Debug("route hold step", "thread", th.id, "model", r.Model, "effort", r.Effort)
	return &Decision{ThreadID: th.id, TurnID: th.turn, Original: original, Route: r, Rewritten: true}
}

// inConversation says the input holds an earlier turn's work: an assistant
// message, reasoning, or a tool call or its output.
func inConversation(items []responses.Item) bool {
	return slices.ContainsFunc(items, func(it responses.Item) bool {
		switch it.Type {
		case responses.TypeMessage:
			return it.Role == "assistant"
		case responses.TypeReasoning, responses.TypeFunctionCall, responses.TypeFunctionCallOutput,
			responses.TypeCustomToolCall, responses.TypeCustomToolCallOutput, responses.TypeLocalShellCall:
			return true
		}
		// Developer and user messages and item types this code does not
		// know (Codex 0.162.1 opens every thread with additional_tools) are
		// context, not a model's reply.
		return false
	})
}

func (th *thread) remember(d Decision) {
	th.last = &d
	th.recent = appendCapped(th.recent, d, RecentKept)
}

// thread returns the thread for key, creating it and evicting the least
// recently used one past MaxThreads.
func (e *Engine) thread(key string) *thread {
	e.mu.Lock()
	defer e.mu.Unlock()
	if el, ok := e.threads[key]; ok {
		e.lru.MoveToFront(el)
		return el.Value.(*thread)
	}
	return e.add(key)
}

// add creates the thread for key under e.mu. Past MaxThreads it evicts the
// least recently used thread that carries no intent: a pause, pin or manual
// hold is the user's word and outlives the bound, so the count can exceed
// MaxThreads by the threads with intent.
func (e *Engine) add(key string) *thread {
	th := &thread{id: key, stats: newTally()}
	if e.loadMode != nil {
		th.mode = e.loadMode(key)
	}
	e.readProject(th)
	e.threads[key] = e.lru.PushFront(th)
	for el := e.lru.Back(); el != nil && e.lru.Len() > MaxThreads; {
		prev := el.Prev()
		if old := el.Value.(*thread); old != th && !old.hasIntent() {
			e.lru.Remove(el)
			delete(e.threads, old.id)
			e.ledger.Forget(old.id)
			if old.review != nil {
				old.review.cancel()
			}
		}
		el = prev
	}
	return th
}

// hasIntent says the thread holds a pause, a pin or a manual hold.
func (th *thread) hasIntent() bool { return th.paused || th.manual || th.pin != nil }

func (e *Engine) lookup(key string) *thread {
	if el, ok := e.threads[key]; ok {
		return el.Value.(*thread)
	}
	return nil
}

// passThrough notes that a thread's next reply is not a routed one.
func (e *Engine) passThrough(key string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if th := e.lookup(key); th != nil {
		th.awaiting = nil
	}
}

// Observe records a reply: the cache ledger, the thread's size, and the
// session stats when the reply answers a routed step.
func (e *Engine) Observe(m responses.Meta, served string, u responses.Usage, status int) {
	defer func() {
		if r := recover(); r != nil {
			e.log.Error("route observe failed", "thread", m.ThreadID, "panic", panicText(r))
		}
	}()
	key := threadKey(m)
	if status < 200 || status > 299 || key == "" {
		return
	}
	e.emitSnapshot(e.observe(key, m, served, u))
}

func (e *Engine) observe(key string, m responses.Meta, served string, u responses.Usage) Snapshot {
	e.mu.Lock()
	defer e.mu.Unlock()
	now := e.now()
	th := e.lookup(key)
	model := served
	var d *Decision
	if th != nil {
		e.bind(th, m.Account)
		th.lastActive = now
		d, th.awaiting = th.awaiting, nil
		if d != nil && d.TurnID != m.TurnID {
			d = nil
		}
		if d != nil && (served == "" || sameModel(d.Route.Model, served)) {
			model = d.Route.Model
		}
		if u.InputTokens > 0 {
			if th.held && th.turn != m.TurnID {
				// The hold ends on a turn this engine never saw start (a
				// brain restarted mid-turn): that turn's later steps stay
				// unrouted, as for a turn that started held.
				th.turn, th.turnOff, th.heldTurn = m.TurnID, true, true
			}
			th.measured, th.held = true, false
			th.lastTotal = max(u.TotalTokens, u.InputTokens+u.OutputTokens)
		}
	}
	if model != "" && u.InputTokens > 0 {
		e.ledger.Observe(key, model, u, now)
	}
	if d != nil {
		priced := *d
		priced.Route.Model = model
		if _, ok := e.cfg.Prices[served]; ok {
			priced.Route.Model = served
		}
		th.stats.reply(priced, u, now, e.cfg.Prices, th.native.Model)
	}
	return e.snapshot(th, now)
}

func (e *Engine) publish(d Decision) {
	e.emit(func() {
		if e.sink != nil {
			e.sink.OnDecision(copyDecision(d))
		}
	})
	e.emitSnapshot(e.lockedSnapshot(d.ThreadID))
}

func (e *Engine) emitSnapshot(s Snapshot) {
	e.emit(func() {
		if e.sink != nil {
			e.sink.OnSnapshot(e.withHealth(s))
		}
	})
}

// emit calls the sink outside every lock; a sink panic is logged, never
// passed on to the request.
func (e *Engine) emit(f func()) {
	defer func() {
		if r := recover(); r != nil {
			e.log.Error("route sink failed", "panic", panicText(r))
		}
	}()
	f()
}

func (e *Engine) health() classifier.Health {
	if e.ask == nil {
		return classifier.Health{}
	}
	return e.ask.Health()
}

func rewriteOf(d Decision) (responses.Rewrite, bool) {
	if !d.Rewritten {
		return responses.Rewrite{}, false
	}
	return responses.Rewrite{Model: d.Route.Model, Effort: d.Route.Effort}, true
}

func threadKey(m responses.Meta) string {
	if m.ThreadID != "" {
		return m.ThreadID
	}
	return m.SessionID
}

// sameModel allows a dated snapshot slug for the requested model.
func sameModel(requested, served string) bool {
	return served == requested || len(served) > len(requested) && served[:len(requested)+1] == requested+"-"
}

func classifierInput(f facts.Facts, cfg config.Config) classifier.Input {
	msgs := make([]classifier.Message, len(f.Dialogue))
	for i, t := range f.Dialogue {
		msgs[i] = classifier.Message{Role: t.Role, Text: t.Text}
	}
	return classifier.Input{Prompt: f.Prompt, Dialogue: msgs, Routes: maps.Clone(cfg.Routes)}
}

// panicText names a panic without echoing arbitrary values, which might
// carry request text: a runtime error's message, else the value's type.
func panicText(r any) string {
	if err, ok := r.(runtime.Error); ok {
		return err.Error()
	}
	return fmt.Sprintf("%T", r)
}

func appendCapped[T any](s []T, v T, limit int) []T {
	s = append(s, v)
	if len(s) > limit {
		s = slices.Clone(s[len(s)-limit:])
	}
	return s
}
