package statedir

import (
	"os"
	"path/filepath"
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
