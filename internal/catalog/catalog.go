package catalog

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"sync"
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

type modelsBody struct {
	Models []Model `json:"models"`
}

type cacheFile struct {
	modelsBody
	FetchedAt time.Time `json:"fetched_at"`
	ETag      string    `json:"etag"`
}

func newCatalog(models []Model) Catalog {
	c := Catalog{Models: make(map[string]Model, len(models))}
	for _, m := range models {
		if m.ToolMode == "" {
			m.ToolMode = ToolModeNone
		}
		c.Models[m.Slug] = m
	}
	return c
}

// Parse decodes a /models response body. ETag and FetchedAt are the caller's.
func Parse(body []byte) (Catalog, error) {
	var b modelsBody
	if err := json.Unmarshal(body, &b); err != nil {
		return Catalog{}, fmt.Errorf("parse models: %w", err)
	}
	return newCatalog(b.Models), nil
}

// LoadCache reads a models_cache.json. A missing file yields an empty
// catalog, not an error. The file is never written.
func LoadCache(path string) (Catalog, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return newCatalog(nil), nil
	}
	if err != nil {
		return Catalog{}, fmt.Errorf("read models cache: %w", err)
	}
	var f cacheFile
	if err := json.Unmarshal(data, &f); err != nil {
		return Catalog{}, fmt.Errorf("parse models cache: %w", err)
	}
	c := newCatalog(f.Models)
	c.ETag = f.ETag
	c.FetchedAt = f.FetchedAt
	return c, nil
}

// GroupOf returns the compatibility group of slug.
func (c Catalog) GroupOf(slug string) (Group, bool) {
	m, ok := c.Models[slug]
	if !ok {
		return Group{}, false
	}
	return Group{CompHash: m.CompHash, Lite: m.ResponsesLite, ToolMode: m.ToolMode}, true
}

// Compatible reports whether both models are known and share a Group.
func (c Catalog) Compatible(a, b string) bool {
	ga, okA := c.GroupOf(a)
	gb, okB := c.GroupOf(b)
	return okA && okB && ga == gb
}

// Window is the usable context in tokens: context_window x percent / 100.
// A missing model, or a non-positive window or percent, is unknown.
func (c Catalog) Window(slug string) (int, bool) {
	m, ok := c.Models[slug]
	if !ok || m.ContextWindow <= 0 || m.EffectivePercent <= 0 {
		return 0, false
	}
	return m.ContextWindow * m.EffectivePercent / 100, true
}

// Store holds the current catalog for concurrent readers and writers.
type Store struct {
	mu sync.RWMutex
	c  Catalog
}

// Current returns the latest catalog; the zero Catalog before the first Set.
func (s *Store) Current() Catalog {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.c
}

// Set replaces the catalog.
func (s *Store) Set(c Catalog) {
	s.mu.Lock()
	s.c = c
	s.mu.Unlock()
}
