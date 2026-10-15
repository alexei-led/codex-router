package sessions

import (
	"cmp"
	"errors"
	"maps"
	"net"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/alexei-led/codex-router/internal/config"
	"github.com/alexei-led/codex-router/internal/control"
	"github.com/alexei-led/codex-router/internal/home"
	"github.com/alexei-led/codex-router/internal/registry"
	"github.com/alexei-led/codex-router/internal/route"
)

// listTimeout bounds the wait for an endpoint's first sessions list.
const listTimeout = 2 * time.Second

// Session is one running session with the endpoint serving it, as
// sessions --json prints it.
type Session struct {
	route.SessionInfo
	Endpoint string `json:"endpoint_id"`
	HomeName string `json:"-"`
	base     string // the label before Disambiguate, shared by colliding sessions
	socket   string // the endpoint's control socket
}

// List lists the sessions of the endpoint at socket, or, when socket is "",
// of every endpoint in the registry at root.
func List(socket, root string) ([]Session, error) {
	if socket != "" {
		return collect([]registry.Endpoint{{Socket: socket}}, func(registry.Endpoint) {}), nil
	}
	eps, _, err := registry.List(root)
	if err != nil {
		return nil, err
	}
	return collect(eps, func(ep registry.Endpoint) { _ = registry.Prune(root, ep) }), nil
}

// collect asks every endpoint for its sessions, in parallel, and returns
// them in panel order (home name, then most recently active) with the
// panel's labels. An endpoint that does not answer is skipped, and pruned
// when it is dead. A session two endpoints list is taken from the more
// recently active report.
func collect(eps []registry.Endpoint, prune func(registry.Endpoint)) []Session {
	var mu sync.Mutex
	byID := map[string]Session{}
	var wg sync.WaitGroup
	for _, ep := range eps {
		wg.Go(func() {
			h, items, err := endpointSessions(ep)
			if err != nil {
				prune(ep)
				return
			}
			mu.Lock()
			defer mu.Unlock()
			for _, it := range items {
				if old, ok := byID[it.ID]; ok && !it.LastActive.After(old.LastActive) {
					continue
				}
				byID[it.ID] = Session{SessionInfo: it, Endpoint: h.EndpointID, HomeName: h.Home.Name, socket: ep.Socket}
			}
		})
	}
	wg.Wait()
	out := slices.AppendSeq(make([]Session, 0, len(byID)), maps.Values(byID))
	slices.SortFunc(out, func(a, b Session) int {
		return cmp.Or(
			cmp.Compare(a.HomeName, b.HomeName), cmp.Compare(a.Home, b.Home),
			b.LastActive.Compare(a.LastActive), cmp.Compare(a.ID, b.ID))
	})
	labels, ids := make([]string, len(out)), make([]string, len(out))
	for i, s := range out {
		labels[i], ids[i] = s.Label, s.ID
	}
	for i, l := range home.Disambiguate(labels, ids) {
		out[i].base, out[i].Label = out[i].Label, l
	}
	return out
}

// endpointSessions dials ep and returns its hello and first sessions list.
// A hello naming another endpoint or home than the entry is an error: the
// entry is stale (M3). An entry without a home (--socket) is not checked.
func endpointSessions(ep registry.Endpoint) (control.Hello, []route.SessionInfo, error) {
	c, err := control.Dial(ep.Socket)
	if err != nil {
		return control.Hello{}, nil, err
	}
	defer func() { _ = c.Close() }()
	h := c.Hello()
	if ep.Home.ID != "" && (h.EndpointID != ep.ID || h.Home.ID != ep.Home.ID) {
		return h, nil, errors.New("stale registry entry")
	}
	select {
	case items, ok := <-c.Sessions():
		if !ok {
			return h, nil, control.ErrDisconnected
		}
		return h, items, nil
	case <-time.After(listTimeout):
		return h, nil, errors.New("no sessions list")
	}
}

// Resolve returns the sessions query names under the first rule that
// matches any: an exact session or thread id; a session or thread id
// prefix; an exact label; a label suffix starting at a separator. Label
// rules try the shown label and the one before disambiguation, so a label
// that several sessions share names all of them. One match resolves;
// several are ambiguous.
func Resolve(sessions []Session, query string) []Session {
	rules := []func(Session) bool{
		func(s Session) bool { return s.ID == query || s.ThreadID == query },
		func(s Session) bool {
			return strings.HasPrefix(s.ID, query) || strings.HasPrefix(s.ThreadID, query)
		},
		func(s Session) bool { return s.Label == query || s.base == query },
		func(s Session) bool {
			return strings.HasSuffix(s.Label, home.LabelSep+query) || strings.HasSuffix(s.base, home.LabelSep+query)
		},
	}
	for _, match := range rules {
		var got []Session
		for _, s := range sessions {
			if match(s) {
				got = append(got, s)
			}
		}
		if len(got) > 0 {
			return got
		}
	}
	return nil
}

// RouteText formats a route as the panel does.
func RouteText(r config.Route) string {
	switch {
	case r.Model == "":
		return "-"
	case r.Effort == "":
		return r.Model
	}
	return r.Model + " · " + string(r.Effort)
}

// dialSocket connects to a control socket and hangs up; tests replace it.
var dialSocket = func(path string) error {
	c, err := net.DialTimeout("unix", path, time.Second)
	if err == nil {
		_ = c.Close()
	}
	return err
}

// Blocked returns the dial error of the first registry endpoint of the home
// whose socket refuses this process (EPERM or EACCES): Codex's sandbox does
// that to shell commands, and the router behind it is not dead.
func Blocked(root, homeID string) error {
	eps, _, err := registry.List(root)
	if err != nil {
		return nil
	}
	for _, ep := range eps {
		if ep.Home.ID != homeID {
			continue
		}
		if err := dialSocket(ep.Socket); errors.Is(err, syscall.EPERM) || errors.Is(err, syscall.EACCES) {
			return err
		}
	}
	return nil
}

// ForShell returns the session of thread when one is listed, else the
// sessions whose cwd is the deepest one equal to or containing dir. Paths
// are compared with symlinks resolved: a shell may see /tmp where Codex
// reports /private/tmp.
func ForShell(sessions []Session, thread, dir string) []Session {
	if thread != "" {
		if i := slices.IndexFunc(sessions, func(s Session) bool { return s.ThreadID == thread }); i >= 0 {
			return sessions[i : i+1]
		}
	}
	dir = canonicalPath(dir)
	var best []Session
	depth := -1
	for _, s := range sessions {
		cwd := canonicalPath(s.Cwd)
		if !filepath.IsAbs(cwd) {
			continue
		}
		rel, err := filepath.Rel(cwd, dir)
		if err != nil || rel == ".." || strings.HasPrefix(rel, "../") {
			continue
		}
		switch {
		case len(cwd) > depth:
			best, depth = []Session{s}, len(cwd)
		case len(cwd) == depth:
			best = append(best, s)
		}
	}
	return best
}

// canonicalPath resolves symlinks in p, or cleans p when it does not exist.
func canonicalPath(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return filepath.Clean(p)
}

// Send sends cmd to the endpoint that listed s.
func Send(s Session, cmd route.Command) error {
	cl, err := control.Dial(s.socket)
	if err != nil {
		return err
	}
	defer func() { _ = cl.Close() }()
	if cl.Hello().EndpointID != s.Endpoint {
		return errors.New("the session's router changed; try again")
	}
	return cl.Send(cmd)
}
