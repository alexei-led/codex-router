package setup

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/alexei-led/codex-router/internal/home"
	"github.com/alexei-led/codex-router/internal/proxy"
	"github.com/alexei-led/codex-router/internal/service"
	"github.com/alexei-led/codex-router/internal/statedir"
)

// ConfigFile is Codex's config in a home.
const ConfigFile = "config.toml"

// PluginEnvFile, in a Codex home, tells the codex-router plugin's scripts
// the binary and the state base setup used, as KEY=value lines. Codex runs
// plugin hooks with the env of whichever shell started the home's daemon,
// which may have neither on PATH or in XDG_STATE_HOME.
const PluginEnvFile = "codex-router.env"

const (
	logsDir         = "logs"
	portTries       = 20
	backupStamp     = "20060102T150405Z"
	pluginEnvHeader = "# codex-router setup wrote this for the codex-router plugin; setup --undo removes it.\n"
)

// Options configure Run.
type Options struct {
	Home home.Home
	// HomePath is the home as the user gave it (home.Given), symlinks kept.
	// The units get it as --home, so they resolve the same Home.Name.
	HomePath string
	// StateDir is the absolute state base, as $XDG_STATE_HOME; the units
	// get it as --state-dir.
	StateDir string
	// Config is the codex-router config the units load; empty lets them
	// use their default.
	Config string
	// Bin is the codex-router binary the units run.
	Bin string
	// DryRun prints what would change and writes nothing.
	DryRun bool
	// Undo removes the config blocks and the units; Purge also deletes the
	// home's state dir.
	Undo, Purge bool
	// FollowSymlink allows editing a symlinked config.toml's target, where
	// the token then lands (plan M9).
	FollowSymlink bool
	Service       *service.Manager
	Out           io.Writer
	// Now stamps backups; FreePort picks a port. Tests replace them.
	Now      func() time.Time
	FreePort func() (int, error)
}

type runner struct {
	o   Options
	app string // codex-router's state dir
	dir string // the home's state dir
}

// Run sets the home up, or undoes it with Options.Undo. It prints the
// resolved home first, then each change. Setup installs and loads the units
// before it writes config.toml, so Codex never points at a port nothing
// holds; undo restores config.toml before it removes the units.
func Run(ctx context.Context, o Options) error {
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.FreePort == nil {
		o.FreePort = freePort
	}
	if o.Out == nil {
		o.Out = io.Discard
	}
	_, _ = fmt.Fprintf(o.Out, "Codex home %s: %s (id %s)\n", o.Home.Name, o.Home.Path, o.Home.ID)
	if !filepath.IsAbs(o.StateDir) {
		return fmt.Errorf("setup: state dir %q is not an absolute path", o.StateDir)
	}
	app, err := statedir.Dir(func(k string) string {
		if k == "XDG_STATE_HOME" {
			return o.StateDir
		}
		return ""
	})
	if err != nil {
		return err
	}
	r := runner{o: o, app: app, dir: statedir.HomeDir(app, o.Home.ID)}
	if o.Undo {
		return r.undo(ctx)
	}
	return r.install(ctx)
}

func (r runner) printf(format string, args ...any) {
	_, _ = fmt.Fprintf(r.o.Out, format, args...)
}

func (r runner) install(ctx context.Context) error {
	if fi, err := os.Stat(r.o.Home.Path); err != nil || !fi.IsDir() {
		return fmt.Errorf("setup: Codex home %s does not exist; run codex with CODEX_HOME=%s once first", r.o.Home.Path, r.o.Home.Path)
	}
	target, err := r.configTarget()
	if err != nil {
		return err
	}
	src, existed, err := readConfig(target)
	if err != nil {
		return err
	}
	port, token, err := r.credentials()
	if err != nil {
		return err
	}
	out, err := AddProvider(src, Provider{Port: port, Token: token})
	if err != nil {
		return fmt.Errorf("setup: %s: %w", target, err)
	}
	r.printDiff(target, src, out)
	spec := service.Spec{
		HomeID: r.o.Home.ID, HomePath: r.o.HomePath, StateDir: r.o.StateDir, Config: r.o.Config,
		Bin: r.o.Bin, Port: port, LogDir: filepath.Join(r.dir, logsDir),
	}
	pluginEnv, err := r.pluginEnv()
	if err != nil {
		return err
	}
	if unmanaged, err := r.unmanagedPluginEnv(); err != nil {
		return err
	} else if unmanaged {
		return fmt.Errorf("setup: %s was not created by codex-router setup; move it before running setup", r.pluginEnvPath())
	}
	if r.o.DryRun {
		r.printf("dry run: would store port %d and a token in %s\n", port, r.dir)
		for _, u := range r.o.Service.Units(r.o.Home.ID) {
			r.printf("dry run: would install %s\n", u.Path)
		}
		r.printf("dry run: would write %s\n", r.pluginEnvPath())
		return nil
	}
	if err := r.writeState(port, token); err != nil {
		return err
	}
	r.printf("stored port %d and the token in %s\n", port, r.dir)
	if err := r.o.Service.Install(ctx, spec); err != nil {
		return err
	}
	for _, u := range r.o.Service.Units(r.o.Home.ID) {
		r.printf("installed %s\n", u.Path)
	}
	if err := statedir.WriteAtomic(r.o.Home.Path, PluginEnvFile, pluginEnv); err != nil {
		return fmt.Errorf("setup: write %s: %w", r.pluginEnvPath(), err)
	}
	r.printf("wrote %s\n", r.pluginEnvPath())
	if !bytes.Equal(src, out) {
		if err := r.writeConfig(target, src, existed, out); err != nil {
			return err
		}
	}
	r.printf("Codex home %s now uses the router at %s:%d. Threads already running keep their\n"+
		"provider until /new or a Codex restart; new threads use the router.\n", r.o.Home.Name, proxy.LoopbackHost, port)
	return nil
}

