package modestore

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/alexei-led/codex-router/internal/config"
)

func open(t *testing.T, app string) (*Store, *bytes.Buffer) {
	t.Helper()
	var logs bytes.Buffer
	s, err := Open(app, "home1", slog.New(slog.NewTextHandler(&logs, nil)))
	if err != nil {
		t.Fatal(err)
	}
	return s, &logs
}

func TestSaveLoadClear(t *testing.T) {
	app := t.TempDir()
	s, _ := open(t, app)
	s.Save("019a-thread", config.ModeCost)

	again, _ := open(t, app)
	if got := again.Load("019a-thread"); got != config.ModeCost {
		t.Fatalf("Load after reopen = %q, want cost", got)
	}
	if got := again.Load("other"); got != "" {
		t.Fatalf("Load of another thread = %q, want none", got)
	}
	again.Save("019a-thread", "")
	if got := again.Load("019a-thread"); got != "" {
		t.Fatalf("Load after clear = %q, want none", got)
	}
	if _, err := os.Stat(filepath.Join(app, "modes", "home1", "019a-thread.json")); !os.IsNotExist(err) {
		t.Fatalf("clear left the file: %v", err)
	}
	again.Save("019a-thread", "")
}

func TestFilesArePrivate(t *testing.T) {
	app := t.TempDir()
	s, _ := open(t, app)
	s.Save("t1", config.ModeQuality)
	dir := filepath.Join(app, "modes", "home1")
	for path, want := range map[string]os.FileMode{dir: 0o700, filepath.Join(dir, "t1.json"): 0o600} {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != want {
			t.Errorf("%s: %v, %v; want %v", path, info, err, want)
		}
	}
}

func TestBadThreadIDsAreRefused(t *testing.T) {
	app := t.TempDir()
	s, logs := open(t, app)
	for _, id := range []string{"", "../escape", "a/b", ".hidden", strings.Repeat("x", 200)} {
		s.Save(id, config.ModeCost)
		if got := s.Load(id); got != "" {
			t.Errorf("Load(%q) = %q, want none", id, got)
		}
	}
	if _, err := os.Stat(filepath.Join(app, "modes", "escape.json")); !os.IsNotExist(err) {
		t.Fatalf("a bad id escaped the store dir: %v", err)
	}
	if !strings.Contains(logs.String(), "mode-store-bad-thread") {
		t.Fatalf("logs = %s, want the refusal", logs)
	}
}

func TestCorruptFilesMeanNoOverride(t *testing.T) {
	app := t.TempDir()
	s, _ := open(t, app)
	dir := filepath.Join(app, "modes", "home1")
	for name, body := range map[string]string{"bad.json": "{", "unknown.json": `{"mode":"lavish"}`, "extra.json": `{"mode":"cost","x":1}`} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if got := s.Load(strings.TrimSuffix(name, ".json")); got != "" {
			t.Errorf("%s: Load = %q, want none", name, got)
		}
	}
}

func TestOpenPrunesFilesUnusedFor30Days(t *testing.T) {
	app := t.TempDir()
	s, _ := open(t, app)
	s.Save("old", config.ModeCost)
	s.Save("used", config.ModeCost)
	s.Save("fresh", config.ModeQuality)
	dir := filepath.Join(app, "modes", "home1")
	old := time.Now().Add(-MaxAge - time.Hour)
	for _, name := range []string{"old.json", "used.json"} {
		if err := os.Chtimes(filepath.Join(dir, name), old, old); err != nil {
			t.Fatal(err)
		}
	}
	if got := s.Load("used"); got != config.ModeCost {
		t.Fatalf("Load(used) = %q", got)
	}

	again, _ := open(t, app)

	for id, want := range map[string]config.RoutingMode{"old": "", "used": config.ModeCost, "fresh": config.ModeQuality} {
		if got := again.Load(id); got != want {
			t.Errorf("after prune Load(%s) = %q, want %q", id, got, want)
		}
	}
}

func TestOpenRefusesASharedDir(t *testing.T) {
	app := t.TempDir()
	dir := filepath.Join(app, "modes", "home1")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(app, "home1", slog.New(slog.DiscardHandler)); err == nil {
		t.Fatal("Open accepted a dir others can read")
	}
}
