package home

import (
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const sha = "0123456789abcdef0123456789abcdef01234567"

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// repo lays out a minimal non-bare repo at dir/name with HEAD set to head.
func repo(t *testing.T, dir, name, head string) string {
	t.Helper()
	top := filepath.Join(dir, name)
	write(t, filepath.Join(top, ".git", "HEAD"), head+"\n")
	return top
}

// worktree lays out a linked worktree of main at dir/name the way
// `git worktree add` does: an absolute gitdir and a relative commondir.
func worktree(t *testing.T, main, dir, name, head string) string {
	t.Helper()
	gitDir := filepath.Join(main, ".git", "worktrees", name)
	write(t, filepath.Join(gitDir, "HEAD"), head+"\n")
	write(t, filepath.Join(gitDir, "commondir"), "../..\n")
	top := filepath.Join(dir, name)
	write(t, filepath.Join(top, ".git"), "gitdir: "+gitDir+"\n")
	return top
}

func TestRepoBranch(t *testing.T) {
	dir := t.TempDir()
	plain := repo(t, dir, "codex-router", "ref: refs/heads/main")
	detached := repo(t, dir, "detached", sha)
	slashBranch := repo(t, dir, "slash", "ref: refs/heads/feat/multi-session")
	wt := worktree(t, plain, dir, "codex-router-wt", "ref: refs/heads/feat/x")
	wtDetached := worktree(t, plain, dir, "wt-detached", sha)

	relWt := filepath.Join(dir, "rel-wt")
	write(t, filepath.Join(relWt, ".git"), "gitdir: ../codex-router/.git/worktrees/rel\n")
	write(t, filepath.Join(plain, ".git", "worktrees", "rel", "HEAD"), "ref: refs/heads/rel\n")
	write(t, filepath.Join(plain, ".git", "worktrees", "rel", "commondir"), "../..\n")

	bare := filepath.Join(dir, "bare.git")
	write(t, filepath.Join(bare, "worktrees", "b", "HEAD"), "ref: refs/heads/b\n")
	write(t, filepath.Join(bare, "worktrees", "b", "commondir"), "../..\n")
	bareWt := filepath.Join(dir, "bare-wt")
	write(t, filepath.Join(bareWt, ".git"), "gitdir: "+filepath.Join(bare, "worktrees", "b")+"\n")

	sub := filepath.Join(plain, "vendor", "sub")
	write(t, filepath.Join(plain, ".git", "modules", "sub", "HEAD"), "ref: refs/heads/sub-main\n")
	write(t, filepath.Join(sub, ".git"), "gitdir: ../../.git/modules/sub\n")

	cases := []struct {
		name       string
		cwd        string
		wantRepo   string
		wantBranch string
	}{
		{"plain repo", plain, "codex-router", "main"},
		{"nested subdir", filepath.Join(plain, "internal", "home"), "codex-router", "main"},
		{"unclean cwd", plain + "/internal/../", "codex-router", "main"},
		{"branch with slash", slashBranch, "slash", "feat/multi-session"},
		{"detached HEAD", detached, "detached", sha[:7]},
		{"linked worktree", wt, "codex-router", "feat/x"},
		{"linked worktree subdir", filepath.Join(wt, "cmd"), "codex-router", "feat/x"},
		{"linked worktree detached", wtDetached, "codex-router", sha[:7]},
		{"relative gitdir", relWt, "codex-router", "rel"},
		{"worktree of bare repo", bareWt, "bare", "b"},
		{"submodule", sub, "sub", "sub-main"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := os.MkdirAll(tc.cwd, 0o700); err != nil {
				t.Fatal(err)
			}
			r, b, ok := RepoBranch(tc.cwd)
			if !ok || r != tc.wantRepo || b != tc.wantBranch {
				t.Errorf("RepoBranch = (%q, %q, %v), want (%q, %q, true)", r, b, ok, tc.wantRepo, tc.wantBranch)
			}
		})
	}
}

