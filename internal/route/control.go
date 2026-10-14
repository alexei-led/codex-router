package route

import (
	"errors"
	"fmt"
	"slices"

	"github.com/alexei-led/codex-router/internal/config"
	"github.com/alexei-led/codex-router/internal/policy"
)

var errCommandPanic = errors.New("command failed")

// Apply runs a control command: pin or unpin a session's next turn, pause
// or resume a session or the whole home, or pick the home's policy preset.
// Pause writes each affected routed thread's own route back to its settings;
// resume also ends manual mode. A home resume keeps session pauses. A panic
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
		if _, ok := e.cfg.Routes[c.Tier]; !ok || !slices.Contains(config.Tiers, c.Tier) {
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
			e.paused = false
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
		if !slices.Contains([]config.Preset{config.PresetThrifty, config.PresetBalanced, config.PresetQuality}, c.Preset) {
			return nil, Snapshot{}, fmt.Errorf("preset: unknown preset %q", c.Preset)
		}
		e.cfg.Preset = c.Preset
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
// route the router chose, back to the route Codex itself sent.
func (e *Engine) restoreNative(ths ...*thread) []Decision {
	var out []Decision
	now := e.now()
	for _, th := range ths {
		if th.manual || !th.wrote || th.lastWritten == th.native {
			continue
		}
		out = append(out, Decision{
			ThreadID: th.id, TurnID: th.turn, Tier: th.tier, Route: th.native, Original: th.lastWritten,
			Reason: policy.ReasonPaused, Why: "routing paused", At: now,
		})
		th.pending = appendCapped(th.pending, th.native, pendingWrites)
		th.lastWritten = th.native
	}
	return out
}

// SettingsUpdated takes a thread/settings/updated notification. A value the
// router wrote is its echo; any other value is the user's own pick (plan
// D12): the thread goes manual until resume. A panic is logged and dropped.
func (e *Engine) SettingsUpdated(threadID string, r config.Route) {
	defer func() {
		if p := recover(); p != nil {
			e.log.Error("route settings update failed", "thread", threadID, "panic", panicText(p))
		}
	}()
	if s, ok := e.settingsUpdated(threadID, r); ok {
		e.emitSnapshot(s)
	}
}

func (e *Engine) settingsUpdated(threadID string, r config.Route) (Snapshot, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	th := e.lookup(threadID)
	if th == nil {
		return Snapshot{}, false
	}
	th.footer = r
	switch i := slices.Index(th.pending, r); {
	case i >= 0:
		th.pending = slices.Clone(th.pending[i+1:])
	case th.manual:
		th.lastWritten, th.native = r, r
	case r == th.lastWritten:
		// lastWritten starts as the native route, which the launcher writes
		// first to subscribe, so that write's echo lands here too.
	default:
		// The pick is now what Codex would use without the router, so pause
		// restores it rather than the route the session started on.
		th.manual, th.pending, th.lastWritten, th.native = true, nil, r, r
		e.log.Info("route manual", "thread", th.id, "model", r.Model, "effort", r.Effort)
	}
	return e.snapshot(th, e.now()), true
}
