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
	AttentionUnknown    Attention = "unknown"    // seeded after a restart: held until a reply measures the cache
	AttentionStateLost  Attention = "state-lost" // the saved intent was unreadable: the home is paused
)

// SessionInfo is one session's summary for the sessions list: a Codex main
// thread of this engine's home. ID is "<home id>/<thread id>". Every number
// comes from the thread's own tallies. The USD fields are set only when every
// eligible reply has prices for its routed and native models.
type SessionInfo struct {
	ID              string        `json:"id"`
	Home            string        `json:"home"`
	ThreadID        string        `json:"thread_id"`
	Label           string        `json:"label"`
	Cwd             string        `json:"cwd"`
	Mode            Mode          `json:"mode"`
	Tier            config.Tier   `json:"tier"`
	Route           config.Route  `json:"route"`
	Replies         int           `json:"replies"`
	Switches        int           `json:"switches"`
	Strip           []config.Tier `json:"strip"`
	LastSwitch      *Switch       `json:"last_switch,omitempty"`
	LastActive      time.Time     `json:"last_active"`
	Attention       Attention     `json:"attention,omitempty"`
	PendingPin      bool          `json:"pending_pin,omitempty"`
	FooterSynced    bool          `json:"footer_synced"`
	PricedReplies   int           `json:"priced_replies"`
	EligibleReplies int           `json:"eligible_replies"`
	SavedUSD        *float64      `json:"saved_usd,omitempty"`
	SpentUSD        *float64      `json:"spent_usd,omitempty"`
	// RoutingMode and RoutingModeSource are additive: an older engine sends
	// neither.
	RoutingMode       config.RoutingMode `json:"routing_mode,omitempty"`
	RoutingModeSource ModeSource         `json:"routing_mode_source,omitempty"`
}

// sessionIdle is how long an ordinary thread may go without a user step or
// reply before the sessions list hides it, so a thread left behind by /new
// leaves. Per-thread pause, manual hold and pending pin intent keep it listed.
// The control server recomputes the list only when it pushes, so an idle row
// leaves with the engine's next step or reply, not on the dot.
const sessionIdle = 10 * time.Minute

// Sessions lists the threads that have had a user step, most recently
// active first, then by thread id. Ordinary threads idle for sessionIdle are
// left out unless they are the engine's most recently active one. Per-thread
// pause, manual hold and pending pin intent keep a thread listed. Cwd and the
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
	var out []SessionInfo
	latest, now := e.latest(), e.now()
	for _, th := range e.all() {
		intent := th.paused || th.manual || th.pin != nil
		if th.lastActive.IsZero() {
			// A restored intent thread has no activity in this process yet.
			// Keep it visible so the user can see and resume it.
			if !intent {
				continue
			}
		} else if th != latest && !intent && now.Sub(th.lastActive) >= sessionIdle {
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
			LastActive: th.lastActive, Attention: e.attention(th), PendingPin: th.pin != nil, FooterSynced: len(th.pending) == 0,
			PricedReplies: th.stats.PricedReplies, EligibleReplies: th.stats.EligibleReplies,
		}
		s.RoutingMode, s.RoutingModeSource = e.routingMode(th)
		if ls := th.stats.LastSwitch; ls != nil {
			c := *ls
			s.LastSwitch = &c
		}
		// Publish a session total only when every routed reply has prices for
		// both its routed model and the native model for that reply.
		if th.stats.EligibleReplies > 0 && th.stats.PricedReplies == th.stats.EligibleReplies {
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

// attention is th's most pressing flag: a lost intent, paused, manual, a
// seeded hold, then a running escalation hold.
func (e *Engine) attention(th *thread) Attention {
	switch {
	case e.lost:
		return AttentionStateLost
	case e.paused || th.paused:
		return AttentionPaused
	case th.manual:
		return AttentionManual
	case th.held:
		return AttentionUnknown
	case th.state.EscalatedSignature != "" && th.state.Turn <= th.state.HoldUntilTurn:
		return AttentionEscalation
	}
	return ""
}
