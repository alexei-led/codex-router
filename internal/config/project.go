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
	"syscall"
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
		data, err := readProjectFile(path)
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

// maxProjectBytes caps a project file read: the file holds one short key.
const maxProjectBytes = 4 << 10

// readProjectFile reads a project file that a cloned repository controls. It
// must be a regular file, not a symlink, and at most maxProjectBytes: a link
// to /dev/zero or a FIFO would otherwise exhaust memory or block the read
// forever. O_NONBLOCK keeps the open from waiting on a FIFO swapped in after
// the Lstat; the opened file is checked again.
func readProjectFile(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, notRegular(info.Mode())
	}
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	if info, err = f.Stat(); err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, notRegular(info.Mode())
	}
	data, err := io.ReadAll(io.LimitReader(f, maxProjectBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxProjectBytes {
		return nil, fmt.Errorf("larger than %d bytes", maxProjectBytes)
	}
	return data, nil
}

func notRegular(m fs.FileMode) error {
	kind := "not a regular file"
	switch {
	case m&fs.ModeSymlink != 0:
		kind = "a symlink"
	case m&fs.ModeNamedPipe != 0:
		kind = "a named pipe"
	case m&fs.ModeDevice != 0:
		kind = "a device"
	case m.IsDir():
		kind = "a directory"
	}
	return fmt.Errorf("%s; the project file must be a regular file", kind)
}

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

// repoRoot is the nearest directory at or above dir holding .git (a
// directory, or a worktree's file), else dir.
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
