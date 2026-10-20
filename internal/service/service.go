package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"text/template"
)

// Roles of a home's units.
const (
	RoleEdge  = "edge"
	RoleBrain = "brain"
)

// EdgeSocket names the edge's socket in the launchd Sockets dict and the
// systemd FileDescriptorName; the edge asks listen.Activated for it.
const EdgeSocket = "edge"

// State is a unit's state as Status reports it.
type State int

// Unit states.
const (
	Missing State = iota // no unit file: the home is not set up
	Down                 // installed, but not loaded or not running
	Up                   // loaded; for the edge, the port is held
)

func (s State) String() string {
	switch s {
	case Down:
		return "down"
	case Up:
		return "up"
	}
	return "not installed"
}

// Unit is one service unit of a home.
type Unit struct {
	Role  string
	Name  string // launchd label or systemd unit name
	Path  string // unit file
	State State
	// watch marks a unit Status reports and Ensure starts. The systemd edge
	// service is not watched: its socket starts it on demand.
	watch bool
	// loaded is launchd's view: the job is known, though maybe not running.
	loaded bool
}

// Spec is what the units of one home run.
type Spec struct {
	HomeID   string
	HomePath string
	// StateDir is the state base the units pass as --state-dir (plan M3):
	// service managers do not see the shell's XDG_STATE_HOME.
	StateDir string
	// Config is the codex-router config file passed as --config, for the
	// same reason; empty passes none.
	Config string
	Bin    string
	Port   int
	// LogDir holds launchd's edge.log and brain.log; systemd uses the
	// journal.
	LogDir string
}

// args is the command line of role's unit.
func (s Spec) args(role string) []string {
	a := []string{s.Bin}
	if s.Config != "" {
		a = append(a, "--config", s.Config)
	}
	return append(a, role, "--home", s.HomePath, "--state-dir", s.StateDir)
}

// Runner runs a service manager command and returns its combined output.
type Runner func(ctx context.Context, name string, args ...string) (string, error)

