package brain

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"

	"github.com/alexei-led/codex-router/internal/route"
)

const (
	intentFile    = "intent.json"
	intentVersion = 1
)

// intentRecord is intent.json: the engine's intent with a format version.
type intentRecord struct {
	Version int `json:"version"`
	route.Intent
}

// intentStore keeps a home's intent in intent.json, written atomically on
// every change: a crash skips shutdown handlers, so nothing waits for one.
type intentStore struct {
	path string
	log  *slog.Logger

	mu  sync.Mutex
	rev uint64 // the newest Rev written or tried
}

func newIntentStore(dir string, log *slog.Logger) *intentStore {
	return &intentStore{path: filepath.Join(dir, intentFile), log: log}
}

// load reads the saved intent. No file is a first start; a file that does
// not read or parse, or has another version, is lost.
func (s *intentStore) load() (in route.Intent, lost bool) {
	b, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return route.Intent{}, false
	}
	var rec intentRecord
	if err == nil {
		err = json.Unmarshal(b, &rec)
	}
	if err == nil && rec.Version != intentVersion {
		err = fmt.Errorf("version %d, want %d", rec.Version, intentVersion)
	}
	if err != nil {
		s.log.Warn("intent-unreadable", "path", s.path, "err", err)
		return route.Intent{}, true
	}
	return rec.Intent, false
}

// save writes in unless a newer intent was already written; it is the
// engine's Deps.SaveIntent, called concurrently. A failed write only warns:
// routing goes on, and the next change writes again.
func (s *intentStore) save(in route.Intent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if in.Rev <= s.rev {
		return
	}
	s.rev = in.Rev
	b, err := json.Marshal(intentRecord{Version: intentVersion, Intent: in})
	if err == nil {
		err = writeAtomic(filepath.Dir(s.path), filepath.Base(s.path), append(b, '\n'))
	}
	if err != nil {
		s.log.Warn("intent-write-failed", "err", err)
	}
}

// writeAtomic replaces dir/name with data through a temp file (0600) and a
// rename, so a reader sees the old file or the new one, never a part.
func writeAtomic(dir, name string, data []byte) error {
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
