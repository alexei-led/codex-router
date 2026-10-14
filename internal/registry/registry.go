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
)

const (
	appDirName = "codex-router"
	dirName    = "registry"
	fileExt    = ".json"
	dialWait   = 200 * time.Millisecond
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
	state := getenv("XDG_STATE_HOME")
	if !filepath.IsAbs(state) {
		h := getenv("HOME")
		if !filepath.IsAbs(h) {
			return "", errors.New("registry: no XDG_STATE_HOME or HOME")
		}
		state = filepath.Join(h, ".local", "state")
	}
	app := filepath.Join(state, appDirName)
	root := filepath.Join(app, dirName)
	if err := os.MkdirAll(root, 0o700); err != nil {
		return "", fmt.Errorf("registry: %w", err)
	}
	for _, dir := range []string{app, root} {
		if err := CheckPrivate(dir); err != nil {
			return "", fmt.Errorf("registry: %w", err)
		}
	}
	return root, nil
}

// CheckPrivate accepts a real directory (not a symlink) owned by this user
// with no group or other access.
func CheckPrivate(dir string) error {
	fi, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !fi.IsDir() {
		return fmt.Errorf("%s is not a directory", dir)
	}
	if fi.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("%s must not be accessible to group or others", dir)
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok && int(st.Uid) != os.Getuid() {
		return fmt.Errorf("%s is not owned by the current user", dir)
	}
	return nil
}

// Write stores ep as root/<ID>.json (0600). It writes a temp file and
// renames it over the entry, so readers see the old or the new entry, never
// a partial one.
func Write(root string, ep Endpoint) error {
	if !validID(ep.ID) {
		return fmt.Errorf("registry: bad endpoint id %q", ep.ID)
	}
	b, err := json.Marshal(ep)
	if err != nil {
		return fmt.Errorf("registry: %w", err)
	}
	// The leading dot keeps temp files out of List.
	f, err := os.CreateTemp(root, "."+ep.ID+".*")
	if err != nil {
		return fmt.Errorf("registry: %w", err)
	}
	tmp := f.Name()
	_, err = f.Write(b)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp, entryPath(root, ep.ID))
	}
	if err != nil {
		_ = os.Remove(tmp)
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

// Prune removes the entries isLive rejects and returns them. Skipped files
// stay: a file that does not parse is not proof its endpoint is gone.
func Prune(root string, isLive func(Endpoint) bool) ([]Endpoint, error) {
	eps, _, err := List(root)
	if err != nil {
		return nil, err
	}
	var removed []Endpoint
	var errs []error
	for _, ep := range eps {
		if isLive(ep) {
			continue
		}
		if err := Remove(root, ep.ID); err != nil {
			errs = append(errs, err)
			continue
		}
		removed = append(removed, ep)
	}
	return removed, errors.Join(errs...)
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
