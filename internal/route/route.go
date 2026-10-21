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

// Decision is one per-request outcome. Its names on the control wire are
// spelled out, as they are for Snapshot and the types below: the engine's
// field names are not the protocol, and internal/control pins these spellings
// with literal fixtures, so a rename here cannot change what a client reads.
type Decision struct {
	ThreadID  string           `json:"ThreadID"`
	TurnID    string           `json:"TurnID"`
	Step      int              `json:"Step"`
	Tier      config.Tier      `json:"Tier"`
	Activity  config.Activity  `json:"Activity"`
	Route     config.Route     `json:"Route"`
	Original  config.Route     `json:"Original"`
	Rewritten bool             `json:"Rewritten"`
	Reason    policy.Reason    `json:"Reason"`
	Why       string           `json:"Why"`
	Support   string           `json:"Support"`
	Estimate  *policy.Estimate `json:"Estimate"`
	Reading   *Reading         `json:"Reading"`
	MidTurn   bool             `json:"MidTurn"`
	At        time.Time        `json:"At"`
	// SupportParts is Support as the fragments the engine built it from, so a
	// panel can punctuate and wrap them itself. It is optional: an engine
	// older than this field sends Support alone, and a reader then shows that
	// whole (a label may hold the separator, so it must not be split). It is
	// last so this additive field moves no existing name on the wire.
	SupportParts []string `json:"SupportParts,omitempty"`
}

// Reading is the classifier answer the decision's turn runs on, for display:
// the probability per tier, and the activity answer with its probability and
// the two likeliest alternates. Tiers holds only the tiers the answer scored.
type Reading struct {
	Tiers      map[config.Tier]float64 `json:"Tiers"`
	Activity   config.Activity         `json:"Activity"`
	ActivityP  float64                 `json:"ActivityP"`
	Alternates []Share                 `json:"Alternates"`
}

// Share is one activity's probability in an answer.
type Share struct {
	Activity config.Activity `json:"Activity"`
	P        float64         `json:"P"`
}

// Switch is the last route change.
type Switch struct {
	At       time.Time       `json:"At"`
	From     config.Tier     `json:"From"`
	To       config.Tier     `json:"To"`
	Activity config.Activity `json:"Activity"`
	MidTurn  bool            `json:"MidTurn"`
}

// Stats sums the session since its first routed reply. LastInputTokens and
// LastCachedTokens are the last routed reply's; FitHeld counts the steps the
// context guard moved off the policy's tier. Pricing coverage counts the
// replies with prices for both the routed and per-reply native models.
type Stats struct {
	Since                time.Time               `json:"Since"`
	Replies              int                     `json:"Replies"`
	Switches             int                     `json:"Switches"`
	ByTier               map[config.Tier]int     `json:"ByTier"`
	ByActivity           map[config.Activity]int `json:"ByActivity"`
	Strip                []config.Tier           `json:"Strip"`
	RepliesPerCell       int                     `json:"RepliesPerCell"`
	LastSwitch           *Switch                 `json:"LastSwitch"`
	CacheReusePct        int                     `json:"CacheReusePct"`
	LastInputTokens      int                     `json:"LastInputTokens"`
	LastCachedTokens     int                     `json:"LastCachedTokens"`
	UncachedSwitchTokens int                     `json:"UncachedSwitchTokens"`
	FitHeld              int                     `json:"FitHeld"`
	RoutedUSD            float64                 `json:"RoutedUSD"`
	BaselineUSD          float64                 `json:"BaselineUSD"`
	SavingsUSD           float64                 `json:"SavingsUSD"`      // sum of cheaper replies' savings before netting extra costs
	ExtraUSD             float64                 `json:"ExtraUSD"`        // sum of costlier replies' extra costs
	PricedReplies        int                     `json:"PricedReplies"`   // replies with prices for both routed and native models
	EligibleReplies      int                     `json:"EligibleReplies"` // routed replies considered for dollar estimates
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
	Kind FindingKind `json:"Kind"`
	Text string      `json:"Text"`
}

// Snapshot is one thread's full state, as the control socket publishes it
// and the TUI renders it. ThreadID is empty when no thread is known.
type Snapshot struct {
	// Seq orders snapshots: they are taken under the engine lock but
	// published after it, so a consumer drops one older than it holds.
	Seq           uint64            `json:"Seq"`
	ThreadID      string            `json:"ThreadID"`
	At            time.Time         `json:"At"`
	Mode          Mode              `json:"Mode"`
	StateLost     bool              `json:"StateLost"` // the saved intent was unreadable; the home is paused until resume
	Preset        config.Preset     `json:"Preset"`
	Pin           *config.Tier      `json:"Pin"`
	Baseline      config.Route      `json:"Baseline"`
	Footer        config.Route      `json:"Footer"`
	FooterSynced  bool              `json:"FooterSynced"`
	Last          *Decision         `json:"Last"`
	Recent        []Decision        `json:"Recent"`
	Classifier    classifier.Health `json:"Classifier"`
	ClassifierOff bool              `json:"ClassifierOff"` // no classifier configured (e.g. its key is unset)
	// ClassifierOffReason says why it is off, without the key. Empty when on.
	ClassifierOffReason  string                       `json:"ClassifierOffReason"`
	ClassifierLabel      string                       `json:"ClassifierLabel"`
	ClassifierModel      string                       `json:"ClassifierModel"`
	ClassifierMedian     time.Duration                `json:"ClassifierMedian"`
	Routes               map[config.Tier]config.Route `json:"Routes"`
	Group                catalog.Group                `json:"Group"`
	CurrentModelKnown    bool                         `json:"CurrentModelKnown"`
	CatalogAvailable     bool                         `json:"CatalogAvailable"`
	Blocked              []string                     `json:"Blocked"`
	CompatibilityUnknown []string                     `json:"CompatibilityUnknown"`
	Policy               config.Policy                `json:"Policy"`
	Stats                Stats                        `json:"Stats"`
	ApproxTokens         int                          `json:"ApproxTokens"`
	Window               int                          `json:"Window"`
	Worth                []Finding                    `json:"Worth"`
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
// HoldUnseeded (the brain) holds a thread whose first request seen already
// carries a conversation, as Resumed does: after a restart, a turn's next
// step can reach the engine before discovery resumes its thread.
type Deps struct {
	Config     config.Config
	Catalog    catalog.Source
	Classifier classifier.Asker
	// ClassifierOffReason is why Classifier is nil; the snapshot shows it.
	ClassifierOffReason string
	Now                 func() time.Time
	Sink                Sink
	Logger              *slog.Logger
	Home                home.Home
	Cwd                 func(threadID string) string
	Intent              Intent
	IntentLost          bool
	SaveIntent          func(Intent)
	HoldUnseeded        bool
}

// Bounds on remembered state. RecentKept and StripCells are per thread.
const (
	MaxThreads = 256
	RecentKept = 64
	StripCells = 48
	// SessionStrip is how many recent reply tiers a SessionInfo carries.
	SessionStrip = 24
)
