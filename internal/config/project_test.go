package config

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestProjectMode(t *testing.T) {
	put := func(t *testing.T, path, body string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cases := []struct {
		name     string
		files    map[string]string // relative to the temp root
		cwd      string
		want     RoutingMode
		wantFile string
		wantErr  string
	}{
		{name: "no file", files: map[string]string{"repo/.git/HEAD": ""}, cwd: "repo/sub"},
		{name: "in cwd", files: map[string]string{"repo/.git/HEAD": "", "repo/.codex/router.json": `{"mode":"cost"}`}, cwd: "repo", want: ModeCost, wantFile: "repo/.codex/router.json"},
		{name: "in the repo root above cwd", files: map[string]string{"repo/.git/HEAD": "", "repo/.codex/router.json": `{"mode":"quality"}`}, cwd: "repo/a/b", want: ModeQuality, wantFile: "repo/.codex/router.json"},
		{name: "the nearest file wins", files: map[string]string{"repo/.git/HEAD": "", "repo/.codex/router.json": `{"mode":"quality"}`, "repo/a/.codex/router.json": `{"mode":"cost"}`}, cwd: "repo/a/b", want: ModeCost, wantFile: "repo/a/.codex/router.json"},
		{name: "a worktree's .git file is a root", files: map[string]string{"wt/.git": "gitdir: x", ".codex/router.json": `{"mode":"cost"}`}, cwd: "wt"},
		{name: "stops at the repo root", files: map[string]string{"repo/.git/HEAD": "", ".codex/router.json": `{"mode":"cost"}`}, cwd: "repo/a"},
		{name: "outside a repo only cwd counts", files: map[string]string{".codex/router.json": `{"mode":"cost"}`}, cwd: "plain"},
		{name: "outside a repo in cwd", files: map[string]string{"plain/.codex/router.json": `{"mode":"balanced"}`}, cwd: "plain", want: ModeBalanced, wantFile: "plain/.codex/router.json"},
		{name: "another key", files: map[string]string{"plain/.codex/router.json": `{"mode":"cost","routes":{}}`}, cwd: "plain", wantFile: "plain/.codex/router.json", wantErr: `unknown field "routes"`},
		{name: "no mode", files: map[string]string{"plain/.codex/router.json": `{}`}, cwd: "plain", wantFile: "plain/.codex/router.json", wantErr: "mode: missing"},
		{name: "unknown mode", files: map[string]string{"plain/.codex/router.json": `{"mode":"thrifty"}`}, cwd: "plain", wantFile: "plain/.codex/router.json", wantErr: `unknown mode "thrifty"`},
		{name: "bad JSON", files: map[string]string{"plain/.codex/router.json": `{"mode":`}, cwd: "plain", wantFile: "plain/.codex/router.json", wantErr: "unexpected EOF"},
		{name: "trailing data", files: map[string]string{"plain/.codex/router.json": `{"mode":"cost"} {}`}, cwd: "plain", wantFile: "plain/.codex/router.json", wantErr: "trailing data"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			for rel, body := range tc.files {
				put(t, filepath.Join(root, rel), body)
			}
			cwd := filepath.Join(root, tc.cwd)
			if err := os.MkdirAll(cwd, 0o700); err != nil {
				t.Fatal(err)
			}
			mode, file, err := ProjectMode(cwd)
			wantFile := ""
			if tc.wantFile != "" {
				wantFile = filepath.Join(root, tc.wantFile)
			}
			if mode != tc.want || file != wantFile {
				t.Errorf("ProjectMode = %q, %q; want %q, %q", mode, file, tc.want, wantFile)
			}
			if tc.wantErr == "" && err != nil || tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)) {
				t.Errorf("err = %v, want %q", err, tc.wantErr)
			}
		})
	}
}

func TestProjectModeRefusesUnsafeFiles(t *testing.T) {
	cases := []struct {
		name    string
		make    func(t *testing.T, root, file string)
		wantErr string
	}{
		{"symlink to a device", func(t *testing.T, _, file string) { symlink(t, "/dev/zero", file) }, "a symlink"},
		{"symlink out of the repo", func(t *testing.T, root, file string) {
			outside := filepath.Join(root, "outside.json")
			if err := os.WriteFile(outside, []byte(`{"mode":"cost"}`), 0o600); err != nil {
				t.Fatal(err)
			}
			symlink(t, outside, file)
		}, "a symlink"},
		{"FIFO", func(t *testing.T, _, file string) {
			if err := syscall.Mkfifo(file, 0o600); err != nil {
				t.Fatal(err)
			}
		}, "a named pipe"},
		{"directory", func(t *testing.T, _, file string) {
			if err := os.Mkdir(file, 0o700); err != nil {
				t.Fatal(err)
			}
		}, "a directory"},
		{"oversize", func(t *testing.T, _, file string) {
			body := `{"mode":"cost"}` + strings.Repeat(" ", maxProjectBytes)
			if err := os.WriteFile(file, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
		}, "larger than 4096 bytes"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			repo := filepath.Join(root, "repo")
			if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Join(repo, ".codex"), 0o700); err != nil {
				t.Fatal(err)
			}
			file := filepath.Join(repo, ProjectFile)
			tc.make(t, root, file)

			done := make(chan struct{})
			var mode RoutingMode
			var err error
			go func() {
				defer close(done)
				mode, _, err = ProjectMode(repo)
			}()
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("ProjectMode blocked")
			}
			if mode != "" || err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("ProjectMode = %q, %v; want no mode and %q", mode, err, tc.wantErr)
			}
		})
	}
}

func symlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
}

func TestProjectModeWithoutCwd(t *testing.T) {
	if mode, file, err := ProjectMode(""); mode != "" || file != "" || err != nil {
		t.Errorf("ProjectMode(\"\") = %q, %q, %v; want nothing", mode, file, err)
	}
}
