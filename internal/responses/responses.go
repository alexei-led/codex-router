package responses

import "github.com/alexei-led/codex-router/internal/config"

// Request headers the proxy reads or forwards. Authorization and
// ChatGPT-Account-ID are forwarded upstream only and never read.
const (
	HeaderTurnMetadata  = "X-Codex-Turn-Metadata"
	HeaderSubagent      = "X-Openai-Subagent"
	HeaderResponsesLite = "X-Openai-Internal-Codex-Responses-Lite"
	HeaderWindowID      = "X-Codex-Window-Id"
	HeaderAuthorization = "Authorization"
	HeaderAccountID     = "Chatgpt-Account-Id"
)

// Response headers the proxy strips: Codex reads them as the served model and
// warns on a mismatch (research §3.1).
const (
	HeaderModel  = "Openai-Model"
	HeaderXModel = "X-Openai-Model"
)

// Kind says whether a request is a routable user step or must pass through
// unchanged (compaction, memory, guardian, subagent, unknown).
type Kind string

const (
	KindUserStep    Kind = "user-step"
	KindPassthrough Kind = "passthrough"
)

// Meta is the parsed x-codex-turn-metadata header plus the subagent and
// window headers. Empty TurnID means the request is not a user step.
type Meta struct {
	SessionID string
	ThreadID  string
	TurnID    string
	AgentName string
	Subagent  string
	WindowID  string
}

// Item is one input item, flattened. Fields not used by a type stay empty;
// unknown types keep only Type.
type Item struct {
	Type      string
	Role      string
	ID        string
	Name      string
	CallID    string
	Text      string
	Output    string
	Arguments string
}

// Request is the parsed body. Bytes is the raw body length; the raw body is
// kept inside for Apply.
type Request struct {
	Model  string
	Effort config.Effort
	Input  []Item
	Bytes  int

	raw []byte
}

// Rewrite is what the router changes: model and reasoning.effort. An empty
// Effort keeps the effort Codex sent.
type Rewrite struct {
	Model  string
	Effort config.Effort
}

// Usage is the usage block of response.completed.
type Usage struct {
	InputTokens       int
	CachedInputTokens int
	OutputTokens      int
	ReasoningTokens   int
	TotalTokens       int
}