func (r runner) undo(ctx context.Context) error {
	target, err := r.configTarget()
	if err != nil {
		return err
	}
	src, existed, err := readConfig(target)
	if err != nil {
		return err
	}
	out, found, err := RemoveBlocks(src)
	switch {
	case err != nil:
		return fmt.Errorf("setup: %s: %w", target, err)
	case !found:
		r.printf("%s has no codex-router blocks\n", target)
	default:
		r.printDiff(target, src, out)
		if !r.o.DryRun {
			if err := r.writeConfig(target, src, existed, out); err != nil {
				return err
			}
		}
	}
	if r.o.DryRun {
		for _, u := range r.o.Service.Installed(r.o.Home.ID) {
			r.printf("dry run: would remove %s\n", u.Path)
		}
	} else {
		removed, err := r.o.Service.Uninstall(ctx, r.o.Home.ID)
		for _, u := range removed {
			r.printf("removed %s\n", u.Path)
		}
		if err != nil {
			return err
		}
	}
	if err := r.removePluginEnv(); err != nil {
		return err
	}
	if _, err := os.Lstat(r.dir); err != nil {
		return nil
	}
	switch {
	case !r.o.Purge:
		r.printf("kept state dir %s (--purge deletes it)\n", r.dir)
	case r.o.DryRun:
		r.printf("dry run: would delete %s\n", r.dir)
	default:
		if err := os.RemoveAll(r.dir); err != nil {
			return fmt.Errorf("setup: %w", err)
		}
		r.printf("deleted %s\n", r.dir)
	}
	return nil
}

func (r runner) pluginEnvPath() string { return filepath.Join(r.o.Home.Path, PluginEnvFile) }

// pluginEnv renders PluginEnvFile. The plugin reads it line by line, so a
// value must not hold a line break.
func (r runner) pluginEnv() ([]byte, error) {
	if strings.ContainsAny(r.o.Bin+r.o.StateDir, "\r\n") {
		return nil, errors.New("setup: the binary or state dir path holds a line break")
	}
	return fmt.Appendf(nil, pluginEnvHeader+
		"CODEX_ROUTER_BIN=%s\nXDG_STATE_HOME=%s\n", r.o.Bin, r.o.StateDir), nil
}

// unmanagedPluginEnv refuses symlinks and files without setup's ownership marker.
// The marker is unchanged from earlier releases so upgrades can reuse their files.
func (r runner) unmanagedPluginEnv() (bool, error) {
	p := r.pluginEnvPath()
	fi, err := os.Lstat(p)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("setup: %w", err)
	}
	if !fi.Mode().IsRegular() {
		return true, nil
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return false, fmt.Errorf("setup: %w", err)
	}
	return !bytes.HasPrefix(b, []byte(pluginEnvHeader)), nil
}

// removePluginEnv deletes only a file owned by setup.
func (r runner) removePluginEnv() error {
	p := r.pluginEnvPath()
	if unmanaged, err := r.unmanagedPluginEnv(); err != nil {
		return err
	} else if unmanaged {
		r.printf("kept %s: not created by codex-router setup\n", p)
		return nil
	}
	if _, err := os.Lstat(p); errors.Is(err, fs.ErrNotExist) {
		return nil
	} else if err != nil {
		return fmt.Errorf("setup: %w", err)
	}
	if r.o.DryRun {
		r.printf("dry run: would remove %s\n", p)
		return nil
	}
	if err := os.Remove(p); err != nil {
		return fmt.Errorf("setup: %w", err)
	}
	r.printf("removed %s\n", p)
	return nil
}

