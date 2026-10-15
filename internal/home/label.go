package home

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
)

// LabelSep separates the parts of a label.
const LabelSep = " · "

const (
	gitEntry     = ".git"
	shortSHALen  = 7
	suffixHexLen = 4
)

// RepoBranch finds the git repo containing cwd by reading .git files, never
// by running git. A linked worktree reports its main repo's name. Detached
// HEAD gives the short commit sha. ok is false when cwd is in no repo or the
// repo's metadata cannot be read.
func RepoBranch(cwd string) (repo, branch string, ok bool) {
	top, ok := findGit(filepath.Clean(cwd))
	if !ok {
		return "", "", false
	}
	gitDir := filepath.Join(top, gitEntry)
	repo = filepath.Base(top)
	fi, err := os.Stat(gitDir)
	if err != nil {
		return "", "", false
	}
	if !fi.IsDir() {
		// Linked worktree or submodule: ".git" is a file naming the real git dir.
		dir, ok := readGitFile(gitDir)
		if !ok {
			return "", "", false
		}
		gitDir = dir
		if common, ok := readRel(gitDir, "commondir"); ok {
			repo = repoName(common)
		}
	}
	branch, ok = headBranch(gitDir)
	if !ok {
		return "", "", false
	}
	return repo, branch, true
}

// Label is the display label "home · repo · branch", or "home · dir" when
// cwd is not in a readable repo. It is not an identity (M1).
func Label(homeName, cwd string) string {
	if cwd == "" {
		return homeName
	}
	if repo, branch, ok := RepoBranch(cwd); ok {
		return homeName + LabelSep + repo + LabelSep + branch
	}
	return homeName + LabelSep + filepath.Base(cwd)
}

// Disambiguate returns labels with " · <4-hex>" appended to each label that
// more than one entry shares. ids[i] belongs to labels[i]. The suffix is
// hashed from the id, so it works for any id format, including UUIDv7 thread
// ids whose leading hex is a timestamp. Two colliding ids can still share a
// suffix (1 in 65536); the labels stay display only.
func Disambiguate(labels, ids []string) []string {
	if len(labels) != len(ids) {
		panic("home.Disambiguate: labels and ids differ in length")
	}
	count := make(map[string]int, len(labels))
	for _, l := range labels {
		count[l]++
	}
	out := make([]string, len(labels))
	for i, l := range labels {
		if count[l] > 1 {
			sum := sha256.Sum256([]byte(ids[i]))
			l += LabelSep + hex.EncodeToString(sum[:])[:suffixHexLen]
		}
		out[i] = l
	}
	return out
}

// findGit walks up from dir to the nearest directory holding a .git entry.
func findGit(dir string) (string, bool) {
	for {
		if _, err := os.Lstat(filepath.Join(dir, gitEntry)); err == nil {
			return dir, true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false
		}
		dir = parent
	}
}

// readGitFile reads "gitdir: <path>" from a .git file; a relative path is
// relative to the file's directory.
func readGitFile(path string) (string, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	dir, ok := strings.CutPrefix(strings.TrimSpace(string(b)), "gitdir:")
	if !ok {
		return "", false
	}
	return abs(filepath.Dir(path), strings.TrimSpace(dir)), true
}

// readRel reads a file in gitDir holding a path relative to gitDir.
func readRel(gitDir, name string) (string, bool) {
	b, err := os.ReadFile(filepath.Join(gitDir, name))
	if err != nil {
		return "", false
	}
	p := strings.TrimSpace(string(b))
	if p == "" {
		return "", false
	}
	return abs(gitDir, p), true
}

// repoName names a repo by its common git dir: "<repo>/.git" gives "repo",
// a bare "<repo>.git" gives "repo".
func repoName(common string) string {
	if filepath.Base(common) == gitEntry {
		return filepath.Base(filepath.Dir(common))
	}
	return strings.TrimSuffix(filepath.Base(common), gitEntry)
}

// headBranch reads HEAD: "ref: refs/heads/x" gives "x", a detached sha
// gives its short form.
func headBranch(gitDir string) (string, bool) {
	b, err := os.ReadFile(filepath.Join(gitDir, "HEAD"))
	if err != nil {
		return "", false
	}
	head := strings.TrimSpace(string(b))
	if ref, ok := strings.CutPrefix(head, "ref:"); ok {
		ref = strings.TrimSpace(ref)
		return strings.TrimPrefix(ref, "refs/heads/"), ref != ""
	}
	if len(head) < shortSHALen {
		return "", false
	}
	return head[:shortSHALen], true
}

func abs(base, p string) string {
	if filepath.IsAbs(p) {
		return filepath.Clean(p)
	}
	return filepath.Join(base, p)
}
