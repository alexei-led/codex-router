package facts

import (
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/alexei-led/codex-router/internal/config"
	"github.com/alexei-led/codex-router/internal/responses"
)

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

// Options bound the dialogue sent to the classifier and set where the
// observed activity and the verification check start. RecentTurns ≤ 0 sends
// no dialogue; MaxTextChars ≤ 0 clips every text to empty. ObservedFrom is
// the first input index whose tool calls count for Observed (never before the
// turn's prompt); VerifiedFrom is the first index a verification is looked
// for from, such as Items at the last model change.
type Options struct {
	RecentTurns  int
	MaxTextChars int
	ObservedFrom int
	VerifiedFrom int
}

// FailureWindow is how many trailing items a repeated failure is looked for in.
const FailureWindow = 40

const (
	roleUser      = "user"
	roleAssistant = "assistant"
	typeMessage   = "message"
	clipMarker    = " […] "
)

// Codex adds these user messages itself (AGENTS.md, environment, aborted
// turns, `!` shell commands); they are neither the prompt nor dialogue.
var contextPrefixes = []string{
	"<environment_context>",
	"<user_instructions>",
	"# AGENTS.md instructions for ",
	"<turn_aborted>",
	"<user_shell_command>",
}

// Extract derives Facts from one request. Pure; only the clipped prompt and
// dialogue keep any text.
func Extract(req *responses.Request, opts Options) Facts {
	if req == nil {
		return Facts{Observed: ObservedTalk}
	}
	items := req.Input
	prompt := promptIndex(items)
	f := Facts{
		Dialogue:     dialogue(items, prompt, opts),
		Continuation: len(items) > 0 && isToolOutput(items[len(items)-1]),
		Failure:      repeatedFailure(items),
		Observed:     observe(items, max(prompt+1, opts.ObservedFrom)),
		Verified:     verifiedSince(items, opts.VerifiedFrom),
		ApproxTokens: approxTokens(req),
		Items:        len(items),
	}
	// Escalation reacts to a live loop: a pair from an earlier turn, or one a
	// later passing check proved fixed, is over.
	if p := f.Failure; p != nil && (p.Index <= prompt || verifiedSince(items, p.Index+1)) {
		f.Failure = nil
	}
	if prompt >= 0 {
		f.Prompt = clip(strings.TrimSpace(items[prompt].Text), opts.MaxTextChars)
	}
	return f
}

// Agrees says whether a turn's observed bucket fits the predicted activity.
// A weak oracle: it tells code from ops and explore, not plan from review.
// Uncertain or unknown activities never agree.
func Agrees(predicted config.Activity, observed Observed) bool {
	return slices.Contains(allowed[predicted], observed)
}

var allowed = map[config.Activity][]Observed{
	config.ActivityCode:    {ObservedCode},
	config.ActivityDebug:   {ObservedCode, ObservedOps, ObservedRead},
	config.ActivityExplore: {ObservedRead, ObservedTalk},
	config.ActivityPlan:    {ObservedTalk, ObservedRead, ObservedDocs},
	config.ActivityReview:  {ObservedRead, ObservedTalk},
	// git status or a test listing still runs commands: a read-only shell turn is ops too.
	config.ActivityOps:  {ObservedOps, ObservedRead},
	config.ActivityDocs: {ObservedDocs},
}

// promptIndex is the index of the last message the user typed, where the
// current turn starts; -1 when there is none.
func promptIndex(items []responses.Item) int {
	for i := len(items) - 1; i >= 0; i-- {
		if isUserPrompt(items[i]) {
			return i
		}
	}
	return -1
}

func isUserPrompt(it responses.Item) bool {
	if it.Type != typeMessage || it.Role != roleUser {
		return false
	}
	text := strings.TrimSpace(it.Text)
	for _, p := range contextPrefixes {
		if strings.HasPrefix(text, p) {
			return false
		}
	}
	return true
}

func isDialogue(it responses.Item) bool {
	return isUserPrompt(it) || it.Type == typeMessage && it.Role == roleAssistant
}

// dialogue is the user and assistant text before and after the prompt,
// without the prompt itself: the last RecentTurns of it, each clipped.
func dialogue(items []responses.Item, prompt int, opts Options) []Turn {
	if opts.RecentTurns <= 0 {
		return nil
	}
	var turns []Turn
	for i, it := range items {
		if i == prompt || !isDialogue(it) {
			continue
		}
		if text := strings.TrimSpace(it.Text); text != "" {
			turns = append(turns, Turn{Role: it.Role, Text: clip(text, opts.MaxTextChars)})
		}
	}
	if len(turns) > opts.RecentTurns {
		turns = turns[len(turns)-opts.RecentTurns:]
	}
	return turns
}

// clip keeps at most limit characters: the head and the tail of a long text,
// where a request and its question usually are. It walks runes instead of
// converting the whole text, which may be megabytes. The kept pieces still go
// through []rune so an invalid byte becomes U+FFFD, as it always has.
func clip(text string, limit int) string {
	if len(text) <= limit || utf8.RuneCountInString(text) <= limit {
		return text
	}
	marker := utf8.RuneCountInString(clipMarker)
	if limit <= marker {
		return string([]rune(text[:headBytes(text, max(0, limit))]))
	}
	room := limit - marker
	head := (room + 1) / 2
	return string([]rune(text[:headBytes(text, head)])) + clipMarker + string([]rune(text[tailStart(text, room-head):]))
}

// headBytes is the byte length of text's first n runes.
func headBytes(text string, n int) int {
	i := 0
	for range n {
		_, size := utf8.DecodeRuneInString(text[i:])
		i += size
	}
	return i
}

// tailStart is the byte offset of text's last n runes.
func tailStart(text string, n int) int {
	j := len(text)
	for range n {
		_, size := utf8.DecodeLastRuneInString(text[:j])
		j -= size
	}
	return j
}

// approxTokens is a byte-based estimate: the body size, or the item text when
// the body size is unknown, at four bytes per token.
func approxTokens(req *responses.Request) int {
	n := 0
	for _, it := range req.Input {
		n += len(it.Text) + len(it.Output) + len(it.Arguments)
	}
	return max(req.Bytes, n) / 4
}
