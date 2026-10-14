package route

import (
	"errors"
	"fmt"
	"slices"

	"github.com/alexei-led/codex-router/internal/config"
	"github.com/alexei-led/codex-router/internal/policy"
)

var errCommandPanic = errors.New("command failed")

// Intent is the user's routing intent, which a restarted brain restores
// (plan M11): the home's pause and the preset a command picked, and per
// thread its pause, a manual hold (a /model pick) and a pending pin. A
// preset from the config is not intent: a restart reads the config again. Learned state (tiers,
// votes, escalations, the cache ledger) is not intent. Rev orders exports:
// a later state has a higher Rev.
type Intent struct {
	Rev     uint64                  `json:"-"`
	Paused  bool                    `json:"paused,omitempty"`
	Preset  config.Preset           `json:"preset,omitempty"`
	Threads map[string]ThreadIntent `json:"threads,omitempty"`
}

// ThreadIntent is one thread's part of the intent.
type ThreadIntent struct {
	Paused bool         `json:"paused,omitempty"`
	Manual bool         `json:"manual,omitempty"`
	Pin    *config.Tier `json:"pin,omitempty"`
}

// Intent exports the engine's current intent.
func (e *Engine) Intent() Intent {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.intent()
}

// intent exports the intent under e.mu; only threads with some intent are
// listed.
func (e *Engine) intent() Intent {
	e.intentRev++
	in := Intent{Rev: e.intentRev, Paused: e.paused}
	if e.presetSet {
		in.Preset = e.cfg.Preset
	}
	for _, th := range e.all() {
		if !th.hasIntent() {
			continue
		}
		ti := ThreadIntent{Paused: th.paused, Manual: th.manual}
		if th.pin != nil {
			p := *th.pin
			ti.Pin = &p
		}
		if in.Threads == nil {
			in.Threads = map[string]ThreadIntent{}
		}
		in.Threads[th.id] = ti
	}
	return in
}

// restore starts the engine from a saved intent. A lost intent pauses the
// home, so nothing is routed against a pause the user may have set, and
// flags every session until a home resume. Invalid pins and presets are
// dropped.
func (e *Engine) restore(in Intent, lost bool) {
	if lost {
		e.paused, e.lost = true, true
		return
	}
	e.paused = in.Paused
	if validPreset(in.Preset) {
		e.cfg.Preset, e.presetSet = in.Preset, true
	}
	for id, ti := range in.Threads {
		if id == "" {
			continue
		}
		th := e.add(id)
		th.paused, th.manual = ti.Paused, ti.Manual
		if ti.Pin != nil && e.validTier(*ti.Pin) {
			t := *ti.Pin
			th.pin = &t
		}
	}
}

// persist hands the intent to Deps.SaveIntent after a change, outside the
// engine lock. Exports may reach it out of order; Rev tells the newest.
// While the saved intent is lost nothing is saved: the pause is the
// engine's own, and a save would overwrite the unreadable file with it, so a
// restart would show a plain pause instead of the lost state. A home resume
// clears the flag and saves.
func (e *Engine) persist() {
	if e.saveIntent == nil {
		return
	}
	e.mu.Lock()
	if e.lost {
		e.mu.Unlock()
		return
	}
	in := e.intent()
	e.mu.Unlock()
	e.emit(func() { e.saveIntent(in) })
}

func (e *Engine) validTier(t config.Tier) bool {
	_, ok := e.cfg.Routes[t]
	return ok && slices.Contains(config.Tiers, t)
}

func validPreset(p config.Preset) bool {
	return slices.Contains([]config.Preset{config.PresetThrifty, config.PresetBalanced, config.PresetQuality}, p)
}

// Apply runs a control command: pin or unpin a session's next turn, pause
// or resume a session or the whole home, or pick the home's policy preset.
// Pause writes each affected routed thread's own route back to its settings;
// resume also ends manual mode. A home resume keeps session pauses and
// clears a lost intent. Every command but snapshot saves the intent. A panic
// is logged and returned as an error.
func (e *Engine) Apply(c Command) (err error) {
	defer func() {
		if r := recover(); r != nil {
			e.log.Error("route command failed", "op", c.Op, "panic", panicText(r))
			err = fmt.Errorf("%s: %w", c.Op, errCommandPanic)
		}
	}()
	writes, s, err := e.apply(c)
	if err != nil {
		return err
	}
	e.log.Info("route command", "op", c.Op, "scope", scopeOf(c), "thread", c.Session, "tier", c.Tier, "preset", c.Preset)
	if c.Op != OpSnapshot {
		e.persist()
	}
	for _, d := range writes {
		e.emit(func() {
			if e.sink != nil {
				e.sink.OnDecision(d)
			}
		})
	}
	e.emitSnapshot(s)
	return nil
}

