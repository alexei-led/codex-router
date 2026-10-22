package config

import "time"

const (
	// decideMargin is the proxy's slack over the classifier's own timeout.
	decideMargin = 200 * time.Millisecond
	// fallbackClassifierTimeout applies when the selected classifier has none.
	fallbackClassifierTimeout = 1500 * time.Millisecond
	// cloudflareEndpoint is the Workers AI run URL of the Clef models; the
	// account comes from CLOUDFLARE_ACCOUNT_ID.
	cloudflareEndpoint = "https://api.cloudflare.com/client/v4/accounts/{CLOUDFLARE_ACCOUNT_ID}/ai/run/@cf/cloudflare/"
	modelSol           = "gpt-6.1-sol"
	modelLuna          = "gpt-6-luna"
)

// Default returns the built-in configuration, classifier Jev with Clef,
// OpenAI and a local Ollama option. Routes use two models of one
// compatibility group (plan D2): gpt-6-luna for micro and low, gpt-6.1-sol
// for medium and high. Codex caches per model, not per effort, so only a
// move between the two models costs cache. Tuned on live routing tests and
// benchmarks on 2026-10-15/16: Luna at high effort matched Sol on bounded
// review, debug, ops, code, explore and plan tasks at 5-10% of the list
// cost; xhigh on either model cost more and planned no better than high.
func Default() Config {
	return Config{
		Baseline: TierMedium,
		Routes: map[Tier]Route{
			TierHigh:   {Model: modelSol, Effort: EffortHigh},
			TierMedium: {Model: modelSol, Effort: EffortMedium},
			TierLow:    {Model: modelLuna, Effort: EffortHigh},
			TierMicro:  {Model: modelLuna, Effort: EffortMedium},
		},
		Activities: map[Activity]map[Tier]Route{
			ActivityDebug: {TierMedium: {Model: modelSol, Effort: EffortHigh}},
		},
		// OpenAI list prices, USD per million tokens, from the model cards
		// verified 2026-09-30 (Sol 6.1, up to 272K input) and 2026-09-23 (Luna).
		Prices: map[string]Price{
			modelSol:  {Input: 2, CachedInput: 0.1, Output: 10},
			modelLuna: {Input: 0.1, CachedInput: 0.01, Output: 0.5},
		},
		Policy: Policy{
			UpgradeVotes:         2,
			UpgradeBase:          0.75,
			UpgradeSlope:         0.15,
			UpgradePivotTokens:   100_000,
			JumpConfidence:       0.95,
			DowngradeVotes:       2,
			DowngradeMass:        0.9,
			DowngradeSlope:       0.08,
			DowngradePivotTokens: 100_000,
			ContinuationMass:     0.7,
			EscalationHoldTurns:  2,
			ReviewEveryRequests:  4,
			MinDwellRequests:     8,
			ActivityMass:         0.6,
			ContextFill:          0.8,
		},
		// The mode patches express a direction; they are not tuned against a
		// representative workload.
		Modes: Modes{
			ModeCost: {Policy: PolicyPatch{
				UpgradeVotes: ptr(3), UpgradeBase: ptr(0.85), JumpConfidence: ptr(0.98),
				DowngradeVotes: ptr(1), DowngradeMass: ptr(0.8),
				MinDwellRequests: ptr(4), EscalationHoldTurns: ptr(1),
			}},
			ModeQuality: {Policy: PolicyPatch{
				UpgradeVotes: ptr(1), UpgradeBase: ptr(0.6), JumpConfidence: ptr(0.9),
				DowngradeVotes: ptr(3), DowngradeMass: ptr(0.95),
				MinDwellRequests: ptr(16), EscalationHoldTurns: ptr(4),
			}},
		},
		Classifier: "jev",
		Classifiers: map[string]Classifier{
			"jev": {
				Label:     "Jev",
				API:       APISystemOne,
				Endpoint:  "https://api.typesafe.ai/v1/systemone",
				Model:     "jev-1.13.0",
				KeyEnv:    "TYPESAFE_API_KEY",
				TimeoutMs: 1500,
			},
			"clef": {
				Label:     "Clef",
				API:       APISystemOne,
				Endpoint:  cloudflareEndpoint + "clef",
				Model:     "clef",
				KeyEnv:    "CLOUDFLARE_API_TOKEN",
				TimeoutMs: 3000,
			},
			"clef-flash": {
				Label:     "Clef Flash",
				API:       APISystemOne,
				Endpoint:  cloudflareEndpoint + "clef-flash",
				Model:     "clef-flash",
				KeyEnv:    "CLOUDFLARE_API_TOKEN",
				TimeoutMs: 3000,
			},
			"clef-omni": {
				Label:     "Clef Omni",
				API:       APISystemOne,
				Endpoint:  cloudflareEndpoint + "clef-omni",
				Model:     "clef-omni",
				KeyEnv:    "CLOUDFLARE_API_TOKEN",
				TimeoutMs: 3000,
			},
			"openai": {
				Label:     "OpenAI",
				API:       APIOpenAIDecisions,
				Endpoint:  "https://api.openai.com/v1/decisions",
				Model:     "gpt-6-luna",
				KeyEnv:    "OPENAI_API_KEY",
				TimeoutMs: 3000,
			},
			"ollama": {
				Label:     "Ollama",
				API:       APIOllama,
				Endpoint:  "http://127.0.0.1:11434/api/chat",
				Model:     "qwen3.5:9b",
				TimeoutMs: 5000,
			},
		},
		Context: Context{RecentTurns: 6, MaxTextChars: 1200},
		Cache:   Cache{TTLMs: 300_000, WarmMarginMs: 30_000},
	}
}

