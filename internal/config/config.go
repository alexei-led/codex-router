package config

// Tier is a task-complexity level. Tiers is ordered weakest first; the index
// in Tiers is the rank.
type Tier string

const (
	TierMicro  Tier = "micro"
	TierLow    Tier = "low"
	TierMedium Tier = "medium"
	TierHigh   Tier = "high"
)

// Tiers lists every tier by rank.
var Tiers = []Tier{TierMicro, TierLow, TierMedium, TierHigh}

// Activity names what a turn produces. ActivityUncertain is a classifier
// answer, never a config key.
type Activity string

const (
	ActivityCode      Activity = "code"
	ActivityDebug     Activity = "debug"
	ActivityExplore   Activity = "explore"
	ActivityPlan      Activity = "plan"
	ActivityReview    Activity = "review"
	ActivityOps       Activity = "ops"
	ActivityDocs      Activity = "docs"
	ActivityUncertain Activity = "uncertain"
)

// Activities lists the seven routable activities.
var Activities = []Activity{
	ActivityCode, ActivityDebug, ActivityExplore, ActivityPlan, ActivityReview, ActivityOps, ActivityDocs,
}

// Effort is a Codex reasoning effort as sent in reasoning.effort.
type Effort string

const (
	EffortLow    Effort = "low"
	EffortMedium Effort = "medium"
	EffortHigh   Effort = "high"
	EffortXHigh  Effort = "xhigh"
)

// Efforts lists efforts weakest first.
var Efforts = []Effort{EffortLow, EffortMedium, EffortHigh, EffortXHigh}

// Route is a model slug plus the effort the router sends for it. An empty
// Effort keeps the effort Codex sent.
type Route struct {
	Model  string `json:"model"`
	Effort Effort `json:"effort,omitempty"`
}

// RoutingMode trades cost against answer quality: it moves the route a tier
// aims at and patches the policy. It is not route.Mode (on, paused, manual).
type RoutingMode string

const (
	ModeCost     RoutingMode = "cost"
	ModeBalanced RoutingMode = "balanced"
	ModeQuality  RoutingMode = "quality"
)

// RoutingModes lists the modes, cheapest first.
var RoutingModes = []RoutingMode{ModeCost, ModeBalanced, ModeQuality}

// ModeConfig is one mode's settings.
type ModeConfig struct {
	Policy PolicyPatch `json:"policy"`
}

// PolicyPatch holds the Policy fields a mode replaces; a nil field keeps the
// configured value.
type PolicyPatch struct {
	UpgradeVotes         *int     `json:"upgradeVotes,omitempty"`
	UpgradeBase          *float64 `json:"upgradeBase,omitempty"`
	UpgradeSlope         *float64 `json:"upgradeSlope,omitempty"`
	UpgradePivotTokens   *int     `json:"upgradePivotTokens,omitempty"`
	JumpConfidence       *float64 `json:"jumpConfidence,omitempty"`
	DowngradeVotes       *int     `json:"downgradeVotes,omitempty"`
	DowngradeMass        *float64 `json:"downgradeMass,omitempty"`
	DowngradeSlope       *float64 `json:"downgradeSlope,omitempty"`
	DowngradePivotTokens *int     `json:"downgradePivotTokens,omitempty"`
	ContinuationMass     *float64 `json:"continuationMass,omitempty"`
	EscalationHoldTurns  *int     `json:"escalationHoldTurns,omitempty"`
	ReviewEveryRequests  *int     `json:"reviewEveryRequests,omitempty"`
	MinDwellRequests     *int     `json:"minDwellRequests,omitempty"`
	ActivityMass         *float64 `json:"activityMass,omitempty"`
	ContextFill          *float64 `json:"contextFill,omitempty"`
}

// Modes maps a mode to its settings. A config file's entry is decoded over
// the built-in entry of the same mode, so it names only the fields it changes.
type Modes map[RoutingMode]ModeConfig

// Price is a model's list price in USD per million tokens. Zero values mean
// unpriced: estimates then stay in tokens.
type Price struct {
	Input       float64 `json:"input"`
	CachedInput float64 `json:"cachedInput"`
	Output      float64 `json:"output"`
}