func TestRepoBranchNotARepo(t *testing.T) {
	dir := t.TempDir()
	noRepo := filepath.Join(dir, "scratch")
	if err := os.MkdirAll(noRepo, 0o700); err != nil {
		t.Fatal(err)
	}
	badGitFile := filepath.Join(dir, "bad-file")
	write(t, filepath.Join(badGitFile, ".git"), "not a gitdir line\n")
	danglingGitFile := filepath.Join(dir, "dangling")
	write(t, filepath.Join(danglingGitFile, ".git"), "gitdir: "+filepath.Join(dir, "gone")+"\n")
	noHead := filepath.Join(dir, "no-head")
	if err := os.MkdirAll(filepath.Join(noHead, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	shortHead := repo(t, dir, "short-head", "abc")
	emptyRef := repo(t, dir, "empty-ref", "ref:")

	cases := []struct {
		name string
		cwd  string
	}{
		{"no repo", noRepo},
		{".git file without gitdir", badGitFile},
		{".git file pointing nowhere", danglingGitFile},
		{"no HEAD", noHead},
		{"HEAD too short", shortHead},
		{"empty ref", emptyRef},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if r, b, ok := RepoBranch(tc.cwd); ok {
				t.Errorf("RepoBranch = (%q, %q, true), want ok false", r, b)
			}
		})
	}
}

func TestRepoBranchUnreadableGit(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads files regardless of mode")
	}
	dir := t.TempDir()
	unreadableDir := repo(t, dir, "locked-dir", "ref: refs/heads/main")
	gitDir := filepath.Join(unreadableDir, ".git")
	if err := os.Chmod(gitDir, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(gitDir, 0o700) })

	unreadableFile := filepath.Join(dir, "locked-file")
	write(t, filepath.Join(unreadableFile, ".git"), "gitdir: "+gitDir+"\n")
	if err := os.Chmod(filepath.Join(unreadableFile, ".git"), 0); err != nil {
		t.Fatal(err)
	}

	for _, cwd := range []string{unreadableDir, unreadableFile} {
		t.Run(filepath.Base(cwd), func(t *testing.T) {
			if r, b, ok := RepoBranch(cwd); ok {
				t.Errorf("RepoBranch = (%q, %q, true), want ok false", r, b)
			}
			if got, want := Label("work", cwd), "work · "+filepath.Base(cwd); got != want {
				t.Errorf("Label = %q, want %q", got, want)
			}
		})
	}
}

func TestLabel(t *testing.T) {
	dir := t.TempDir()
	plain := repo(t, dir, "codex-router", "ref: refs/heads/main")
	wt := worktree(t, plain, dir, "codex-router-wt", "ref: refs/heads/feat/x")
	noRepo := filepath.Join(dir, "scratch")
	if err := os.MkdirAll(noRepo, 0o700); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name string
		home string
		cwd  string
		want string
	}{
		{"repo", "default", plain, "default · codex-router · main"},
		{"worktree", "work", wt, "work · codex-router · feat/x"},
		{"no repo", "work", noRepo, "work · scratch"},
		{"missing dir", "work", filepath.Join(dir, "gone"), "work · gone"},
		{"empty cwd", "work", "", "work"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Label(tc.home, tc.cwd); got != tc.want {
				t.Errorf("Label(%q, %q) = %q, want %q", tc.home, tc.cwd, got, tc.want)
			}
		})
	}
}

func TestDisambiguate(t *testing.T) {
	labels := []string{"work · app · main", "work · app · main", "default · app · main", "work · app · main"}
	ids := []string{"h1/t1", "h1/t2", "h2/t1", "h1/t3"}

	got := Disambiguate(labels, ids)

	if got[2] != labels[2] {
		t.Errorf("unique label changed: %q", got[2])
	}
	seen := map[string]bool{}
	for _, i := range []int{0, 1, 3} {
		suffix, ok := strings.CutPrefix(got[i], labels[i]+" · ")
		if _, err := hex.DecodeString(suffix); !ok || len(suffix) != 4 || err != nil {
			t.Errorf("got[%d] = %q, want %q + 4-hex", i, got[i], labels[i]+" · ")
		}
		if seen[got[i]] {
			t.Errorf("got[%d] = %q still collides", i, got[i])
		}
		seen[got[i]] = true
	}
	if labels[0] != "work · app · main" {
		t.Errorf("input mutated: %q", labels[0])
	}
	if again := Disambiguate(labels, ids); again[0] != got[0] {
		t.Errorf("suffix not stable: %q then %q", got[0], again[0])
	}
}

func TestDisambiguateNoCollisions(t *testing.T) {
	labels := []string{"a", "b"}
	got := Disambiguate(labels, []string{"1", "2"})
	if got[0] != "a" || got[1] != "b" {
		t.Errorf("Disambiguate = %q, want unchanged", got)
	}
	if got := Disambiguate(nil, nil); len(got) != 0 {
		t.Errorf("Disambiguate(nil) = %q, want empty", got)
	}
}

func TestDisambiguateLengthMismatchPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("want panic on length mismatch")
		}
	}()
	Disambiguate([]string{"a"}, nil)
}