func ptr[T any](v T) *T { return &v }

// Effective returns Policy with mode's patch applied: a field the patch sets
// replaces the configured value. An empty mode is balanced.
func (c Config) Effective(mode RoutingMode) Policy {
	p := c.Policy
	if mode == "" {
		mode = ModeBalanced
	}
	m := c.Modes[mode].Policy
	set(&p.UpgradeVotes, m.UpgradeVotes)
	set(&p.UpgradeBase, m.UpgradeBase)
	set(&p.UpgradeSlope, m.UpgradeSlope)
	set(&p.UpgradePivotTokens, m.UpgradePivotTokens)
	set(&p.JumpConfidence, m.JumpConfidence)
	set(&p.DowngradeVotes, m.DowngradeVotes)
	set(&p.DowngradeMass, m.DowngradeMass)
	set(&p.DowngradeSlope, m.DowngradeSlope)
	set(&p.DowngradePivotTokens, m.DowngradePivotTokens)
	set(&p.ContinuationMass, m.ContinuationMass)
	set(&p.EscalationHoldTurns, m.EscalationHoldTurns)
	set(&p.ReviewEveryRequests, m.ReviewEveryRequests)
	set(&p.MinDwellRequests, m.MinDwellRequests)
	set(&p.ActivityMass, m.ActivityMass)
	set(&p.ContextFill, m.ContextFill)
	return p
}

func set[T any](dst *T, v *T) {
	if v != nil {
		*dst = *v
	}
}

// RouteFor returns the route for a tier and activity. An activity override
// replaces the tier's model and effort field by field; an empty override field
// inherits the tier's route.
func (c Config) RouteFor(t Tier, a Activity) Route {
	r := c.Routes[t]
	o, ok := c.Activities[a][t]
	if !ok {
		return r
	}
	if o.Model != "" {
		r.Model = o.Model
	}
	if o.Effort != "" {
		r.Effort = o.Effort
	}
	return r
}

// DecideTimeout bounds one routing decision: the selected classifier's
// timeout plus a margin, so the proxy outlasts the classifier call.
func (c Config) DecideTimeout() time.Duration {
	t := time.Duration(c.Classifiers[c.Classifier].TimeoutMs) * time.Millisecond
	if t <= 0 {
		t = fallbackClassifierTimeout
	}
	return t + decideMargin
}
