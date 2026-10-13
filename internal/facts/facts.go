package facts

// Turn is one user or assistant text message, clipped.
type Turn struct {
	Role string
	Text string
}

// Observed is the activity bucket a turn's tool calls show.
type Observed string

const (
	ObservedCode Observed = "code"
	ObservedDocs Observed = "docs"
	ObservedOps  Observed = "ops"
	ObservedRead Observed = "read"
	ObservedTalk Observed = "talk"
)

// Failure is a tool error seen twice with an edit attempt in between.
type Failure struct {
	Signature string
	Index     int
}

// Facts is the policy input derived from a request.
type Facts struct {
	Prompt       string
	Dialogue     []Turn
	Continuation bool
	Failure      *Failure
	Observed     Observed
	Verified     bool
	ApproxTokens int
	Items        int
}

// Options bound the dialogue sent to the classifier.
type Options struct {
	RecentTurns  int
	MaxTextChars int
}

// FailureWindow is how many trailing items a repeated failure is looked for in.
const FailureWindow = 40
