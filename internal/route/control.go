package route

import (
	"errors"
	"fmt"
	"slices"

	"github.com/alexei-led/codex-router/internal/config"
	"github.com/alexei-led/codex-router/internal/policy"
)

var errCommandPanic = errors.New("command failed")

// Apply runs a control command: pin or unpin the next turn's tier, pause or
// resume routing, or pick a policy preset. Pause writes each routed thread's
// own route back to its settings; resume also ends manual mode. A panic is
// logged and returned as an error.
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
	e.log.Info("route command", "op", c.Op, "tier", c.Tier, "preset", c.Preset)
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
	var writes []Decision
	switch c.Op {
	case OpPin:
		if _, ok := e.cfg.Routes[c.Tier]; !ok || !slices.Contains(config.Tiers, c.Tier) {
			return nil, Snapshot{}, fmt.Errorf("pin: unknown tier %q", c.Tier)
		}
		t := c.Tier
		e.pin = &t
	case OpUnpin:
		e.pin = nil
	case OpPause:
		if !e.paused {
			e.paused = true
			writes = e.restoreNative()
		}
	case OpResume:
		e.paused = false
		for el := e.lru.Front(); el != nil; el = el.Next() {
			el.Value.(*thread).manual = false
		}
	case OpPreset:
		if !slices.Contains([]config.Preset{config.PresetThrifty, config.PresetBalanced, config.PresetQuality}, c.Preset) {
			return nil, Snapshot{}, fmt.Errorf("preset: unknown preset %q", c.Preset)
		}
		e.cfg.Preset = c.Preset
	case OpSnapshot:
	default:
		return nil, Snapshot{}, fmt.Errorf("unknown op %q", c.Op)
	}
	return writes, e.snapshot(e.now()), nil
}

// restoreNative returns a settings write per thread whose footer shows a
// route the router chose, back to the route Codex itself sent.
func (e *Engine) restoreNative() []Decision {
	var out []Decision
	now := e.now()
	for el := e.lru.Front(); el != nil; el = el.Next() {
		th := el.Value.(*thread)
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
// router wrote is its echo; any other value after the router's first write
// is the user's own pick (plan D12): the thread goes manual until resume. A
// panic is logged and dropped.
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
	case !th.wrote || r == th.lastWritten:
	default:
		// The pick is now what Codex would use without the router, so pause
		// restores it rather than the route the session started on.
		th.manual, th.pending, th.lastWritten, th.native = true, nil, r, r
		e.log.Info("route manual", "thread", th.id, "model", r.Model, "effort", r.Effort)
	}
	return e.snapshot(e.now()), true
}
