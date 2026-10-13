package cost

import "time"

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

// Switch is one priced switch: the tokens the candidate writes cold and the
// USD estimate when the candidate is priced (else 0).
type Switch struct {
	Candidate      string
	Incumbent      string
	UncachedTokens int
	USD            float64
}
