package cost

import (
	"sync"
	"time"

	"github.com/alexei-led/codex-router/internal/config"
	"github.com/alexei-led/codex-router/internal/responses"
)

// Cache is the warmth rule: a model is warm from its last response until
// TTL minus WarmMargin has passed.
type Cache struct {
	TTL        time.Duration
	WarmMargin time.Duration
}

// CacheState is what the ledger remembers per thread and model.
type CacheState struct {
	PrefixTokens int
	LastAt       time.Time
}

// Ledger remembers, per thread, each model's cached prefix and the model the
// thread last ran on. Keys ignore effort: Codex's cache is effort-neutral. A
// detour to another model does not evict the previous model's entry. Safe for
// concurrent use.
type Ledger struct {
	cache   Cache
	mu      sync.Mutex
	threads map[string]*threadCache
}

type threadCache struct {
	current string
	models  map[string]CacheState
}

// NewLedger returns an empty ledger using the warmth rule c.
func NewLedger(c Cache) *Ledger {
	return &Ledger{cache: c, threads: map[string]*threadCache{}}
}

// Observe records a completed response on model: its whole input is now the
// model's cached prefix, and model becomes the thread's current model.
func (l *Ledger) Observe(thread, model string, u responses.Usage, at time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	t := l.threads[thread]
	if t == nil {
		t = &threadCache{models: map[string]CacheState{}}
		l.threads[thread] = t
	}
	t.current = model
	t.models[model] = CacheState{PrefixTokens: u.InputTokens, LastAt: at}
}

// Warm reports whether model holds a cached prefix for thread at now.
func (l *Ledger) Warm(thread, model string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	_, warm := l.warmPrefix(thread, model, now)
	return warm
}

// SwitchTax is how many more uncached input tokens the thread's next request,
// of tokens context tokens, writes on candidate than on the thread's current
// model. A warm model pays only what its cached prefix lacks; a cold one pays
// all tokens. It is 0 for the current model, for a candidate warm on at least
// the current model's prefix, and when the current model is cold or unknown,
// since staying then protects nothing.
//
// The tax is relative to staying, not the candidate's absolute uncached
// tokens: the part both models must write is paid either way and must not
// raise the switch bar.
func (l *Ledger) SwitchTax(thread, candidate string, tokens int, now time.Time) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	tokens = max(0, tokens)
	current := ""
	if t := l.threads[thread]; t != nil {
		current = t.current
	}
	stay, _ := l.warmPrefix(thread, current, now)
	move, _ := l.warmPrefix(thread, candidate, now)
	return max(0, min(stay, tokens)-min(move, tokens))
}

// Forget drops a thread's entries, for a caller evicting the thread.
func (l *Ledger) Forget(thread string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.threads, thread)
}

// USD prices a response at p's per-million list prices: uncached input,
// cached input and output (which includes reasoning tokens). An unpriced
// model costs 0.
func USD(p config.Price, u responses.Usage) float64 {
	cached := min(max(0, u.CachedInputTokens), u.InputTokens)
	uncached := u.InputTokens - cached
	return (float64(uncached)*p.Input + float64(cached)*p.CachedInput + float64(u.OutputTokens)*p.Output) / 1e6
}

// warmPrefix returns model's cached prefix for thread and true while it is
// warm at now, else 0 and false.
func (l *Ledger) warmPrefix(thread, model string, now time.Time) (int, bool) {
	t := l.threads[thread]
	if t == nil {
		return 0, false
	}
	s, ok := t.models[model]
	if !ok || s.PrefixTokens <= 0 || now.Before(s.LastAt) ||
		!now.Before(s.LastAt.Add(l.cache.TTL-l.cache.WarmMargin)) {
		return 0, false
	}
	return s.PrefixTokens, true
}
