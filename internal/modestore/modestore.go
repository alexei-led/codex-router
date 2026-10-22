// Package modestore keeps each session's routing mode override: one small
// file per Codex thread under <state>/codex-router/modes/<home id>/. The
// override then survives a launcher or brain restart and a Codex resume of
// the thread, and a new thread starts without one.
package modestore

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"time"

	"github.com/alexei-led/codex-router/internal/config"
	"github.com/alexei-led/codex-router/internal/statedir"
)

// MaxAge is how long an override may go unused before Open deletes it. A
// load counts as use, so a thread still being resumed keeps its override.
const MaxAge = 30 * 24 * time.Hour

// threadID is what a store file may be named after: Codex thread and session
// ids, never a path.
var threadID = regexp.MustCompile(`^[A-Za-z0-9_-][A-Za-z0-9._-]{0,127}$`)

// Store is one home's overrides. Safe for concurrent use: each thread has its
// own file, written atomically.
type Store struct {
	dir string
	log *slog.Logger
}

type record struct {
	Mode config.RoutingMode `json:"mode"`
}

// Open returns the store of home homeID under app (statedir.Dir), creating
// its dir private, and deletes overrides unused for MaxAge.
func Open(app, homeID string, log *slog.Logger) (*Store, error) {
	if !threadID.MatchString(homeID) {
		return nil, fmt.Errorf("mode store: bad home id %q", homeID)
	}
	dir := filepath.Join(app, "modes", homeID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("mode store: %w", err)
	}
	for _, d := range []string{filepath.Dir(dir), dir} {
		if err := statedir.CheckPrivate(d); err != nil {
			return nil, fmt.Errorf("mode store: %w", err)
		}
	}
	s := &Store{dir: dir, log: log}
	s.prune(time.Now())
	return s, nil
}

func (s *Store) prune(now time.Time) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		s.log.Warn("mode-store-prune-failed", "err", err)
		return
	}
	for _, e := range entries {
		info, err := e.Info()
		if err != nil || !info.Mode().IsRegular() || now.Sub(info.ModTime()) < MaxAge {
			continue
		}
		if err := os.Remove(filepath.Join(s.dir, e.Name())); err != nil && !errors.Is(err, fs.ErrNotExist) {
			s.log.Warn("mode-store-prune-failed", "err", err)
		}
	}
}

func (s *Store) path(thread string) (string, bool) {
	if !threadID.MatchString(thread) {
		s.log.Warn("mode-store-bad-thread", "thread", thread)
		return "", false
	}
	return filepath.Join(s.dir, thread+".json"), true
}

// Load is thread's override, "" for none. A file that does not read or
// parse, or names an unknown mode, is no override. A load marks the file used.
func (s *Store) Load(thread string) config.RoutingMode {
	path, ok := s.path(thread)
	if !ok {
		return ""
	}
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return ""
	}
	var r record
	if err == nil {
		dec := json.NewDecoder(bytes.NewReader(b))
		dec.DisallowUnknownFields()
		err = dec.Decode(&r)
	}
	if err == nil && !slices.Contains(config.RoutingModes, r.Mode) {
		err = fmt.Errorf("unknown mode %q", r.Mode)
	}
	if err != nil {
		s.log.Warn("mode-store-unreadable", "thread", thread, "err", err)
		return ""
	}
	now := time.Now()
	_ = os.Chtimes(path, now, now)
	return r.Mode
}

// Save stores thread's override; "" deletes it. A failure only warns: the
// override still holds in the running engine.
func (s *Store) Save(thread string, m config.RoutingMode) {
	path, ok := s.path(thread)
	if !ok {
		return
	}
	var err error
	if m == "" {
		if err = os.Remove(path); errors.Is(err, fs.ErrNotExist) {
			err = nil
		}
	} else {
		b, _ := json.Marshal(record{Mode: m})
		err = statedir.WriteAtomic(s.dir, filepath.Base(path), b)
	}
	if err != nil {
		s.log.Warn("mode-store-write-failed", "thread", thread, "err", err)
	}
}
