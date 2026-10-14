package catalog_test

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/alexei-led/codex-router/internal/catalog"
	"github.com/alexei-led/codex-router/internal/config"
)

const fixture = "testdata/models_cache.json"

func loadFixture(t *testing.T) catalog.Catalog {
	t.Helper()
	c, err := catalog.LoadCache(fixture)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestParseFields(t *testing.T) {
	body, err := os.ReadFile(fixture)
	if err != nil {
		t.Fatal(err)
	}
	c, err := catalog.Parse(body)
	if err != nil {
		t.Fatal(err)
	}
	m, ok := c.Models["gpt-6.1-sol"]
	if !ok {
		t.Fatal("gpt-6.1-sol missing")
	}
	if m.Visibility != "list" || m.ContextWindow != 500000 || m.MaxContextWindow != 872000 ||
		m.EffectivePercent != 95 || m.CompHash != "3000" || !m.ResponsesLite || m.ToolMode != "code_mode_only" {
		t.Errorf("unexpected model: %+v", m)
	}
	if len(m.ReasoningLevels) != 6 || m.ReasoningLevels[0].Effort != config.EffortLow {
		t.Errorf("reasoning levels: %+v", m.ReasoningLevels)
	}
	if m.DefaultEffort != config.EffortLow {
		t.Errorf("default effort = %q, want low", m.DefaultEffort)
	}
}

func TestMissingToolModeIsNone(t *testing.T) {
	c := loadFixture(t)
	if got := c.Models["gpt-5.5"].ToolMode; got != catalog.ToolModeNone {
		t.Errorf("tool_mode = %q, want %q", got, catalog.ToolModeNone)
	}
}

func TestParseInvalid(t *testing.T) {
	if _, err := catalog.Parse([]byte("{")); err == nil {
		t.Fatal("want error")
	}
}

func TestCompatible(t *testing.T) {
	c := loadFixture(t)
	tests := []struct {
		a, b string
		want bool
	}{
		{"gpt-6.1-sol", "gpt-6-luna", true},
		{"gpt-6-luna", "gpt-6.1-sol", true},
		{"gpt-6.1-sol", "gpt-5.5", false},
		{"gpt-5.5", "gpt-5.5", true},
		{"gpt-6.1-sol", "unknown", false},
		{"unknown", "unknown", false},
	}
	for _, tt := range tests {
		if got := c.Compatible(tt.a, tt.b); got != tt.want {
			t.Errorf("Compatible(%q, %q) = %v, want %v", tt.a, tt.b, got, tt.want)
		}
	}
}

func TestGroupOf(t *testing.T) {
	c := loadFixture(t)
	g, ok := c.GroupOf("gpt-5.5")
	want := catalog.Group{CompHash: "2911", Lite: false, ToolMode: catalog.ToolModeNone}
	if !ok || g != want {
		t.Errorf("GroupOf = %+v, %v; want %+v", g, ok, want)
	}
	if _, ok := c.GroupOf("unknown"); ok {
		t.Error("unknown slug reported known")
	}
}

func TestWindow(t *testing.T) {
	c := loadFixture(t)
	tests := []struct {
		slug string
		want int
		ok   bool
	}{
		{"gpt-6.1-sol", 475000, true},
		{"gpt-6-luna", 258400, true},
		{"unknown", 0, false},
	}
	for _, tt := range tests {
		got, ok := c.Window(tt.slug)
		if got != tt.want || ok != tt.ok {
			t.Errorf("Window(%q) = %d, %v; want %d, %v", tt.slug, got, ok, tt.want, tt.ok)
		}
	}
}

func TestWindowUnknownWhenFieldsMissing(t *testing.T) {
	c, err := catalog.Parse([]byte(`{"models":[{"slug":"a"},{"slug":"b","context_window":1000,"effective_context_window_percent":0}]}`))
	if err != nil {
		t.Fatal(err)
	}
	for _, slug := range []string{"a", "b"} {
		if w, ok := c.Window(slug); ok {
			t.Errorf("Window(%q) = %d, want unknown", slug, w)
		}
	}
}

// The live /models body omits effective_context_window_percent; Codex then
// uses 95, and so must the guard.
func TestWindowDefaultsPercentLikeCodex(t *testing.T) {
	c, err := catalog.Parse([]byte(`{"models":[{"slug":"a","context_window":272000}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if w, ok := c.Window("a"); w != 258400 || !ok {
		t.Errorf("Window = %d, %v; want 258400, true", w, ok)
	}
}

func TestLoadCacheMissingFile(t *testing.T) {
	c, err := catalog.LoadCache(filepath.Join(t.TempDir(), "absent.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Models) != 0 {
		t.Errorf("models = %d, want 0", len(c.Models))
	}
	if _, ok := c.Window("gpt-6.1-sol"); ok {
		t.Error("empty catalog knows a model")
	}
}

func TestLoadCacheErrors(t *testing.T) {
	bad := filepath.Join(t.TempDir(), "bad.json")
	if err := os.WriteFile(bad, []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := catalog.LoadCache(bad); err == nil {
		t.Error("want error for corrupt cache")
	}
}

func TestStoreConcurrent(t *testing.T) {
	var s catalog.Store
	if len(s.Current().Models) != 0 {
		t.Fatal("zero store not empty")
	}
	full := loadFixture(t)
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(2)
		go func() {
			defer wg.Done()
			for range 200 {
				s.Set(full)
			}
		}()
		go func() {
			defer wg.Done()
			for range 200 {
				_ = s.Current().Compatible("gpt-6.1-sol", "gpt-6-luna")
			}
		}()
	}
	wg.Wait()
	if !s.Current().Compatible("gpt-6.1-sol", "gpt-6-luna") {
		t.Error("catalog lost after concurrent Set")
	}
}

func TestCacheWatchReloadsAChangedCacheAtMostEveryCheckPeriod(t *testing.T) {
	path := filepath.Join(t.TempDir(), catalog.CacheFile)
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	store := &catalog.Store{}
	w := catalog.NewCacheWatch(path, store, func() time.Time { return now })
	write := func(slug string, mod time.Time) {
		t.Helper()
		if err := os.WriteFile(path, []byte(`{"models":[{"slug":"`+slug+`"}]}`), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, mod, mod); err != nil {
			t.Fatal(err)
		}
	}
	check := func(wantReloaded bool, wantErr bool, wantSlug string) {
		t.Helper()
		reloaded, err := w.Check()
		if reloaded != wantReloaded || (err != nil) != wantErr {
			t.Fatalf("Check = %v, %v; want reloaded %v, error %v", reloaded, err, wantReloaded, wantErr)
		}
		if _, ok := store.Current().Models[wantSlug]; wantSlug != "" && !ok {
			t.Fatalf("catalog = %v, want %s", store.Current().Models, wantSlug)
		}
	}

	check(false, false, "") // no file yet
	write("gpt-a", now)
	check(false, false, "") // looked a moment ago
	now = now.Add(catalog.CacheCheckEvery)
	check(true, false, "gpt-a")

	now = now.Add(catalog.CacheCheckEvery)
	check(false, false, "gpt-a") // unchanged

	write("gpt-b", now) // same size, newer mtime
	now = now.Add(catalog.CacheCheckEvery)
	check(true, false, "gpt-b")

	if err := os.WriteFile(path, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	now = now.Add(catalog.CacheCheckEvery)
	check(false, true, "gpt-b") // a broken file keeps the last catalog
	now = now.Add(catalog.CacheCheckEvery)
	check(false, false, "gpt-b") // and is not read again until it changes
}
