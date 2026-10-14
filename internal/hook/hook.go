package hook

import (
	"encoding/json"
	"errors"
	"io"
	"slices"
	"strings"
	"time"

	"github.com/alexei-led/codex-router/internal/control"
	"github.com/alexei-led/codex-router/internal/home"
	"github.com/alexei-led/codex-router/internal/registry"
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

// Timeout bounds the whole hook run, registry lookup and socket included.
const Timeout = 200 * time.Millisecond

// Run reads one hook input from stdin, finds the endpoint that routes its
// thread, asks it for that thread's snapshot and writes
// {"systemMessage": ...} when the turn's route differs from the previous
// turn's, else {}. The endpoint is the control socket named by
// control.EnvSocket (read through getenv; set by the launcher), else the
// registry endpoint of the hook's Codex home that lists the thread (plain
// codex). No endpoint, unreadable input or a slow stdin, registry or router
// all print {} within Timeout and return nil: a hook must never block Codex.
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
		go func() { snapCh <- fetch(getenv, in.SessionID) }()
		select {
		case snap := <-snapCh:
			out.SystemMessage = Message(snap, in)
		case <-deadline:
		}
	case <-deadline:
	}
	return json.NewEncoder(stdout).Encode(out)
}

// fetch returns thread's snapshot, or the zero Snapshot when no endpoint
// routes the thread. The caller abandons it at the deadline; the process
// exits right after.
func fetch(getenv func(string) string, thread string) route.Snapshot {
	if thread == "" {
		return route.Snapshot{}
	}
	if socket := getenv(control.EnvSocket); socket != "" {
		snap, _ := snapshotAt(registry.Endpoint{Socket: socket}, thread)
		return snap
	}
	return lookup(getenv, thread)
}

// lookup asks every registry endpoint of the hook's Codex home, in
// parallel, and returns the snapshot from the first that lists thread. A
// thread id alone may repeat across homes (M1), so other homes are never
// asked. The hook only reads the registry: pruning is the panel's job.
func lookup(getenv func(string) string, thread string) route.Snapshot {
	h, err := home.Resolve(getenv)
	if err != nil {
		return route.Snapshot{}
	}
	root, err := registry.Root(getenv)
	if err != nil {
		return route.Snapshot{}
	}
	eps, _, err := registry.List(root)
	if err != nil {
		return route.Snapshot{}
	}
	eps = slices.DeleteFunc(eps, func(ep registry.Endpoint) bool { return ep.Home.ID != h.ID })
	found := make(chan route.Snapshot, len(eps))
	failed := make(chan struct{}, len(eps))
	for _, ep := range eps {
		go func() {
			if snap, err := snapshotAt(ep, thread); err == nil {
				found <- snap
				return
			}
			failed <- struct{}{}
		}()
	}
	for range eps {
		select {
		case snap := <-found:
			return snap
		case <-failed:
		}
	}
	return route.Snapshot{}
}

// snapshotAt dials ep and returns thread's snapshot. For a registry entry
// (one with an ID) the hello must name the entry's endpoint and home (M3),
// and the endpoint's first sessions list must include thread.
func snapshotAt(ep registry.Endpoint, thread string) (route.Snapshot, error) {
	c, err := control.Dial(ep.Socket)
	if err != nil {
		return route.Snapshot{}, err
	}
	defer func() { _ = c.Close() }()
	hello := c.Hello()
	if ep.ID != "" {
		if hello.EndpointID != ep.ID || hello.Home.ID != ep.Home.ID {
			return route.Snapshot{}, errors.New("stale registry entry")
		}
		items, ok := <-c.Sessions()
		if !ok {
			return route.Snapshot{}, control.ErrDisconnected
		}
		if !slices.ContainsFunc(items, func(s route.SessionInfo) bool { return s.ThreadID == thread }) {
			return route.Snapshot{}, errors.New("thread not listed")
		}
	}
	if err := c.Subscribe(control.SessionID(hello.Home.ID, thread)); err != nil {
		return route.Snapshot{}, err
	}
	snap, ok := <-c.Snapshots()
	if !ok {
		return route.Snapshot{}, control.ErrDisconnected
	}
	return snap, nil
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
	// A pinned turn has no activity and a route may keep Codex's effort:
	// empty parts are left out rather than shown as "·  ·".
	from := strings.Join(nonEmpty(string(d.Tier), string(d.Activity)), " · ")
	to := strings.Join(nonEmpty(d.Route.Model, string(d.Route.Effort), d.Why), " · ")
	return MessagePrefix + from + " → " + to
}

func nonEmpty(parts ...string) []string {
	return slices.DeleteFunc(parts, func(p string) bool { return p == "" })
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