// configTarget is the file to edit: the home's config.toml, or with
// FollowSymlink the file it links to. A symlink usually points into a
// dotfiles repo, where the token would get committed (plan M9).
func (r runner) configTarget() (string, error) {
	p := filepath.Join(r.o.Home.Path, ConfigFile)
	fi, err := os.Lstat(p)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return p, nil
	case err != nil:
		return "", fmt.Errorf("setup: %w", err)
	case fi.Mode()&fs.ModeSymlink != 0:
		if !r.o.FollowSymlink {
			return "", fmt.Errorf("setup: %s is a symlink, likely into a dotfiles repo, and the provider token would land there; pass --follow-symlink to edit its target anyway", p)
		}
		t, err := filepath.EvalSymlinks(p)
		if err != nil {
			return "", fmt.Errorf("setup: %w", err)
		}
		return t, nil
	case !fi.Mode().IsRegular():
		return "", fmt.Errorf("setup: %s is not a regular file", p)
	}
	return p, nil
}

func readConfig(path string) (data []byte, existed bool, err error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("setup: %w", err)
	}
	return b, true, nil
}

func (r runner) printDiff(path string, a, b []byte) {
	if bytes.Equal(a, b) {
		r.printf("%s: no change\n", path)
		return
	}
	r.printf("%s:\n%s", path, Diff(a, b))
}

// writeConfig backs up the old config (when there was one), then replaces
// it with data, 0600: the file holds the token.
func (r runner) writeConfig(target string, old []byte, existed bool, data []byte) error {
	if existed {
		b, err := r.backup(old)
		if err != nil {
			return err
		}
		r.printf("backup: %s\n", b)
	}
	if err := statedir.WriteAtomic(filepath.Dir(target), filepath.Base(target), data); err != nil {
		return fmt.Errorf("setup: write %s: %w", target, err)
	}
	r.printf("wrote %s\n", target)
	return nil
}

// backup writes data to a new timestamped file (0600) next to the home's
// config.toml, never over an older backup.
func (r runner) backup(data []byte) (string, error) {
	base := filepath.Join(r.o.Home.Path, ConfigFile+".codex-router-"+r.o.Now().UTC().Format(backupStamp))
	for n := 1; ; n++ {
		p := base + ".bak"
		if n > 1 {
			p = base + "-" + strconv.Itoa(n) + ".bak"
		}
		f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		if err != nil {
			return "", fmt.Errorf("setup: backup: %w", err)
		}
		_, err = f.Write(data)
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return "", fmt.Errorf("setup: backup: %w", err)
		}
		return p, nil
	}
}

// credentials returns the stored port and token, or new ones. Reusing them
// keeps a re-run from moving a port the units already hold.
func (r runner) credentials() (port int, token string, err error) {
	port, _ = statedir.ReadPort(r.dir)
	if b, err := os.ReadFile(filepath.Join(r.dir, statedir.TokenFile)); err == nil {
		token = strings.TrimSpace(string(b))
	}
	if port == 0 {
		if port, err = r.pickPort(); err != nil {
			return 0, "", err
		}
	}
	if token == "" {
		token = newToken()
	}
	return port, token, nil
}

// pickPort returns a free loopback port no other set-up home uses: an edge
// that is down still owns its port.
func (r runner) pickPort() (int, error) {
	taken := map[int]bool{}
	others, _ := filepath.Glob(filepath.Join(statedir.HomeDir(r.app, "*"), statedir.PortFile))
	for _, p := range others {
		if dir := filepath.Dir(p); dir != r.dir {
			port, _ := statedir.ReadPort(dir)
			taken[port] = true
		}
	}
	for range portTries {
		p, err := r.o.FreePort()
		if err != nil {
			return 0, fmt.Errorf("setup: pick a port: %w", err)
		}
		if !taken[p] {
			return p, nil
		}
	}
	return 0, errors.New("setup: found no free port that no other home uses")
}

// writeState makes the home's state dir and its logs dir (0700, private up
// to codex-router/, as the edge and brain require) and stores port and
// token (0600).
func (r runner) writeState(port int, token string) error {
	if err := os.MkdirAll(filepath.Join(r.dir, logsDir), 0o700); err != nil {
		return fmt.Errorf("setup: %w", err)
	}
	if err := statedir.CheckHome(r.app, r.dir); err != nil {
		return fmt.Errorf("setup: %w", err)
	}
	for name, data := range map[string]string{statedir.PortFile: strconv.Itoa(port) + "\n", statedir.TokenFile: token + "\n"} {
		if err := statedir.WriteAtomic(r.dir, name, []byte(data)); err != nil {
			return fmt.Errorf("setup: write %s: %w", name, err)
		}
	}
	return nil
}

// freePort asks the kernel for a free loopback port.
func freePort() (int, error) {
	ln, err := net.Listen("tcp", net.JoinHostPort(proxy.LoopbackHost, "0"))
	if err != nil {
		return 0, err
	}
	defer func() { _ = ln.Close() }()
	return ln.Addr().(*net.TCPAddr).Port, nil
}

// newToken returns 128 random bits in hex. crypto/rand.Read does not fail.
func newToken() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
