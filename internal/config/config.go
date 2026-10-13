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

// Preset selects a built-in policy override set.
type Preset string

const (
	PresetThrifty  Preset = "thrifty"
	PresetBalanced Preset = "balanced"
	PresetQuality  Preset = "quality"
)

// Price is a model's list price in USD per million tokens. Zero values mean
// unpriced: estimates then stay in tokens.
type Price struct {
	Input       float64 `json:"input"`
	CachedInput float64 `json:"cachedInput"`
	Output      float64 `json:"output"`
}

// Policy holds the switching thresholds. Taxes are in uncached tokens.
type Policy struct {
	UpgradeVotes          int     `json:"upgradeVotes"`
	UpgradeBase           float64 `json:"upgradeBase"`
	UpgradeSlope          float64 `json:"upgradeSlope"`
	UpgradePivotTokens    int     `json:"upgradePivotTokens"`
	JumpConfidence        float64 `json:"jumpConfidence"`
	DowngradeVotes        int     `json:"downgradeVotes"`
	DowngradeMass         float64 `json:"downgradeMass"`
	DowngradeSlope        float64 `json:"downgradeSlope"`
	DowngradePivotTokens  int     `json:"downgradePivotTokens"`
	DowngradeHorizonTurns int     `json:"downgradeHorizonTurns"`
	ContinuationMass      float64 `json:"continuationMass"`
	EscalationHoldTurns   int     `json:"escalationHoldTurns"`
	ReviewEveryRequests   int     `json:"reviewEveryRequests"`
	MinDwellRequests      int     `json:"minDwellRequests"`
	ActivityMass          float64 `json:"activityMass"`
	ContextFill           float64 `json:"contextFill"`
}

// Classifier describes one classifier endpoint. API names the wire adapter
// ("system-one" or "ollama"). KeyEnv names the environment variable holding
// the bearer key; empty means no key.
type Classifier struct {
	Label     string `json:"label"`
	API       string `json:"api"`
	Endpoint  string `json:"endpoint"`
	Model     string `json:"model"`
	KeyEnv    string `json:"keyEnv,omitempty"`
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
	Baseline    Tier                        `json:"baseline"`
	Routes      map[Tier]Route              `json:"routes"`
	Activities  map[Activity]map[Tier]Route `json:"activities,omitempty"`
	Prices      map[string]Price            `json:"prices,omitempty"`
	Policy      Policy                      `json:"policy"`
	Preset      Preset                      `json:"preset"`
	Classifier  string                      `json:"classifier"`
	Classifiers map[string]Classifier       `json:"classifiers"`
	Context     Context                     `json:"context"`
	Cache       Cache                       `json:"cache"`
	Proxy       Proxy                       `json:"proxy"`
	Passthrough []string                    `json:"passthrough,omitempty"`
	CodexBin    string                      `json:"codexBin,omitempty"`
}

// File locations under the XDG config dir.
const (
	DirName  = "codex-router"
	FileName = "config.json"
)

// Classifier wire adapters.
const (
	APISystemOne = "system-one"
	APIOllama    = "ollama"
)
