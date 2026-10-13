package launcher

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/alexei-led/codex-router/internal/control"
)

const (
	logDirName     = "logs"
	liveDialWait   = 200 * time.Millisecond
	runtimeIDBytes = 4
)

// RuntimeBase returns the parent of the per-session runtime dirs (plan D7):
// $XDG_RUNTIME_DIR/codex-router, else $TMPDIR/codex-router, else
// /tmp/codex-router. Relative values are ignored.
func RuntimeBase(getenv func(string) string) string {
	for _, key := range []string{"XDG_RUNTIME_DIR", "TMPDIR"} {
		if dir := getenv(key); filepath.IsAbs(dir) {
			return filepath.Join(dir, RuntimeDirName)
		}
	}
	return filepath.Join("/tmp", RuntimeDirName)
}

// makeRuntimeDir creates base (0700) and a fresh <8-hex> session dir in it.
// control.Listen needs a private parent, and a base owned by someone else
// could swap session dirs, so both are checked.
func makeRuntimeDir(base string, getenv func(string) string) (dir, id string, err error) {
	if base == "" {
		base = RuntimeBase(getenv)
	}
	if err := os.MkdirAll(base, 0o700); err != nil {
		return "", "", fmt.Errorf("runtime dir: %w", err)
	}
	if err := checkPrivate(base); err != nil {
		return "", "", fmt.Errorf("runtime dir: %w", err)
	}
	for range 8 {
		b := make([]byte, runtimeIDBytes)
		_, _ = rand.Read(b)
		id = hex.EncodeToString(b)
		dir = filepath.Join(base, id)
		err = os.Mkdir(dir, 0o700)
		if err == nil {
			return dir, id, checkPrivate(dir)
		}
		if !errors.Is(err, fs.ErrExist) {
			break
		}
	}
	return "", "", fmt.Errorf("runtime dir: %w", err)
}

// checkPrivate accepts a real directory owned by this user with no group or
// other access.
func checkPrivate(dir string) error {
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

func writePort(dir string, port int) error {
	if err := os.WriteFile(filepath.Join(dir, PortFileName), []byte(strconv.Itoa(port)+"\n"), 0o600); err != nil {
		return fmt.Errorf("write proxy port: %w", err)
	}
	return nil
}

// NewestControlSocket returns the control socket of the most recently
// started live session under base. Session dirs that are not private to
// this user are skipped.
func NewestControlSocket(base string) (string, error) {
	entries, err := os.ReadDir(base)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return "", err
	}
	var best string
	var bestAt time.Time
	for _, e := range entries {
		dir := filepath.Join(base, e.Name())
		if checkPrivate(dir) != nil {
			continue
		}
		sock := filepath.Join(dir, control.SocketName)
		fi, err := os.Lstat(sock)
		if err != nil || fi.Mode().Type() != fs.ModeSocket || !live(sock) {
			continue
		}
		if best == "" || fi.ModTime().After(bestAt) {
			best, bestAt = sock, fi.ModTime()
		}
	}
	if best == "" {
		return "", fmt.Errorf("no running codex-router session under %s", base)
	}
	return best, nil
}

func live(sock string) bool {
	c, err := net.DialTimeout("unix", sock, liveDialWait)
	if err != nil {
		return false
	}
	_ = c.Close()
	return true
}

// logDir is $XDG_STATE_HOME/codex-router/logs, else
// ~/.local/state/codex-router/logs.
func logDir(getenv func(string) string) (string, error) {
	state := getenv("XDG_STATE_HOME")
	if !filepath.IsAbs(state) {
		home := getenv("HOME")
		if !filepath.IsAbs(home) {
			return "", errors.New("no XDG_STATE_HOME or HOME")
		}
		state = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(state, RuntimeDirName, logDirName), nil
}

// openLog returns the session logger and makes it the slog default until
// restore: the app-server client logs through the default, and nothing may
// reach the terminal while the Codex TUI owns it. The file is 0600 in a 0700
// dir. If it cannot be opened, one stderr line says so (the TUI has not
// started yet) and logs are dropped.
func openLog(o Options, id string) (*slog.Logger, func()) {
	log, closeFile := o.Logger, func() {}
	if log == nil {
		log = slog.New(slog.DiscardHandler)
		if f, err := openLogFile(o.getenv, id); err != nil {
			_, _ = fmt.Fprintf(o.Stderr, "codex-router: no log file: %v\n", err)
		} else {
			log = slog.New(slog.NewJSONHandler(f, &slog.HandlerOptions{Level: slog.LevelDebug}))
			closeFile = func() { _ = f.Close() }
		}
	}
	prev := slog.Default()
	slog.SetDefault(log)
	return log, func() {
		slog.SetDefault(prev)
		closeFile()
	}
}

func openLogFile(getenv func(string) string, id string) (*os.File, error) {
	dir, err := logDir(getenv)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return nil, err
	}
	return os.OpenFile(filepath.Join(dir, id+".log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
}
