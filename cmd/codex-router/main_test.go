package main

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testEnv(t *testing.T) []string {
	t.Helper()
	home := t.TempDir()
	return []string{"HOME=" + home, "TMPDIR=" + home, "CODEX_HOME=" + home, envConfig + "=" + filepath.Join(home, "none.json")}
}

func TestRunPrintsAndRejects(t *testing.T) {
	cases := []struct {
		name       string
		args       []string
		wantCode   int
		wantStdout string
		wantStderr string
	}{
		{"version subcommand", []string{"version"}, exitOK, "dev\n", ""},
		{"version flag", []string{"--version"}, exitOK, "dev\n", ""},
		{"help", []string{"--help"}, exitOK, "Exit codes:", ""},
		{"config without path", []string{"--config"}, exitUsage, "", "--config needs a path"},
		{"unknown proxy flag", []string{"proxy", "--bogus"}, exitUsage, "", "flag provided but not defined"},
		{"stray tui argument", []string{"tui", "extra"}, exitUsage, "", `unexpected argument "extra"`},
		{"tui without a running session", []string{"tui"}, exitError, "", "no running codex-router session"},
		{"hook without a router", []string{"hook"}, exitOK, "{}\n", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := run(context.Background(), tc.args, testEnv(t), strings.NewReader(""), &stdout, &stderr)
			if code != tc.wantCode {
				t.Errorf("exit code = %d, want %d (stderr %q)", code, tc.wantCode, stderr.String())
			}
			if !strings.Contains(stdout.String(), tc.wantStdout) {
				t.Errorf("stdout = %q, want it to contain %q", stdout.String(), tc.wantStdout)
			}
			if !strings.Contains(stderr.String(), tc.wantStderr) {
				t.Errorf("stderr = %q, want it to contain %q", stderr.String(), tc.wantStderr)
			}
		})
	}
}

func TestProxyRejectsBadConfig(t *testing.T) {
	bad := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(bad, []byte(`{"bogus": 1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	code := run(context.Background(), []string{"--config", bad, "proxy", "--port", "0"}, testEnv(t), nil, io.Discard, &stderr)
	if code != exitError || !strings.Contains(stderr.String(), "bogus") {
		t.Errorf("exit code = %d, stderr %q; want 1 naming the bad key", code, stderr.String())
	}
}

func TestProxyServesHealthAndPrintsProviderFlags(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out, stdout := io.Pipe()
	done := make(chan int, 1)
	go func() {
		done <- run(ctx, []string{"proxy", "--port", "0"}, testEnv(t), nil, stdout, io.Discard)
		_ = stdout.Close()
	}()

	lines := bufio.NewScanner(out)
	var printed []string
	for len(printed) < 4 && lines.Scan() {
		printed = append(printed, lines.Text())
	}
	go func() { _, _ = io.Copy(io.Discard, out) }()
	if len(printed) < 4 {
		t.Fatalf("printed %q, want a listen line, two -c lines and the no-token warning", printed)
	}
	addr := strings.TrimPrefix(strings.Split(printed[0], ";")[0], "proxy listening on ")
	if !strings.Contains(printed[1], "-c 'model_provider=router'") ||
		!strings.Contains(printed[2], `base_url="http://`+addr+`/v1"`) {
		t.Errorf("provider flags = %q", printed[1:])
	}
	if !strings.Contains(printed[3], "no token") {
		t.Errorf("warning = %q, want it to say the proxy checks no token", printed[3])
	}

	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get("http://" + addr + "/healthz")
	if err != nil {
		t.Fatalf("healthz: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("healthz status = %d", resp.StatusCode)
	}

	cancel()
	select {
	case code := <-done:
		if code != exitOK {
			t.Errorf("exit code = %d, want 0", code)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("proxy did not stop")
	}
}
