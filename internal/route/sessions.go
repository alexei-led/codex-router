package route

import (
	"cmp"
	"slices"
	"time"

	"github.com/alexei-led/codex-router/internal/config"
	"github.com/alexei-led/codex-router/internal/home"
)

// Attention flags a session the user may want to look at.
type Attention string

const (
	AttentionPaused     Attention = "paused"     // this session or its home is paused
	AttentionManual     Attention = "manual"     // the footer model was picked with /model
	AttentionEscalation Attention = "escalation" // a repeated failure holds a higher tier
)

// SessionInfo is one session's summary for the sessions list: a Codex main
// thread of this engine's home. ID is "<home id>/<thread id>". Every number
// comes from the thread's own tallies. The USD fields are set only when
// prices are configured.
type SessionInfo struct {
	ID           string        `json:"id"`
	Home         string        `json:"home"`
	ThreadID     string        `json:"thread_id"`
	Label        string        `json:"label"`
	Cwd          string        `json:"cwd"`
	Mode         Mode          `json:"mode"`
	Tier         config.Tier   `json:"tier"`
	Route        config.Route  `json:"route"`
	Replies      int           `json:"replies"`
	Switches     int           `json:"switches"`
	Strip        []config.Tier `json:"strip"`
	LastSwitch   *Switch       `json:"last_switch,omitempty"`
	LastActive   time.Time     `json:"last_active"`
	Attention    Attention     `json:"attention,omitempty"`
	FooterSynced bool          `json:"footer_synced"`
	SavedUSD     *float64      `json:"saved_usd,omitempty"`
	SpentUSD     *float64      `json:"spent_usd,omitempty"`
}

// sessionIdle is how long a thread may go without a user step or reply
// before the sessions list hides it, so a thread left behind by /new leaves.
// The control server recomputes the list only when it pushes, so an idle
// row leaves with the engine's next step or reply, not on the dot.
const sessionIdle = 10 * time.Minute

// Sessions lists the threads that have had a user step, most recently
// active first, then by thread id. A thread idle for sessionIdle is left
// out unless it is the engine's most recently active one. Cwd and the
// label, which reads the cwd's git metadata, are filled outside the engine
// lock.
func (e *Engine) Sessions() []SessionInfo {
	out := e.sessions()
	for i := range out {
		if e.cwd != nil {
			out[i].Cwd = e.cwd(out[i].ThreadID)
		}
		out[i].Label = home.Label(e.home.Name, out[i].Cwd)
	}
	return out
}

func (e *Engine) sessions() []SessionInfo {
	e.mu.Lock()
	defer e.mu.Unlock()
	priced := len(e.cfg.Prices) > 0
	var out []SessionInfo
	latest, now := e.latest(), e.now()
	for _, th := range e.all() {
		if th.lastActive.IsZero() || th != latest && now.Sub(th.lastActive) >= sessionIdle {
			continue
		}
		mode := e.mode(th)
		rt := th.native
		if mode == ModeOn && th.hasSent {
			rt = th.sent
		}
		s := SessionInfo{
			ID: e.home.ID + "/" + th.id, Home: e.home.ID, ThreadID: th.id,
			Mode: mode, Tier: th.tier, Route: rt,
			Replies: th.stats.Replies, Switches: th.stats.Switches, Strip: slices.Clone(th.stats.tail),
			LastActive: th.lastActive, Attention: e.attention(th), FooterSynced: len(th.pending) == 0,
		}
		if ls := th.stats.LastSwitch; ls != nil {
			c := *ls
			s.LastSwitch = &c
		}
		if priced {
			spent, saved := th.stats.RoutedUSD, th.stats.BaselineUSD-th.stats.RoutedUSD
			s.SpentUSD, s.SavedUSD = &spent, &saved
		}
		out = append(out, s)
	}
	slices.SortFunc(out, func(a, b SessionInfo) int {
		if c := b.LastActive.Compare(a.LastActive); c != 0 {
			return c
		}
		return cmp.Compare(a.ThreadID, b.ThreadID)
	})
	return out
}

// attention is th's most pressing flag: paused, then manual, then a running
// escalation hold.
func (e *Engine) attention(th *thread) Attention {
	switch {
	case e.paused || th.paused:
		return AttentionPaused
	case th.manual:
		return AttentionManual
	case th.state.EscalatedSignature != "" && th.state.Turn <= th.state.HoldUntilTurn:
		return AttentionEscalation
	}
	return ""
}