// Exec is the Runner that runs the command.
func Exec(ctx context.Context, name string, args ...string) (string, error) {
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

// Options select and place a Manager's backend.
type Options struct {
	GOOS string // "darwin" (launchd) or "linux" (systemd)
	// Home is the user's home dir; launchd agents go to Library/LaunchAgents.
	Home string
	// ConfigHome is $XDG_CONFIG_HOME; systemd user units go to
	// systemd/user in it. Empty or relative means Home/.config.
	ConfigHome string
	UID        int // launchd's gui/<uid> domain
	Run        Runner
}

type backend interface {
	units(homeID string) []Unit
	render(s Spec, u Unit) ([]byte, error)
	// reload makes the service manager reread unit files.
	reload(ctx context.Context) error
	load(ctx context.Context, us []Unit) error
	// unload stops and unloads us; errors are ignored, since a unit that
	// is not loaded is fine there.
	unload(ctx context.Context, us []Unit)
	state(ctx context.Context, u *Unit)
	start(ctx context.Context, us []Unit) error
}

// Manager installs and checks the units of homes.
type Manager struct {
	b backend
}

// New returns the Manager for o.GOOS.
func New(o Options) (*Manager, error) {
	if !filepath.IsAbs(o.Home) {
		return nil, errors.New("service: HOME is not an absolute path")
	}
	if o.Run == nil {
		o.Run = Exec
	}
	switch o.GOOS {
	case "darwin":
		return &Manager{launchd{dir: filepath.Join(o.Home, "Library", "LaunchAgents"), uid: o.UID, run: o.Run}}, nil
	case "linux":
		cfg := o.ConfigHome
		if !filepath.IsAbs(cfg) {
			cfg = filepath.Join(o.Home, ".config")
		}
		return &Manager{systemd{dir: filepath.Join(cfg, "systemd", "user"), run: o.Run}}, nil
	}
	return nil, fmt.Errorf("service: %s has no supported service manager", o.GOOS)
}

// Units lists the unit files of a home, installed or not.
func (m *Manager) Units(homeID string) []Unit { return m.b.units(homeID) }

// Installed lists the units of a home whose file exists.
func (m *Manager) Installed(homeID string) []Unit {
	var out []Unit
	for _, u := range m.b.units(homeID) {
		if _, err := os.Lstat(u.Path); err == nil {
			out = append(out, u)
		}
	}
	return out
}

// Install writes the units of s's home and loads them. Units already
// installed are unloaded first, so a changed port or binary takes effect.
func (m *Manager) Install(ctx context.Context, s Spec) error {
	units := m.b.units(s.HomeID)
	files := make([][]byte, len(units))
	for i, u := range units {
		b, err := m.b.render(s, u)
		if err != nil {
			return err
		}
		files[i] = b
	}
	if old := m.Installed(s.HomeID); len(old) > 0 {
		m.b.unload(ctx, old)
	}
	for i, u := range units {
		if err := os.MkdirAll(filepath.Dir(u.Path), 0o755); err != nil {
			return fmt.Errorf("service: %w", err)
		}
		if err := os.WriteFile(u.Path, files[i], 0o644); err != nil {
			return fmt.Errorf("service: %w", err)
		}
	}
	if err := m.b.reload(ctx); err != nil {
		return err
	}
	return m.b.load(ctx, units)
}

// Uninstall unloads a home's units and deletes their files. It returns the
// units it removed; none is not an error.
func (m *Manager) Uninstall(ctx context.Context, homeID string) ([]Unit, error) {
	units := m.Installed(homeID)
	if len(units) == 0 {
		return nil, nil
	}
	m.b.unload(ctx, units)
	var errs []error
	for _, u := range units {
		if err := os.Remove(u.Path); err != nil && !errors.Is(err, os.ErrNotExist) {
			errs = append(errs, fmt.Errorf("service: %w", err))
		}
	}
	errs = append(errs, m.b.reload(ctx))
	return units, errors.Join(errs...)
}

// Status reports the watched units of a home: the edge's socket and the
// brain.
func (m *Manager) Status(ctx context.Context, homeID string) []Unit {
	var out []Unit
	for _, u := range m.b.units(homeID) {
		if !u.watch {
			continue
		}
		if _, err := os.Lstat(u.Path); err == nil {
			m.b.state(ctx, &u)
		}
		out = append(out, u)
	}
	return out
}

type statusReader interface {
	readState(ctx context.Context, u *Unit) error
}

// StatusReadOnly reports watched unit states and returns errors from the
// platform status command. It never starts or changes a unit.
func (m *Manager) StatusReadOnly(ctx context.Context, homeID string) ([]Unit, error) {
	reader, ok := m.b.(statusReader)
	if !ok {
		return nil, errors.New("service: status reader is unavailable")
	}
	var out []Unit
	for _, u := range m.b.units(homeID) {
		if !u.watch {
			continue
		}
		if _, err := os.Lstat(u.Path); errors.Is(err, os.ErrNotExist) {
			u.State = Missing
		} else if err != nil {
			return out, fmt.Errorf("service: status %s: %w", u.Name, err)
		} else if err := reader.readState(ctx, &u); err != nil {
			return out, fmt.Errorf("service: status %s: %w", u.Name, err)
		}
		out = append(out, u)
	}
	return out, nil
}

// Ensured is what Ensure found or did.
type Ensured int

// Ensure results.
const (
	NotSetUp Ensured = iota // a unit file is missing: run setup
	AlreadyUp
	Started
)

// Ensure starts a set-up home's units that are down. It does not wait for
// them to run.
func (m *Manager) Ensure(ctx context.Context, homeID string) (Ensured, error) {
	var down []Unit
	for _, u := range m.Status(ctx, homeID) {
		switch u.State {
		case Missing:
			return NotSetUp, nil
		case Down:
			down = append(down, u)
		}
	}
	if len(down) == 0 {
		return AlreadyUp, nil
	}
	if err := m.b.start(ctx, down); err != nil {
		return NotSetUp, err
	}
	return Started, nil
}

// renderTemplate executes t with data.
func renderTemplate(t *template.Template, data any) ([]byte, error) {
	var b bytes.Buffer
	if err := t.Execute(&b, data); err != nil {
		return nil, fmt.Errorf("service: render %s: %w", t.Name(), err)
	}
	return b.Bytes(), nil
}
