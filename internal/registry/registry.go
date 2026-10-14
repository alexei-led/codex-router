package registry

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/alexei-led/codex-router/internal/home"
	"github.com/alexei-led/codex-router/internal/statedir"
)

const (
	dirName  = "registry"
	fileExt  = ".json"
	dialWait = 200 * time.Millisecond
)

// Endpoint kinds: a stage 1 launcher session or a stage 2 per-home brain.
const (
	KindSession = "session"
	KindBrain   = "brain"
)

// Endpoint is one registry entry, stored as <ID>.json.
type Endpoint struct {
	ID       string    `json:"endpoint_id"`
	Kind     string    `json:"kind"`
	Protocol int       `json:"protocol"`
	PID      int       `json:"pid"`
	Started  time.Time `json:"started"`
	Home     home.Home `json:"home"`
	Socket   string    `json:"socket"`
}

// Skipped is a registry file List could not use.
type Skipped struct {
	Path string
	Err  error
}

// Root returns the registry dir, $XDG_STATE_HOME/codex-router/registry, else
// ~/.local/state/codex-router/registry, creating it 0700. Relative values
// are ignored. Both the codex-router dir and the registry dir must be
// private to this user: whoever can write there can point clients at their
// own socket.
func Root(getenv func(string) string) (string, error) {
	app, err := statedir.Dir(getenv)
	if err != nil {
		return "", err
	}
	root := filepath.Join(app, dirName)
	if err := os.MkdirAll(root, 0o700); err != nil {
		return "", fmt.Errorf("registry: %w", err)
	}
	for _, dir := range []string{app, root} {
		if err := statedir.CheckPrivate(dir); err != nil {
			return "", fmt.Errorf("registry: %w", err)
		}
	}
	return root, nil
}

// Write stores ep as root/<ID>.json (0600) atomically, so readers see the
// old or the new entry, never a partial one. The temp file's leading dot
// keeps it out of List.
func Write(root string, ep Endpoint) error {
	if !validID(ep.ID) {
		return fmt.Errorf("registry: bad endpoint id %q", ep.ID)
	}
	b, err := json.Marshal(ep)
	if err != nil {
		return fmt.Errorf("registry: %w", err)
	}
	if err := statedir.WriteAtomic(root, ep.ID+fileExt, b); err != nil {
		return fmt.Errorf("registry: write %s: %w", ep.ID, err)
	}
	return nil
}

// Remove deletes the entry for id. A missing entry is not an error: a
// reader may have pruned it first.
func Remove(root, id string) error {
	if !validID(id) {
		return fmt.Errorf("registry: bad endpoint id %q", id)
	}
	if err := os.Remove(entryPath(root, id)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("registry: %w", err)
	}
	return nil
}

// List returns the entries in root ordered by start time. Files that are not
// regular, do not parse, or whose endpoint_id differs from the file name are
// returned as skipped instead of failing the whole list.
func List(root string) ([]Endpoint, []Skipped, error) {
	des, err := os.ReadDir(root)
	if err != nil {
		return nil, nil, fmt.Errorf("registry: %w", err)
	}
	var eps []Endpoint
	var skipped []Skipped
	for _, de := range des {
		name := de.Name()
		id, ok := strings.CutSuffix(name, fileExt)
		if !ok || strings.HasPrefix(name, ".") {
			continue
		}
		p := filepath.Join(root, name)
		ep, err := readEntry(p, de.Type())
		if err == nil && ep.ID != id {
			err = fmt.Errorf("endpoint_id %q does not match the file name", ep.ID)
		}
		if err != nil {
			skipped = append(skipped, Skipped{Path: p, Err: err})
			continue
		}
		eps = append(eps, ep)
	}
	slices.SortFunc(eps, func(a, b Endpoint) int {
		return cmp.Or(a.Started.Compare(b.Started), cmp.Compare(a.ID, b.ID))
	})
	return eps, skipped, nil
}

func readEntry(p string, typ os.FileMode) (Endpoint, error) {
	var ep Endpoint
	if !typ.IsRegular() {
		return ep, errors.New("not a regular file")
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return ep, err
	}
	err = json.Unmarshal(b, &ep)
	return ep, err
}

// Prune removes ep's entry, read earlier by List, when its endpoint is dead
// (Alive). An entry rewritten since (a restart reusing the id) stays: Prune
// re-reads it and removes it only if its pid and start time are unchanged. A
// write between that read and the remove can still lose; the entry then
// comes back with the endpoint's next registration.
func Prune(root string, ep Endpoint) error {
	if Alive(ep) || !unchanged(root, ep) {
		return nil
	}
	return Remove(root, ep.ID)
}

// unchanged reports whether the entry for ep.ID still has ep's pid and start.
func unchanged(root string, ep Endpoint) bool {
	cur, err := readEntry(entryPath(root, ep.ID), 0)
	return err == nil && cur.PID == ep.PID && cur.Started.Equal(ep.Started)
}

// Alive reports whether ep's socket answers or its process still exists. The
// pid check keeps an endpoint that has registered but not yet listens. A
// reused pid keeps a dead entry until that process exits too; the hello
// handshake catches it meanwhile.
func Alive(ep Endpoint) bool {
	if ep.Socket != "" {
		if c, err := net.DialTimeout("unix", ep.Socket, dialWait); err == nil {
			_ = c.Close()
			return true
		}
	}
	return pidAlive(ep.PID)
}

func pidAlive(pid int) bool {
	// Kill with pid <= 0 signals process groups, not one process.
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// validID keeps an id to one plain file name that List would show.
func validID(id string) bool {
	return id != "" && !strings.HasPrefix(id, ".") && !strings.ContainsAny(id, `/\`)
}

func entryPath(root, id string) string {
	return filepath.Join(root, id+fileExt)
}