// Policy holds the switching thresholds. Taxes are in uncached tokens.
type Policy struct {
	UpgradeVotes         int     `json:"upgradeVotes"`
	UpgradeBase          float64 `json:"upgradeBase"`
	UpgradeSlope         float64 `json:"upgradeSlope"`
	UpgradePivotTokens   int     `json:"upgradePivotTokens"`
	JumpConfidence       float64 `json:"jumpConfidence"`
	DowngradeVotes       int     `json:"downgradeVotes"`
	DowngradeMass        float64 `json:"downgradeMass"`
	DowngradeSlope       float64 `json:"downgradeSlope"`
	DowngradePivotTokens int     `json:"downgradePivotTokens"`
	ContinuationMass     float64 `json:"continuationMass"`
	EscalationHoldTurns  int     `json:"escalationHoldTurns"`
	ReviewEveryRequests  int     `json:"reviewEveryRequests"`
	MinDwellRequests     int     `json:"minDwellRequests"`
	ActivityMass         float64 `json:"activityMass"`
	ContextFill          float64 `json:"contextFill"`
	// Deprecated: accepted for existing configs; has no effect.
	DowngradeHorizonTurns int `json:"downgradeHorizonTurns,omitempty"`
}

// ClassifierOff as Config.Classifier turns the classifier off: no prompt
// leaves the machine, and only pins and the context guard change the model.
const ClassifierOff = "off"

// Classifiers maps a name to a classifier. A config file's entry is decoded
// over the entry of the same name, so it names only the fields it changes.
type Classifiers map[string]Classifier

// Classifier describes one classifier endpoint. API names the wire adapter
// ("system-one", "openai-decisions" or "ollama"). KeyEnv names the
// environment variable holding the bearer key; empty means no key. Endpoint
// may hold {NAME} placeholders, filled from the variable NAME. EnvFile is a
// dotenv file (absolute or ~/ path) read for KeyEnv and each placeholder when
// the process env does not set it: service units do not see the shell's env.
type Classifier struct {
	Label     string `json:"label"`
	API       string `json:"api"`
	Endpoint  string `json:"endpoint"`
	Model     string `json:"model"`
	KeyEnv    string `json:"keyEnv,omitempty"`
	EnvFile   string `json:"envFile,omitempty"`
	TimeoutMs int    `json:"timeoutMs"`
}

// Context bounds what the classifier sees.
type Context struct {
	RecentTurns  int `json:"recentTurns"`
	MaxTextChars int `json:"maxTextChars"`
}

// Cache models prompt-cache warmth: per model, effort-neutral (spikes 3, 5).
type Cache struct {
	TTLMs        int `json:"ttlMs"`
	WarmMarginMs int `json:"warmMarginMs"`
}

// Proxy holds listener settings. Port 0 binds an ephemeral port per session.
type Proxy struct {
	Port int `json:"port"`
}

// Config is the whole router configuration.
type Config struct {
	Baseline   Tier                        `json:"baseline"`
	Routes     map[Tier]Route              `json:"routes"`
	Activities map[Activity]map[Tier]Route `json:"activities,omitempty"`
	Prices     map[string]Price            `json:"prices,omitempty"`
	Policy     Policy                      `json:"policy"`
	// Mode is the profile's routing mode; empty means not set (balanced).
	Mode        RoutingMode `json:"mode,omitempty"`
	Modes       Modes       `json:"modes"`
	Classifier  string      `json:"classifier"`
	Classifiers Classifiers `json:"classifiers"`
	Context     Context     `json:"context"`
	Cache       Cache       `json:"cache"`
	Proxy       Proxy       `json:"proxy"`
	CodexBin    string      `json:"codexBin,omitempty"`
}

// File locations under the XDG config dir.
const (
	DirName  = "codex-router"
	FileName = "config.json"
)

// Classifier wire adapters.
const (
	APISystemOne       = "system-one"
	APIOpenAIDecisions = "openai-decisions"
	APIOllama          = "ollama"
)
