package home

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"
)

const (
	defaultDir  = ".codex"
	defaultName = "default"
	namePrefix  = defaultDir + "-"
	idHexLen    = 12
)

// Home is one Codex home. ID is the first 12 hex of sha256(Path).
type Home struct {
	ID   string `json:"id"`
	Path string `json:"path"` // canonical: symlinks resolved when the dir exists
	Name string `json:"name"`
}

// Resolve returns the home from $CODEX_HOME, else $HOME/.codex. A relative
// CODEX_HOME is an error: its meaning would depend on the caller's cwd, so
// two shells could give one home two IDs. The dir need not exist yet; then
// the cleaned path is used as is.
func Resolve(getenv func(string) string) (Home, error) {
	p, err := Given(getenv)
	if err != nil {
		return Home{}, err
	}
	canon, err := filepath.EvalSymlinks(p)
	switch {
	case err == nil:
	case errors.Is(err, fs.ErrNotExist):
		canon = p
	default:
		return Home{}, fmt.Errorf("codex home: %w", err)
	}
	sum := sha256.Sum256([]byte(canon))
	return Home{ID: hex.EncodeToString(sum[:])[:idHexLen], Path: canon, Name: name(p)}, nil
}

// Given returns the home path as the user set it, $CODEX_HOME else
// $HOME/.codex, cleaned with its symlinks kept. Name comes from it, so a
// process handed this path resolves the same home under the same Name.
func Given(getenv func(string) string) (string, error) {
	p := getenv("CODEX_HOME")
	if p == "" {
		h := getenv("HOME")
		if !filepath.IsAbs(h) {
			return "", errors.New("codex home: HOME is not an absolute path")
		}
		p = filepath.Join(h, defaultDir)
	} else if !filepath.IsAbs(p) {
		return "", fmt.Errorf("codex home: CODEX_HOME %q is not an absolute path", p)
	}
	return filepath.Clean(p), nil
}

// name derives the display name from the path the user set, not the
// symlink target: ~/.codex-work stays "work" even if it points into a
// dotfiles repo.
func name(p string) string {
	base := filepath.Base(p)
	if base == defaultDir {
		return defaultName
	}
	if x, ok := strings.CutPrefix(base, namePrefix); ok && x != "" {
		return x
	}
	return base
}
