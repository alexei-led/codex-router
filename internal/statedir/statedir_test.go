package statedir

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadPort(t *testing.T) {
	cases := []struct {
		name    string
		content string // "" writes no file
		want    int
	}{
		{"a port", "8417\n", 8417},
		{"no file", "", 0},
		{"not a number", "http", 0},
		{"out of range", "70000", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if tc.content != "" {
				if err := os.WriteFile(filepath.Join(dir, PortFile), []byte(tc.content), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			got, err := ReadPort(dir)
			if got != tc.want || (err == nil) != (tc.want != 0) {
				t.Fatalf("ReadPort = %d, %v; want %d", got, err, tc.want)
			}
		})
	}
}

func TestCheckHomeRefusesAnOpenParent(t *testing.T) {
	app := filepath.Join(t.TempDir(), appDirName)
	dir := HomeDir(app, "3fa9c0d1e2b4")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := CheckHome(app, dir); err != nil {
		t.Fatalf("private dirs: %v", err)
	}
	if err := os.Chmod(filepath.Dir(dir), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := CheckHome(app, dir); err == nil {
		t.Fatal("homes/ open to its group: want error")
	}
}

func TestWriteAtomicReplacesThe0600File(t *testing.T) {
	dir := t.TempDir()
	for _, data := range []string{"old", "new"} {
		if err := WriteAtomic(dir, "f", []byte(data)); err != nil {
			t.Fatal(err)
		}
	}
	p := filepath.Join(dir, "f")
	b, err := os.ReadFile(p)
	if err != nil || string(b) != "new" {
		t.Fatalf("content = %q, %v; want new", b, err)
	}
	if fi, _ := os.Stat(p); fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v, want 0600", fi.Mode().Perm())
	}
	if des, _ := os.ReadDir(dir); len(des) != 1 {
		t.Fatalf("dir holds %d files, want no temp file left", len(des))
	}
}

func TestDir(t *testing.T) {
	cases := []struct {
		name    string
		env     map[string]string
		want    string
		wantErr bool
	}{
		{"xdg state home", map[string]string{"XDG_STATE_HOME": "/s", "HOME": "/h"}, "/s/codex-router", false},
		{"relative xdg falls back to home", map[string]string{"XDG_STATE_HOME": "s", "HOME": "/h"}, "/h/.local/state/codex-router", false},
		{"home only", map[string]string{"HOME": "/h"}, "/h/.local/state/codex-router", false},
		{"relative home", map[string]string{"HOME": "h"}, "", true},
		{"nothing set", nil, "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Dir(func(k string) string { return tc.env[k] })
			if got != tc.want || (err != nil) != tc.wantErr {
				t.Errorf("Dir = %q, %v; want %q, error %v", got, err, tc.want, tc.wantErr)
			}
		})
	}
}

func TestCheckPrivate(t *testing.T) {
	cases := []struct {
		name    string
		prepare func(t *testing.T, path string)
		wantErr string
	}{
		{"private dir", func(t *testing.T, p string) { mkdirMode(t, p, 0o700) }, ""},
		{"group readable", func(t *testing.T, p string) { mkdirMode(t, p, 0o750) }, "group or others"},
		{"world writable", func(t *testing.T, p string) { mkdirMode(t, p, 0o702) }, "group or others"},
		{"regular file", func(t *testing.T, p string) {
			if err := os.WriteFile(p, nil, 0o600); err != nil {
				t.Fatal(err)
			}
		}, "not a directory"},
		{"symlink to a private dir", func(t *testing.T, p string) {
			target := p + "-target"
			mkdirMode(t, target, 0o700)
			if err := os.Symlink(target, p); err != nil {
				t.Fatal(err)
			}
		}, "not a directory"},
		{"missing", func(*testing.T, string) {}, "no such file"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "d")
			tc.prepare(t, path)
			err := CheckPrivate(path)
			if tc.wantErr == "" {
				if err != nil {
					t.Errorf("CheckPrivate = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("CheckPrivate = %v, want error containing %q", err, tc.wantErr)
			}
		})
	}
}

func TestCheckHomeRefusesOpenAppAndHomeDirs(t *testing.T) {
	for _, level := range []string{"app", "home"} {
		t.Run(level, func(t *testing.T) {
			app := filepath.Join(t.TempDir(), appDirName)
			dir := HomeDir(app, "3fa9c0d1e2b4")
			if err := os.MkdirAll(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			open := map[string]string{"app": app, "home": dir}[level]
			if err := os.Chmod(open, 0o705); err != nil {
				t.Fatal(err)
			}
			if err := CheckHome(app, dir); err == nil {
				t.Error("CheckHome = nil, want error")
			}
		})
	}
}

func TestWriteAtomicFailureLeavesNoTempFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "f", "child"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := WriteAtomic(dir, "f", []byte("x")); err == nil {
		t.Error("WriteAtomic over a non-empty directory = nil, want error")
	}
	if des, _ := os.ReadDir(dir); len(des) != 1 {
		t.Errorf("dir holds %d entries, want only the original", len(des))
	}
	if err := WriteAtomic(filepath.Join(dir, "missing"), "f", []byte("x")); err == nil {
		t.Error("WriteAtomic into a missing dir = nil, want error")
	}
}

func mkdirMode(t *testing.T, path string, mode os.FileMode) {
	t.Helper()
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}
