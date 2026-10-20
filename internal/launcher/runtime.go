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
	"sort"
	"strconv"
	"time"

	"github.com/alexei-led/codex-router/internal/statedir"
)

const (
	logDirName        = "logs"
	logRetentionCount = 40
	runtimeIDBytes    = 4
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
	return openLogWith(o, id, os.Remove)
}

func openLogWith(o Options, id string, remove func(string) error) (*slog.Logger, func()) {
	log, closeFile := o.Logger, func() {}
	if log == nil {
		log = slog.New(slog.DiscardHandler)
		if f, retentionErrors, err := openLogFileWith(o.getenv, id, remove); err != nil {
			_, _ = fmt.Fprintf(o.Stderr, "codex-router: no log file: %v\n", err)
		} else {
			log = slog.New(slog.NewJSONHandler(f, &slog.HandlerOptions{Level: slog.LevelDebug}))
			for _, err := range retentionErrors {
				log.Warn("log-retention-failed", "err", err)
			}
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

func openLogFileWith(getenv func(string) string, id string, remove func(string) error) (*os.File, []error, error) {
	dir, err := logDir(getenv)
	if err != nil {
		return nil, nil, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, nil, err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return nil, nil, err
	}
	f, err := os.OpenFile(filepath.Join(dir, id+".log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, nil, err
	}
	return f, retainLogs(dir, f.Name(), remove), nil
}

type retainedLog struct {
	name    string
	path    string
	modTime time.Time
	active  bool
}

// retainLogs keeps the newest run logs by modification time. Only regular
// files named by the launcher runtime-id pattern are candidates; active is
// always kept and directory entries are inspected without following links.
func retainLogs(dir, active string, remove func(string) error) []error {
	dirInfo, err := os.Lstat(dir)
	if err != nil {
		return []error{err}
	}
	if !dirInfo.IsDir() || dirInfo.Mode()&os.ModeSymlink != 0 {
		return []error{errors.New("log path is not a real directory")}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return []error{err}
	}
	active = filepath.Clean(active)
	var logs []retainedLog
	for _, entry := range entries {
		name := entry.Name()
		if !isRunLogName(name) {
			continue
		}
		path := filepath.Join(dir, name)
		fi, err := os.Lstat(path)
		if err != nil || !fi.Mode().IsRegular() {
			continue
		}
		logs = append(logs, retainedLog{
			name: name, path: path, modTime: fi.ModTime(), active: filepath.Clean(path) == active,
		})
	}
	sort.Slice(logs, func(i, j int) bool {
		if logs[i].active != logs[j].active {
			return logs[i].active
		}
		if !logs[i].modTime.Equal(logs[j].modTime) {
			return logs[i].modTime.After(logs[j].modTime)
		}
		return logs[i].name > logs[j].name
	})
	if len(logs) <= logRetentionCount {
		return nil
	}
	if remove == nil {
		remove = os.Remove
	}
	var errs []error
	for _, file := range logs[logRetentionCount:] {
		// Recheck the entry immediately before unlinking. os.Remove unlinks a
		// symlink rather than following it, but do not remove one as a candidate.
		fi, err := os.Lstat(file.path)
		if err != nil || !fi.Mode().IsRegular() {
			continue
		}
		if err := remove(file.path); err != nil {
			errs = append(errs, fmt.Errorf("remove %s: %w", file.name, err))
		}
	}
	return errs
}

func isRunLogName(name string) bool {
	if len(name) != 12 || name[8:] != ".log" {
		return false
	}
	for _, r := range name[:8] {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}
