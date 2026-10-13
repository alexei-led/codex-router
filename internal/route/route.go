package route

import (
	"log/slog"
	"time"

	"github.com/alexei-led/codex-router/internal/catalog"
	"github.com/alexei-led/codex-router/internal/classifier"
	"github.com/alexei-led/codex-router/internal/config"
	"github.com/alexei-led/codex-router/internal/policy"
	"github.com/alexei-led/codex-router/internal/responses"
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

// Command is what the TUI or hook asks the engine to do.
type Command struct {
	Op     Op            `json:"op"`
	Tier   config.Tier   `json:"tier,omitempty"`
	Preset config.Preset `json:"preset,omitempty"`
}

// Decision is one per-request outcome.
type Decision struct {
	ThreadID  string
	TurnID    string
	Step      int
	Kind      responses.Kind
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
	At        time.Time
	From      config.Tier
	To        config.Tier
	FromRoute config.Route
	ToRoute   config.Route
	Activity  config.Activity
	MidTurn   bool
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
)

// Finding is one WORTH A LOOK line derived from the session's facts.
type Finding struct {
	Kind FindingKind
	Text string
}

// Snapshot is the full state the control socket publishes and the TUI renders.
type Snapshot struct {
	At               time.Time
	Mode             Mode
	Preset           config.Preset
	Pin              *config.Tier
	Baseline         config.Route
	Footer           config.Route
	FooterSynced     bool
	Last             *Decision
	Recent           []Decision
	Classifier       classifier.Health
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

// Deps are the engine's boundaries.
type Deps struct {
	Config     config.Config
	Catalog    catalog.Source
	Classifier classifier.Asker
	Now        func() time.Time
	Sink       Sink
	Logger     *slog.Logger
}

// Bounds on remembered state.
const (
	MaxThreads = 256
	RecentKept = 64
	StripCells = 48
)
