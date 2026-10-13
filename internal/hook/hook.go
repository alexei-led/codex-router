package hook

import (
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"time"

	"github.com/alexei-led/codex-router/internal/control"
	"github.com/alexei-led/codex-router/internal/route"
)

// Input is the subset of the Codex hook stdin the hook reads.
type Input struct {
	SessionID string `json:"session_id"`
	TurnID    string `json:"turn_id"`
	Model     string `json:"model"`
	Event     string `json:"hook_event_name"`
}

// Output is the hook's stdout. Only systemMessage is ever set.
type Output struct {
	SystemMessage string `json:"systemMessage,omitempty"`
}

// MessagePrefix starts every line; Codex renders it as "↳ Hook · router: …".
const MessagePrefix = "router: "

// Timeout bounds the whole hook run, socket included.
const Timeout = 200 * time.Millisecond

// Run reads one hook input from stdin, asks the control socket for a snapshot
// and writes {"systemMessage": ...} when the turn's route differs from the
// previous turn's, else {}. A missing or unreachable socket, unreadable input
// or a slow router all print {} and return nil: a hook must never block Codex.
func Run(stdin io.Reader, stdout io.Writer, socket string) error {
	var in Input
	_ = json.NewDecoder(stdin).Decode(&in)
	var out Output
	if snap, ok := fetch(socket); ok {
		out.SystemMessage = Message(snap, in)
	}
	return json.NewEncoder(stdout).Encode(out)
}

func fetch(socket string) (route.Snapshot, bool) {
	if socket == "" {
		return route.Snapshot{}, false
	}
	type result struct {
		snap route.Snapshot
		ok   bool
	}
	ch := make(chan result, 1)
	done := make(chan struct{})
	defer close(done)
	go func() {
		c, err := control.Dial(socket)
		if err != nil {
			ch <- result{}
			return
		}
		defer func() { _ = c.Close() }()
		select {
		case s, ok := <-c.Updates():
			ch <- result{s, ok}
		case <-done:
		}
	}()
	select {
	case r := <-ch:
		return r.snap, r.ok
	case <-time.After(Timeout):
		return route.Snapshot{}, false
	}
}

// Message returns the switch line for the hook's turn, or "" when there is
// none: no decision for the turn yet, no earlier turn, or the same route.
// With no turn id in the input, the thread's latest turn is used.
func Message(s route.Snapshot, in Input) string {
	turns := lastPerTurn(s, in.SessionID)
	cur := len(turns) - 1
	if in.TurnID != "" {
		cur = slices.IndexFunc(turns, func(d route.Decision) bool { return d.TurnID == in.TurnID })
	}
	if cur < 1 {
		return ""
	}
	d, prev := turns[cur], turns[cur-1]
	if d.Route == prev.Route {
		return ""
	}
	return fmt.Sprintf("%s%s · %s → %s · %s · %s", MessagePrefix, d.Tier, d.Activity, d.Route.Model, d.Route.Effort, d.Why)
}

// lastPerTurn returns the final decision of each turn of the thread, ordered
// by that decision's time. Snapshot order is not relied on.
func lastPerTurn(s route.Snapshot, thread string) []route.Decision {
	all := slices.Clone(s.Recent)
	if s.Last != nil {
		all = append(all, *s.Last)
	}
	slices.SortStableFunc(all, func(a, b route.Decision) int { return a.At.Compare(b.At) })
	var turns []route.Decision
	for _, d := range all {
		if d.ThreadID != thread || d.TurnID == "" {
			continue
		}
		turns = slices.DeleteFunc(turns, func(t route.Decision) bool { return t.TurnID == d.TurnID })
		turns = append(turns, d)
	}
	return turns
}
