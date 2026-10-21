package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
)

// ProjectFile is a project's routing mode file, relative to a directory
// between a session's cwd and its repository root.
const ProjectFile = ".codex/router.json"

// ProjectMode returns the mode set by the project file nearest to cwd, and
// that file's path. The search runs from cwd up to the nearest directory
// holding .git, inclusive; outside a repository only cwd is searched. No file
// gives no mode, no path and no error. A file that does not parse, holds a
// key other than mode, or names an unknown mode is an error, with its path.
func ProjectMode(cwd string) (RoutingMode, string, error) {
	if cwd == "" {
		return "", "", nil
	}
	root := repoRoot(cwd)
	for dir := cwd; ; dir = filepath.Dir(dir) {
		path := filepath.Join(dir, ProjectFile)
		data, err := os.ReadFile(path)
		switch {
		case err == nil:
			mode, err := parseProject(data)
			if err != nil {
				return "", path, fmt.Errorf("%s: %w", path, err)
			}
			return mode, path, nil
		case !errors.Is(err, fs.ErrNotExist):
			return "", path, fmt.Errorf("read %s: %w", path, err)
		}
		if dir == root || filepath.Dir(dir) == dir {
			return "", "", nil
		}
	}
}

// repoRoot is the nearest directory at or above dir holding .git (a
// directory, or a worktree's file), else dir.
// ProjectModes is ProjectMode per thread, from each thread's cwd; nil when
// cwd is nil.
func ProjectModes(cwd func(threadID string) string) func(threadID string) (RoutingMode, error) {
	if cwd == nil {
		return nil
	}
	return func(threadID string) (RoutingMode, error) {
		m, _, err := ProjectMode(cwd(threadID))
		return m, err
	}
}

func repoRoot(dir string) string {
	for d := dir; ; d = filepath.Dir(d) {
		if _, err := os.Stat(filepath.Join(d, ".git")); err == nil {
			return d
		}
		if filepath.Dir(d) == d {
			return dir
		}
	}
}

func parseProject(data []byte) (RoutingMode, error) {
	var f struct {
		Mode RoutingMode `json:"mode"`
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&f); err != nil {
		return "", err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return "", errors.New("trailing data after JSON object")
	}
	switch {
	case f.Mode == "":
		return "", errors.New("mode: missing; the file holds only mode")
	case !slices.Contains(RoutingModes, f.Mode):
		return "", fmt.Errorf("mode: unknown mode %q, want cost, balanced or quality", f.Mode)
	}
	return f.Mode, nil
}
