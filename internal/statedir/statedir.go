package statedir

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

const (
	appDirName = "codex-router"
	homesDir   = "homes"
)

// Files in a home's dir: setup writes the provider port and token, the edge
// reads them.
const (
	PortFile  = "port"
	TokenFile = "token"
)

// Dir returns codex-router's state dir, $XDG_STATE_HOME/codex-router, else
// ~/.local/state/codex-router. Relative values are ignored. It creates
// nothing.
func Dir(getenv func(string) string) (string, error) {
	state := getenv("XDG_STATE_HOME")
	if !filepath.IsAbs(state) {
		h := getenv("HOME")
		if !filepath.IsAbs(h) {
			return "", errors.New("state dir: no XDG_STATE_HOME or HOME")
		}
		state = filepath.Join(h, ".local", "state")
	}
	return filepath.Join(state, appDirName), nil
}

// HomeDir is the stage 2 dir of a home, <app>/homes/<home id>/, where app is
// codex-router's state dir (Dir).
func HomeDir(app, homeID string) string {
	return filepath.Join(app, homesDir, homeID)
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

// CheckHome checks that the home dir at dir and its parents up to app are
// private (CheckPrivate): whoever can write there can swap the token or take
// the link socket.
func CheckHome(app, dir string) error {
	for _, d := range []string{app, filepath.Dir(dir), dir} {
		if err := CheckPrivate(d); err != nil {
			return err
		}
	}
	return nil
}

// ReadPort reads the provider port setup stored in the home dir at dir.
func ReadPort(dir string) (int, error) {
	path := filepath.Join(dir, PortFile)
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, fmt.Errorf("port: %w", err)
	}
	p, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil || p < 1 || p > 65535 {
		return 0, fmt.Errorf("%s does not hold a port", path)
	}
	return p, nil
}

// WriteAtomic replaces dir/name with data through a temp file (0600) and a
// rename, so a reader sees the old file or the new one, never a part. The
// temp file's name starts with a dot.
func WriteAtomic(dir, name string, data []byte) error {
	f, err := os.CreateTemp(dir, "."+name+".*")
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(f.Name(), filepath.Join(dir, name))
	}
	if err != nil {
		_ = os.Remove(f.Name())
	}
	return err
}
