package classifier

import (
	"context"
	"net/http"
	"time"

	"github.com/alexei-led/codex-router/internal/config"
)

// Choice is a route answer: a config.Tier or ChoiceUncertain.
type Choice string

// ChoiceUncertain is the route answer for a request that is unclear or not a task.
const ChoiceUncertain Choice = "uncertain"

// ActivityAdvice is the activity answer, or nil when it was not given.
type ActivityAdvice struct {
	Choice        config.Activity
	Probabilities map[config.Activity]float64
}

// Advice is the shared answer shape every adapter returns. Continuation is
// nil when the protocol does not ask it.
type Advice struct {
	Choice        Choice
	Probabilities map[Choice]float64
	Continuation  *float64
	Activity      *ActivityAdvice
	Latency       time.Duration
}

// Message is one dialogue entry shown to the classifier.
type Message struct {
	Role string `json:"role"`
	Text string `json:"text"`
}

// Input is everything a classifier request may contain.
type Input struct {
	Prompt   string
	Dialogue []Message
	Routes   map[config.Tier]config.Route
}

// Adapter builds a request body for a protocol and parses its answer.
type Adapter interface {
	Build(model string, in Input) ([]byte, error)
	Parse(body []byte) (Advice, error)
}

// Asker is what the router depends on; Client implements it.
type Asker interface {
	Ask(ctx context.Context, in Input) (Advice, error)
	Health() Health
}

// Doer sends one HTTP request; *http.Client satisfies it.
type Doer interface {
	Do(req *http.Request) (*http.Response, error)
}

// Health is the client's breaker state.
type Health struct {
	Failures    int
	PausedUntil time.Time
	Latency     time.Duration
}

// Code is a classifier failure reason.
type Code string

// Error makes Code an error.
func (c Code) Error() string { return string(c) }

const (
	ErrTimeout     Code = "timeout"
	ErrMalformed   Code = "malformed"
	ErrAuth        Code = "auth"
	ErrHTTP        Code = "http"
	ErrUnreachable Code = "unreachable"
	ErrPaused      Code = "paused"
	ErrCancelled   Code = "cancelled"
)

// Retry and breaker rules shared by every protocol.
const (
	RetryDelay      = 100 * time.Millisecond
	ActivityStepMin = 300 * time.Millisecond
	FailuresToPause = 3
	PauseDuration   = 60 * time.Second
	MaxAnswerChars  = 16 * 1024
	MaxAttempts     = 2
)

// maxAnswerBytes is the most UTF-8 an answer of MaxAnswerChars UTF-16 code
// units can take: three bytes per unit. A longer body is oversized unread.
const maxAnswerBytes = 3 * MaxAnswerChars
