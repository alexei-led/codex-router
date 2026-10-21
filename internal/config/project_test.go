package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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

func TestProjectModeWithoutCwd(t *testing.T) {
	if mode, file, err := ProjectMode(""); mode != "" || file != "" || err != nil {
		t.Errorf("ProjectMode(\"\") = %q, %q, %v; want nothing", mode, file, err)
	}
}
