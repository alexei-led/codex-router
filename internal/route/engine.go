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
	"github.com/alexei-led/codex-router/internal/policy"
	"github.com/alexei-led/codex-router/internal/responses"
)

// Reasons the engine adds to policy's: activity moves inside a tier, a move
// up on the turn's own tool calls, and a later step keeping the turn's route.
const (
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

// Classifier deadline: the configured timeout plus the margin the proxy adds
// to its decision deadline; the fallback is the default classifier's timeout.
const (
	askMarginMs   = 200
	fallbackAskMs = 1500
)

var (
	errNoClassifier    = errors.New("no classifier")
	errClassifierPanic = errors.New("classifier panic")
)

// Engine routes user steps. All state lives under mu; step serializes the
// requests of one thread so a classifier call never runs under mu.
type Engine struct {
	cat    catalog.Source
	ask    classifier.Asker
	now    func() time.Time
	sink   Sink
	log    *slog.Logger
	ledger *cost.Ledger
	// reviews counts background classifier reviews in flight.
	reviews sync.WaitGroup

	mu      sync.Mutex
	cfg     config.Config
	threads map[string]*list.Element
	lru     *list.List
	focus   *thread
	paused  bool
	pin     *config.Tier
	last    *Decision
	recent  []Decision
	stats   tally
	// latencies are the recent classifier answers' latencies.
	latencies []time.Duration
}

// thread is one Codex thread's routing state. step is held for a whole
// Decide; every other field is guarded by Engine.mu.
type thread struct {
	id   string
	step sync.Mutex

	native   config.Route // what Codex sent first: its configured route
	model    string       // the last request's model
	tier     config.Tier
	activity config.Activity // the activity whose route runs, "" for the base route
	state    policy.State
	measured bool // a reply on this thread was observed

	turn       string
	stepIndex  int
	turnPinned bool
	turnOff    bool // the classifier failed on the turn's first step
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

// New returns an engine. A nil Now uses time.Now, a nil Logger discards.
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
	return &Engine{
		cat:     d.Catalog,
		ask:     d.Classifier,
		now:     d.Now,
		sink:    d.Sink,
		log:     d.Logger,
		ledger:  cost.NewLedger(cost.Cache{TTL: ms(d.Config.Cache.TTLMs), WarmMargin: ms(d.Config.Cache.WarmMarginMs)}),
		cfg:     d.Config,
		threads: map[string]*list.Element{},
		lru:     list.New(),
		stats:   newTally(),
	}
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
}

func (s *step) sent() config.Effort { return s.req.Effort }

// Decide returns the rewrite for one request, or false to forward it
// unchanged. Any panic or error forwards unchanged.
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

	st, retried, old := e.begin(th, req, m)
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

	var d Decision
	if st.first {
		var advice *classifier.Advice
		var err error
		if st.pin == nil {
			advice, err = e.classify(ctx, st)
		}
		d = e.decideFirst(st, advice, err)
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

// begin reads the thread under the lock. It returns a pinned decision for a
// retry, nil step when the request passes unchanged, and a review the new
// turn replaced, for the caller to stop outside the lock.
func (e *Engine) begin(th *thread, req *responses.Request, m responses.Meta) (*step, *Decision, *review) {
	e.mu.Lock()
	defer e.mu.Unlock()
	cfg := e.cfg
	original := config.Route{Model: req.Model, Effort: req.Effort}
	if th.native.Model == "" {
		th.native, th.lastWritten, th.footer = original, original, original
		th.tier, th.activity = cellForModel(cfg, original)
	}
	th.model = req.Model
	e.focus = th
	if e.paused || th.manual {
		th.awaiting = nil
		return nil, nil, nil
	}
	fp := req.Fingerprint(m)
	for _, r := range th.retries {
		if r.fingerprint == fp {
			d := r.decision
			return nil, &d, nil
		}
	}
	th.awaiting = nil
	var old *review
	first := th.turn != m.TurnID
	if first {
		old, th.review = th.review, nil
		th.turn, th.stepIndex, th.turnPinned, th.turnOff, th.advice = m.TurnID, 0, false, false, nil
		th.retries, th.observedFrom = nil, 0
	} else if th.turnOff {
		return nil, nil, nil
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
		return nil, nil, old
	}
	st := &step{
		th: th, meta: m, req: req, fp: fp, cfg: cfg, policy: cfg.Effective(), cat: cat, now: e.now(),
		first: first, original: original, tokens: th.lastTotal,
		opts: facts.Options{
			RecentTurns:  cfg.Context.RecentTurns,
			MaxTextChars: cfg.Context.MaxTextChars,
			ObservedFrom: th.observedFrom,
			VerifiedFrom: th.switchedAt.items,
		},
	}
	if first && e.pin != nil {
		p := *e.pin
		st.pin = &p
	}
	return st, nil, old
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
	ctx, cancel := context.WithTimeout(ctx, askTimeout(st.cfg))
	defer cancel()
	a, err := e.ask.Ask(ctx, classifierInput(st.facts, st.cfg))
	if err != nil {
		e.log.Warn("classifier failed", "thread", st.meta.ThreadID, "turn", st.meta.TurnID, "code", err.Error())
		return nil, err
	}
	return &a, nil
}

// askTimeout bounds a classifier call on the engine's side as well, so a
// caller without a deadline cannot hold a step past the classifier's own
// timeout plus the proxy's margin (plan Data flow step 2).
func askTimeout(cfg config.Config) time.Duration {
	ms := cfg.Classifiers[cfg.Classifier].TimeoutMs
	if ms <= 0 {
		ms = fallbackAskMs
	}
	return time.Duration(ms+askMarginMs) * time.Millisecond
}

// decideFirst decides a turn's first step. A classifier failure leaves the
// whole turn unrouted, unless the request's model cannot hold the thread and
// the baseline can (plan D6).
func (e *Engine) decideFirst(st *step, advice *classifier.Advice, askErr error) Decision {
	e.mu.Lock()
	defer e.mu.Unlock()
	th := st.th
	e.noteLatency(advice)
	if askErr != nil {
		if _, fix := e.upward(st); fix {
			th.reviewedAt = th.requests
			return e.finish(st, cell{tier: st.cfg.Baseline, reason: policy.ReasonFit, state: th.state})
		}
		th.turnOff = true
		d := e.base(st)
		d.Tier, d.Route, d.Reason = th.tier, st.original, policy.ReasonError
		d.Why = whyText(d, st.cfg, st.tokens)
		th.retries = appendCapped(th.retries, retry{st.fp, d}, retriesKept)
		e.remember(d)
		return d
	}
	var c cell
	if st.pin != nil {
		state := th.state
		state.Turn++
		c = cell{tier: *st.pin, reason: policy.ReasonPinned, state: state}
		th.turnPinned = true
		if e.pin != nil && *e.pin == *st.pin {
			e.pin = nil
		}
	} else {
		c = e.choose(st, e.incumbent(st), advice, false)
	}
	th.advice = advice
	th.state = c.state
	th.reviewedAt = th.requests
	return e.finish(st, c)
}

// decideLater decides a later step of the turn: an arrived review answer, an
// escalation on a new repeated failure, or a move up on the turn's tool
// calls; otherwise the turn's route, refitted. It may return a review to
// start outside the lock.
func (e *Engine) decideLater(st *step) (Decision, *review) {
	e.mu.Lock()
	defer e.mu.Unlock()
	th := st.th
	cur := cell{tier: th.tier, activity: th.activity, reason: ReasonContinue, state: th.state}
	reviewed := false
	if r := th.review; r != nil && r.ready {
		th.review = nil
		if r.answer != nil && !th.turnPinned {
			cur = e.choose(st, e.incumbent(st), r.answer, true)
			th.state, th.advice, th.observedFrom, th.reviewedAt = cur.state, r.answer, r.upTo, th.requests
			reviewed = true
		}
	}
	if !reviewed && !th.turnPinned && st.facts.Failure != nil && st.facts.Failure.Signature != th.state.EscalatedSignature {
		if c := e.choose(st, e.incumbent(st), nil, true); c.reason == policy.ReasonEscalation {
			cur, th.state = c, c.state
		}
	}
	if !reviewed && !th.turnPinned && cur.reason != policy.ReasonEscalation {
		cur = e.observedMove(st, cur)
	}
	var job *review
	every := st.policy.ReviewEveryRequests
	if every > 0 && e.ask != nil && !th.turnPinned && th.review == nil && th.requests-th.reviewedAt >= every {
		ctx, cancel := context.WithTimeout(context.Background(), askTimeout(st.cfg))
		job = &review{ctx: ctx, cancel: cancel, done: make(chan struct{}), upTo: st.facts.Items}
		th.review, th.reviewedAt = job, th.requests
	}
	return e.finish(st, cur), job
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
	routeFor := func(t config.Tier) config.Route { return resolve(cfg.RouteFor(t, c.activity), st.sent()) }
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
		e.stats.FitHeld++
	}

	prevTier, prev := th.tier, st.original
	if th.hasSent {
		prev = th.sent
	}
	th.tier, th.activity = d.Tier, c.activity
	th.requests++
	tax := 0
	if d.Route != prev {
		tax = e.ledger.SwitchTax(th.id, d.Route.Model, st.tokens, st.now)
		th.switchedAt = mark{requests: th.requests, items: len(st.req.Input)}
		e.stats.switched(Switch{
			At: st.now, From: prevTier, To: d.Tier, FromRoute: prev, ToRoute: d.Route,
			Activity: d.Activity, MidTurn: d.MidTurn,
		}, tax)
	} else if !th.hasSent {
		th.switchedAt = mark{requests: th.requests, items: len(st.req.Input)}
	}
	th.sent, th.hasSent = d.Route, true
	th.approx = st.tokens
	th.stepIndex++
	d.Why, d.Support = whyText(d, cfg, st.tokens), supportText(d, c.advice, cfg, tax)
	dd := d
	th.awaiting = &dd
	th.retries = appendCapped(th.retries, retry{st.fp, d}, retriesKept)
	if d.Route != th.lastWritten {
		th.pending = appendCapped(th.pending, d.Route, pendingWrites)
		th.lastWritten, th.wrote = d.Route, true
	}
	e.remember(d)
	e.log.Info("route decision", "thread", d.ThreadID, "turn", d.TurnID, "step", d.Step, "tier", d.Tier,
		"activity", d.Activity, "reason", d.Reason, "model", d.Route.Model, "effort", d.Route.Effort, "rewritten", d.Rewritten)
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
		ThreadID: st.th.id, TurnID: st.meta.TurnID, Step: st.th.stepIndex, Kind: responses.KindUserStep,
		Original: st.original, MidTurn: st.th.stepIndex > 0, At: st.now,
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

func (e *Engine) remember(d Decision) {
	e.last = &d
	e.recent = appendCapped(e.recent, d, RecentKept)
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
	th := &thread{id: key}
	e.threads[key] = e.lru.PushFront(th)
	for e.lru.Len() > MaxThreads {
		el := e.lru.Back()
		old := el.Value.(*thread)
		e.lru.Remove(el)
		delete(e.threads, old.id)
		e.ledger.Forget(old.id)
		if old.review != nil {
			old.review.cancel()
		}
	}
	return th
}

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
		d, th.awaiting = th.awaiting, nil
		if d != nil && d.TurnID != m.TurnID {
			d = nil
		}
		if d != nil && (served == "" || sameModel(d.Route.Model, served)) {
			model = d.Route.Model
		}
		if u.InputTokens > 0 {
			th.measured = true
			th.lastTotal = max(u.TotalTokens, u.InputTokens+u.OutputTokens)
		}
	}
	if model != "" && u.InputTokens > 0 {
		e.ledger.Observe(key, model, u, now)
	}
	if d != nil {
		e.stats.reply(*d, u, now, e.cfg.Prices, th.native.Model)
	}
	return e.snapshot(now)
}

func (e *Engine) publish(d Decision) {
	e.emit(func() {
		if e.sink != nil {
			e.sink.OnDecision(d)
		}
	})
	e.emitSnapshot(e.Snapshot())
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
