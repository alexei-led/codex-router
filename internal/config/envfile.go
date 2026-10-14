package config

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Env file errors. Their text never holds a value from the file.
var (
	ErrEnvFileMode = errors.New("readable by group or others; chmod 600 it")
	ErrKeyNotInEnv = errors.New("key not in file")
)

// ExpandHome turns a leading "~/" into home. Other paths are returned as is.
func ExpandHome(path, home string) string {
	if rest, ok := strings.CutPrefix(path, "~/"); ok {
		return filepath.Join(home, rest)
	}
	return path
}

// ReadEnvKey returns the value of name in the dotenv file at path: lines
// KEY=value or export KEY=value, values optionally in single or double
// quotes (no escapes), # comment lines, and a " #" comment after an unquoted
// value. The last assignment wins; an empty value counts as missing. Nothing
// is exported to the process env. A file that group or others can read is
// refused, as a shared key file is a leaked key.
func ReadEnvKey(path, name string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return "", err
	}
	if info.Mode().Perm()&0o077 != 0 {
		return "", fmt.Errorf("%s: %w", path, ErrEnvFileMode)
	}
	var value string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		if k, v, ok := parseEnvLine(sc.Text()); ok && k == name {
			value = v
		}
	}
	if err := sc.Err(); err != nil {
		return "", fmt.Errorf("%s: %w", path, err)
	}
	if value == "" {
		return "", fmt.Errorf("%s: %s: %w", path, name, ErrKeyNotInEnv)
	}
	return value, nil
}

func parseEnvLine(line string) (key, value string, ok bool) {
	line = strings.TrimSpace(strings.TrimSuffix(line, "\r"))
	if line == "" || strings.HasPrefix(line, "#") {
		return "", "", false
	}
	if rest, found := strings.CutPrefix(line, "export"); found && rest != "" && (rest[0] == ' ' || rest[0] == '\t') {
		line = strings.TrimSpace(rest)
	}
	key, value, ok = strings.Cut(line, "=")
	if !ok {
		return "", "", false
	}
	key, value = strings.TrimSpace(key), strings.TrimSpace(value)
	if len(value) >= 2 && (value[0] == '"' || value[0] == '\'') {
		if end := strings.IndexByte(value[1:], value[0]); end >= 0 {
			return key, value[1 : end+1], true
		}
	}
	if i := strings.Index(value, " #"); i >= 0 {
		value = strings.TrimSpace(value[:i])
	}
	return key, value, true
}
