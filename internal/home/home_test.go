package home

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestResolveName(t *testing.T) {
	cases := []struct {
		name     string
		env      map[string]string
		wantPath string
		wantName string
	}{
		{"unset uses HOME/.codex", map[string]string{"HOME": "/nonexistent/u"}, "/nonexistent/u/.codex", "default"},
		{"empty CODEX_HOME is unset", map[string]string{"HOME": "/nonexistent/u", "CODEX_HOME": ""}, "/nonexistent/u/.codex", "default"},
		{".codex", map[string]string{"CODEX_HOME": "/nonexistent/u/.codex"}, "/nonexistent/u/.codex", "default"},
		{".codex-work", map[string]string{"CODEX_HOME": "/nonexistent/u/.codex-work"}, "/nonexistent/u/.codex-work", "work"},
		{"codex-x keeps basename", map[string]string{"CODEX_HOME": "/nonexistent/u/codex-x"}, "/nonexistent/u/codex-x", "codex-x"},
		{".codex- keeps basename", map[string]string{"CODEX_HOME": "/nonexistent/u/.codex-"}, "/nonexistent/u/.codex-", ".codex-"},
		{"trailing slash", map[string]string{"CODEX_HOME": "/nonexistent/u/.codex-work/"}, "/nonexistent/u/.codex-work", "work"},
		{"unclean path", map[string]string{"CODEX_HOME": "/nonexistent/u/../u/./.codex-work"}, "/nonexistent/u/.codex-work", "work"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, err := Resolve(env(tc.env))
			if err != nil {
				t.Fatalf("Resolve: %v", err)
			}
			if h.Path != tc.wantPath || h.Name != tc.wantName {
				t.Errorf("Resolve = {Path %q, Name %q}, want {%q, %q}", h.Path, h.Name, tc.wantPath, tc.wantName)
			}
			sum := sha256.Sum256([]byte(tc.wantPath))
			if want := hex.EncodeToString(sum[:])[:12]; h.ID != want {
				t.Errorf("ID = %q, want %q", h.ID, want)
			}
		})
	}
}

func TestResolveRejects(t *testing.T) {
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		env  map[string]string
	}{
		{"relative CODEX_HOME", map[string]string{"HOME": "/u", "CODEX_HOME": ".codex-work"}},
		{"relative HOME", map[string]string{"HOME": "u"}},
		{"no HOME", map[string]string{}},
		{"path under a file", map[string]string{"CODEX_HOME": filepath.Join(file, ".codex")}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if h, err := Resolve(env(tc.env)); err == nil {
				t.Errorf("Resolve = %+v, want error", h)
			}
		})
	}
}

func TestResolveSymlinkedHomeHasTheSameID(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "dotfiles", "codex-work")
	if err := os.MkdirAll(real, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, ".codex-work")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	viaReal, err := Resolve(env(map[string]string{"CODEX_HOME": real}))
	if err != nil {
		t.Fatal(err)
	}
	viaLink, err := Resolve(env(map[string]string{"CODEX_HOME": link}))
	if err != nil {
		t.Fatal(err)
	}
	if viaLink.ID != viaReal.ID || viaLink.Path != viaReal.Path {
		t.Errorf("via link = %+v, via target = %+v: want same ID and Path", viaLink, viaReal)
	}
	if viaLink.Name != "work" {
		t.Errorf("Name via link = %q, want work (from the path the user set)", viaLink.Name)
	}
}

func TestResolveDistinctHomesHaveDistinctIDs(t *testing.T) {
	a, err := Resolve(env(map[string]string{"CODEX_HOME": "/nonexistent/u/.codex"}))
	if err != nil {
		t.Fatal(err)
	}
	b, err := Resolve(env(map[string]string{"CODEX_HOME": "/nonexistent/u/.codex-work"}))
	if err != nil {
		t.Fatal(err)
	}
	if a.ID == b.ID {
		t.Errorf("both homes have ID %q", a.ID)
	}
}
