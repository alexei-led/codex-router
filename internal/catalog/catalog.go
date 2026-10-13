package catalog

import (
	"time"

	"github.com/alexei-led/codex-router/internal/config"
)

// ReasoningLevel is one entry of supported_reasoning_levels.
type ReasoningLevel struct {
	Effort config.Effort `json:"effort"`
}

// Model is one catalog entry. Only the fields the router reads are decoded.
type Model struct {
	Slug             string           `json:"slug"`
	Visibility       string           `json:"visibility"`
	ContextWindow    int              `json:"context_window"`
	MaxContextWindow int              `json:"max_context_window"`
	EffectivePercent int              `json:"effective_context_window_percent"`
	CompHash         string           `json:"comp_hash"`
	ResponsesLite    bool             `json:"use_responses_lite"`
	ToolMode         string           `json:"tool_mode"`
	ReasoningLevels  []ReasoningLevel `json:"supported_reasoning_levels"`
}

// Group is the compatibility key: a per-step rewrite is safe only between
// models with an equal Group (research §1.2, spikes 3-5).
type Group struct {
	CompHash string
	Lite     bool
	ToolMode string
}

// Catalog is a parsed /models body with the ETag it came with.
type Catalog struct {
	Models    map[string]Model
	ETag      string
	FetchedAt time.Time
}

// Source is what consumers read the current catalog through.
type Source interface {
	Current() Catalog
}

// ToolModeNone is the tool_mode of an entry that has no tool_mode field.
const ToolModeNone = "none"
