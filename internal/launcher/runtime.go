package launcher

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"

	"github.com/alexei-led/codex-router/internal/statedir"
)

const (
	logDirName     = "logs"
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
	if err := statedir.CheckPrivate(base); err != nil {
		return "", "", fmt.Errorf("runtime dir: %w", err)
	}
	for range 8 {
		b := make([]byte, runtimeIDBytes)
		_, _ = rand.Read(b)
		id = hex.EncodeToString(b)
		dir = filepath.Join(base, id)
		err = os.Mkdir(dir, 0o700)
		if err == nil {
			return dir, id, statedir.CheckPrivate(dir)
		}
		if !errors.Is(err, fs.ErrExist) {
			break
		}
	}
	return "", "", fmt.Errorf("runtime dir: %w", err)
}

func writePort(dir string, port int) error {
	if err := os.WriteFile(filepath.Join(dir, PortFileName), []byte(strconv.Itoa(port)+"\n"), 0o600); err != nil {
		return fmt.Errorf("write proxy port: %w", err)
	}
	return nil
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
