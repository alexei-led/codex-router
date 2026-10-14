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
}

// Output is the hook's stdout. Only systemMessage is ever set.
type Output struct {
	SystemMessage string `json:"systemMessage,omitempty"`
}

// MessagePrefix starts every line; Codex renders it as "↳ Hook · router: …".
const MessagePrefix = "router: "

// Timeout bounds the whole hook run, socket included.
const Timeout = 200 * time.Millisecond

// Run reads one hook input from stdin, asks the control socket named by
// control.EnvSocket (read through getenv) for its thread's snapshot and
// writes {"systemMessage": ...} when the turn's route differs from the
// previous turn's, else {}. A missing or unreachable socket, unreadable input
// or a slow stdin or router all print {} within Timeout and return nil: a
// hook must never block Codex.
func Run(stdin io.Reader, stdout io.Writer, getenv func(string) string) error {
	deadline := time.After(Timeout)
	inCh := make(chan Input, 1)
	go func() {
		var in Input
		if json.NewDecoder(stdin).Decode(&in) != nil {
			in = Input{}
		}
		inCh <- in
	}()

	var out Output
	select {
	case in := <-inCh:
		snapCh := make(chan route.Snapshot, 1)
		go func() { snapCh <- fetch(getenv(control.EnvSocket), in.SessionID) }()
		select {
		case snap := <-snapCh:
			out.SystemMessage = Message(snap, in)
		case <-deadline:
		}
	case <-deadline:
	}
	return json.NewEncoder(stdout).Encode(out)
}

// fetch returns thread's snapshot, or the zero Snapshot when the socket is
// unset or unreachable or the thread is unknown. The caller abandons it at
// the deadline; the process exits right after.
func fetch(socket, thread string) route.Snapshot {
	if socket == "" || thread == "" {
		return route.Snapshot{}
	}
	c, err := control.Dial(socket)
	if err != nil {
		return route.Snapshot{}
	}
	defer func() { _ = c.Close() }()
	if c.Subscribe(control.SessionID(c.Hello().Home.ID, thread)) != nil {
		return route.Snapshot{}
	}
	return <-c.Snapshots()
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