func (e *Engine) apply(c Command) ([]Decision, Snapshot, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if c.Scope != "" && c.Scope != ScopeSession && c.Scope != ScopeHome {
		return nil, Snapshot{}, fmt.Errorf("%s: unknown scope %q", c.Op, c.Scope)
	}
	var writes []Decision
	switch c.Op {
	case OpPin:
		if !e.validTier(c.Tier) {
			return nil, Snapshot{}, fmt.Errorf("pin: unknown tier %q", c.Tier)
		}
		th, err := e.session(c)
		if err != nil {
			return nil, Snapshot{}, err
		}
		t := c.Tier
		th.pin = &t
	case OpUnpin:
		th, err := e.session(c)
		if err != nil {
			return nil, Snapshot{}, err
		}
		th.pin = nil
	case OpPause:
		if c.home() {
			if !e.paused {
				e.paused = true
				writes = e.restoreNative(e.all()...)
			}
			break
		}
		th, err := e.session(c)
		if err != nil {
			return nil, Snapshot{}, err
		}
		if !th.paused {
			th.paused = true
			writes = e.restoreNative(th)
		}
	case OpResume:
		if c.home() {
			e.paused, e.lost = false, false
			for _, th := range e.all() {
				th.manual = false
			}
			break
		}
		th, err := e.session(c)
		if err != nil {
			return nil, Snapshot{}, err
		}
		th.paused, th.manual = false, false
	case OpPreset:
		if !c.home() {
			return nil, Snapshot{}, errors.New("preset: home scope only")
		}
		if !validPreset(c.Preset) {
			return nil, Snapshot{}, fmt.Errorf("preset: unknown preset %q", c.Preset)
		}
		e.cfg.Preset, e.presetSet = c.Preset, true
	case OpSnapshot:
	default:
		return nil, Snapshot{}, fmt.Errorf("unknown op %q", c.Op)
	}
	th := e.latest()
	if c.Session != "" {
		th = e.lookup(c.Session)
	}
	return writes, e.snapshot(th, e.now()), nil
}

// session returns the thread a session command targets, creating it so a
// pin or pause set before the thread's first step holds for it.
func (e *Engine) session(c Command) (*thread, error) {
	if c.Scope == ScopeHome || c.Session == "" {
		return nil, fmt.Errorf("%s: needs a session", c.Op)
	}
	if th := e.lookup(c.Session); th != nil {
		return th, nil
	}
	return e.add(c.Session), nil
}

func scopeOf(c Command) Scope {
	if c.home() {
		return ScopeHome
	}
	return ScopeSession
}

// all returns every thread, most recently used first.
func (e *Engine) all() []*thread {
	out := make([]*thread, 0, e.lru.Len())
	for el := e.lru.Front(); el != nil; el = el.Next() {
		out = append(out, el.Value.(*thread))
	}
	return out
}

// restoreNative returns a settings write per thread whose footer shows a
// route the router chose, back to the route Codex itself sent. An empty
// native effort (the model's default, as thread/resume reports it) is sent
// as the catalog's default: the app-server keeps the current effort when a
// write omits it, so the router's effort would stay.
func (e *Engine) restoreNative(ths ...*thread) []Decision {
	var out []Decision
	now := e.now()
	for _, th := range ths {
		if th.manual || !th.wrote || e.sameSetting(th.lastWritten, th.native) {
			continue
		}
		r := th.native
		if r.Effort == "" {
			r.Effort = e.cat.Current().Models[r.Model].DefaultEffort
		}
		out = append(out, Decision{
			ThreadID: th.id, TurnID: th.turn, Tier: th.tier, Route: r, Original: th.lastWritten,
			Reason: policy.ReasonPaused, Why: "routing paused", At: now,
		})
		th.pending = appendCapped(th.pending, r, pendingWrites)
		th.lastWritten = r
	}
	return out
}

// SettingsUpdated takes a thread/settings/updated notification. A value the
// router wrote is its echo; any other value is the user's own pick (plan
// D12): the thread goes manual until resume, and the intent is saved. A
// panic is logged and dropped.
func (e *Engine) SettingsUpdated(threadID string, r config.Route) {
	defer func() {
		if p := recover(); p != nil {
			e.log.Error("route settings update failed", "thread", threadID, "panic", panicText(p))
		}
	}()
	s, ok, manual := e.settingsUpdated(threadID, r)
	if manual {
		e.persist()
	}
	if ok {
		e.emitSnapshot(s)
	}
}

// settingsUpdated applies the update under the lock; manual reports that
// the thread just went manual.
func (e *Engine) settingsUpdated(threadID string, r config.Route) (s Snapshot, ok, manual bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	th := e.lookup(threadID)
	if th == nil {
		return Snapshot{}, false, false
	}
	th.footer = r
	switch i := th.pendingIndex(r, e.sameSetting); {
	case i >= 0:
		th.pending = slices.Clone(th.pending[i+1:])
	case th.manual:
		th.lastWritten, th.native = r, r
	case e.sameSetting(r, th.lastWritten):
		// lastWritten starts as the native route, which the launcher writes
		// first to subscribe, so that write's echo lands here too.
	default:
		e.pick(th, r)
		manual = true
	}
	return e.snapshot(th, e.now()), true, manual
}
