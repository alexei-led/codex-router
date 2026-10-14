package config

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeEnvFile(t *testing.T, body string, mode os.FileMode) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(path, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestReadEnvKey(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{"plain", "API_KEY=abc\n", "abc"},
		{"export", "export API_KEY=abc\n", "abc"},
		{"export with tab", "export\tAPI_KEY=abc", "abc"},
		{"double quotes", `API_KEY="a b#c"` + "\n", "a b#c"},
		{"single quotes", "API_KEY='a=b'\n", "a=b"},
		{"spaces around", "  API_KEY = abc  \n", "abc"},
		{"crlf", "OTHER=x\r\nAPI_KEY=abc\r\n", "abc"},
		{"comment lines", "# API_KEY=old\nAPI_KEY=abc\n# trailing\n", "abc"},
		{"inline comment", "API_KEY=abc # note\n", "abc"},
		{"other keys ignored", "API_KEY_2=no\nXAPI_KEY=no\nAPI_KEY=abc\nZ=1\n", "abc"},
		{"last wins", "API_KEY=one\nAPI_KEY=two\n", "two"},
		{"no trailing newline", "API_KEY=abc", "abc"},
		{"exported prefix in key is not export", "exportAPI_KEY=no\nAPI_KEY=abc\n", "abc"},
		{"unterminated quote kept", `API_KEY="abc` + "\n", `"abc`},
		{"value with equals", "API_KEY=a=b\n", "a=b"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ReadEnvKey(writeEnvFile(t, tc.body, 0o600), "API_KEY")
			if err != nil || got != tc.want {
				t.Errorf("ReadEnvKey = %q, %v; want %q", got, err, tc.want)
			}
		})
	}
}

func TestReadEnvKeyMissing(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"absent", "OTHER=x\n"},
		{"empty value", "API_KEY=\n"},
		{"empty quotes", "API_KEY=\"\"\n"},
		{"commented out", "# API_KEY=abc\n"},
		{"no equals", "API_KEY\n"},
		{"empty file", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ReadEnvKey(writeEnvFile(t, tc.body, 0o600), "API_KEY")
			if !errors.Is(err, ErrKeyNotInEnv) || got != "" {
				t.Errorf("ReadEnvKey = %q, %v; want ErrKeyNotInEnv", got, err)
			}
		})
	}
}

func TestReadEnvKeyRefusesSharedFile(t *testing.T) {
	for _, mode := range []os.FileMode{0o640, 0o604, 0o644, 0o660} {
		path := writeEnvFile(t, "API_KEY=secret-value\n", mode)
		got, err := ReadEnvKey(path, "API_KEY")
		if !errors.Is(err, ErrEnvFileMode) || got != "" {
			t.Errorf("mode %o: ReadEnvKey = %q, %v; want ErrEnvFileMode", mode, got, err)
		}
		if err != nil && (!strings.Contains(err.Error(), path) || !strings.Contains(err.Error(), "chmod 600")) {
			t.Errorf("mode %o: error %q should name the path and chmod 600", mode, err)
		}
		if err != nil && strings.Contains(err.Error(), "secret-value") {
			t.Errorf("mode %o: error leaks the value", mode)
		}
	}
}

func TestReadEnvKeyMissingFile(t *testing.T) {
	_, err := ReadEnvKey(filepath.Join(t.TempDir(), "nope"), "API_KEY")
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("err = %v, want not exist", err)
	}
}

func TestExpandHome(t *testing.T) {
	cases := []struct{ in, want string }{
		{"~/x/.env", "/h/x/.env"},
		{"/abs/.env", "/abs/.env"},
		{"~user/.env", "~user/.env"},
		{"", ""},
	}
	for _, tc := range cases {
		if got := ExpandHome(tc.in, "/h"); got != tc.want {
			t.Errorf("ExpandHome(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
