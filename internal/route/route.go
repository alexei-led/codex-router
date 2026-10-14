package route

import (
	"log/slog"
	"time"

	"github.com/alexei-led/codex-router/internal/catalog"
	"github.com/alexei-led/codex-router/internal/classifier"
	"github.com/alexei-led/codex-router/internal/config"
	"github.com/alexei-led/codex-router/internal/home"
	"github.com/alexei-led/codex-router/internal/policy"
)

// Mode is the routing mode of a thread.
type Mode string

const (
	ModeOn     Mode = "on"
	ModePaused Mode = "paused"
	ModeManual Mode = "manual"
)

// Op is a control command.
type Op string

const (
	OpPin      Op = "pin"
	OpUnpin    Op = "unpin"
	OpPause    Op = "pause"
	OpResume   Op = "resume"
	OpPreset   Op = "preset"
	OpSnapshot Op = "snapshot"
)

// Scope says what a command acts on: one session (thread) or the whole home
// (every thread of this engine).
type Scope string

const (
	ScopeSession Scope = "session"
	ScopeHome    Scope = "home"
)

// Command is what the TUI or hook asks the engine to do. Session is a thread
// id. Pin and unpin act on a session; preset on the home; pause and resume on
// either. An empty Scope means the session when Session is set, else the
// home. ID is the caller's, for matching the reply.
type Command struct {
	Op      Op            `json:"op"`
	Session string        `json:"session,omitempty"`
	Scope   Scope         `json:"scope,omitempty"`
	ID      string        `json:"id,omitempty"`
	Tier    config.Tier   `json:"tier,omitempty"`
	Preset  config.Preset `json:"preset,omitempty"`
}

// home reports whether c acts on the whole home.
func (c Command) home() bool {
	return c.Scope == ScopeHome || c.Scope == "" && c.Session == ""
}

// Decision is one per-request outcome.
type Decision struct {
	ThreadID  string
	TurnID    string
	Step      int
	Tier      config.Tier
	Activity  config.Activity
	Route     config.Route
	Original  config.Route
	Rewritten bool
	Reason    policy.Reason
	Why       string
	Support   string
	Estimate  *policy.Estimate
	Reading   *Reading
	MidTurn   bool
	At        time.Time
}

// Reading is the classifier answer the decision's turn runs on, for display:
// the probability per tier, and the activity answer with its probability and
// the two likeliest alternates. Tiers holds only the tiers the answer scored.
type Reading struct {
	Tiers      map[config.Tier]float64
	Activity   config.Activity
	ActivityP  float64
	Alternates []Share
}

// Share is one activity's probability in an answer.
type Share struct {
	Activity config.Activity
	P        float64
}

// Switch is the last route change.
type Switch struct {
	At       time.Time
	From     config.Tier
	To       config.Tier
	Activity config.Activity
	MidTurn  bool
}

// Stats sums the session since its first routed reply. LastInputTokens and
// LastCachedTokens are the last routed reply's; FitHeld counts the steps the
// context guard moved off the policy's tier.
type Stats struct {
	Since                time.Time
	Replies              int
	Switches             int
	ByTier               map[config.Tier]int
	ByActivity           map[config.Activity]int
	Strip                []config.Tier
	RepliesPerCell       int
	LastSwitch           *Switch
	CacheReusePct        int
	LastInputTokens      int
	LastCachedTokens     int
	UncachedSwitchTokens int
	FitHeld              int
	RoutedUSD            float64
	BaselineUSD          float64
}

// FindingKind names what a WORTH A LOOK finding is about, and so where the
// user changes it.
type FindingKind string

const (
	FindingClassifier FindingKind = "classifier" // paused or failing
	FindingManual     FindingKind = "manual"     // the footer model was picked with /model
	FindingContext    FindingKind = "context"    // the context guard kept larger windows
	FindingBlocked    FindingKind = "blocked"    // configured routes outside the group
	FindingCache      FindingKind = "cache"      // low cache reuse after switches
	FindingState      FindingKind = "state"      // the saved intent was unreadable
)

// Finding is one WORTH A LOOK line derived from the session's facts.
type Finding struct {
	Kind FindingKind
	Text string
}

// Snapshot is one thread's full state, as the control socket publishes it
// and the TUI renders it. ThreadID is empty when no thread is known.
type Snapshot struct {
	// Seq orders snapshots: they are taken under the engine lock but
	// published after it, so a consumer drops one older than it holds.
	Seq              uint64
	ThreadID         string
	At               time.Time
	Mode             Mode
	StateLost        bool // the saved intent was unreadable; the home is paused until resume
	Preset           config.Preset
	Pin              *config.Tier
	Baseline         config.Route
	Footer           config.Route
	FooterSynced     bool
	Last             *Decision
	Recent           []Decision
	Classifier       classifier.Health
	ClassifierOff    bool // no classifier configured (e.g. its key is unset)
	ClassifierLabel  string
	ClassifierMedian time.Duration
	Routes           map[config.Tier]config.Route
	Group            catalog.Group
	Blocked          []string
	Policy           config.Policy
	Stats            Stats
	ApproxTokens     int
	Window           int
	Worth            []Finding
}

// Sink receives decisions and snapshots; the launcher fans them out to the
// app-server client and the control socket. Called outside engine locks.
// OnDecision runs inside Decide, before the request goes upstream, and holds
// that thread's next step so its settings writes keep their order; an
// implementation must not block on I/O (queue it instead).
type Sink interface {
	OnDecision(d Decision)
	OnSnapshot(s Snapshot)
}

// Deps are the engine's boundaries. Home names the Codex home the engine
// routes for; Cwd gives a thread's working directory (nil: unknown).
// Intent is the saved intent to start from; IntentLost says it could not be
// read. SaveIntent (nil: not kept) gets the intent after every change,
// outside engine locks and possibly concurrently: it keeps the highest Rev.
type Deps struct {
	Config     config.Config
	Catalog    catalog.Source
	Classifier classifier.Asker
	Now        func() time.Time
	Sink       Sink
	Logger     *slog.Logger
	Home       home.Home
	Cwd        func(threadID string) string
	Intent     Intent
	IntentLost bool
	SaveIntent func(Intent)
}

// Bounds on remembered state. RecentKept and StripCells are per thread.
const (
	MaxThreads = 256
	RecentKept = 64
	StripCells = 48
	// SessionStrip is how many recent reply tiers a SessionInfo carries.
	SessionStrip = 24
)
