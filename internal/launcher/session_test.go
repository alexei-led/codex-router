package launcher

import (
	"bufio"
	"context"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/alexei-led/codex-router/internal/config"
	"github.com/alexei-led/codex-router/internal/route"
)

func TestWaitCodexForwardsTermAndHupAndDropsIntAndQuit(t *testing.T) {
	cases := []struct {
		name string
		end  func(sigs chan<- os.Signal, cancel context.CancelFunc)
	}{
		{"SIGTERM is forwarded", func(sigs chan<- os.Signal, _ context.CancelFunc) { sigs <- syscall.SIGTERM }},
		{"context end sends SIGTERM", func(_ chan<- os.Signal, cancel context.CancelFunc) { cancel() }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			exe, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			r, w, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = r.Close() }()
			cmd := exec.Command(exe, fakeSignalsArg)
			cmd.Env = append(os.Environ(), envFake+"=1")
			cmd.Stdout = w
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			_ = w.Close()
			lines := make(chan string)
			go func() {
				defer close(lines)
				sc := bufio.NewScanner(r)
				for sc.Scan() {
					lines <- sc.Text()
				}
			}()
			var got []string
			expect := func(want string) {
				t.Helper()
				select {
				case line := <-lines:
					got = append(got, line)
					if line != want {
						t.Fatalf("child printed %q, want %q (so far %q)", line, want, got)
					}
				case <-time.After(5 * time.Second):
					t.Fatalf("child never printed %q (so far %q)", want, got)
				}
			}

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			sigs := make(chan os.Signal)
			s := &session{log: slog.New(slog.DiscardHandler), codex: cmd}
			code := make(chan int, 1)
			go func() { code <- s.waitCodex(ctx, sigs) }()

			expect("ready")
			sigs <- syscall.SIGINT
			sigs <- syscall.SIGQUIT
			sigs <- syscall.SIGHUP
			expect("hangup")
			tc.end(sigs, cancel)
			expect("terminated")

			select {
			case c := <-code:
				if c != exitOnTerm {
					t.Errorf("exit code = %d, want %d", c, exitOnTerm)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("waitCodex did not return")
			}
			for line := range lines {
				t.Errorf("child also printed %q", line)
			}
		})
	}
}

func TestWaitCodexReportsSignalDeath(t *testing.T) {
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	s := &session{log: slog.New(slog.DiscardHandler), codex: cmd}
	_ = cmd.Process.Signal(syscall.SIGKILL)
	if code := s.waitCodex(context.Background(), nil); code != 128+int(syscall.SIGKILL) {
		t.Errorf("exit code = %d, want %d", code, 128+int(syscall.SIGKILL))
	}
}

func TestNewClassifier(t *testing.T) {
	withAPI := func(api string) config.Config {
		cfg := config.Default()
		cc := cfg.Classifiers[cfg.Classifier]
		cc.API = api
		cfg.Classifiers[cfg.Classifier] = cc
		return cfg
	}
	unknown := config.Default()
	unknown.Classifier = "missing"
	dir := t.TempDir()
	keyFile := func(name, body string, mode os.FileMode) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(body), mode); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, mode); err != nil {
			t.Fatal(err)
		}
		return path
	}
	withFile := func(path string) config.Config {
		cfg := config.Default()
		cc := cfg.Classifiers[cfg.Classifier]
		cc.EnvFile = path
		cfg.Classifiers[cfg.Classifier] = cc
		return cfg
	}
	good := keyFile("good.env", "export TYPESAFE_API_KEY='from-file'\n", 0o600)
	shared := keyFile("shared.env", "TYPESAFE_API_KEY=from-file\n", 0o644)
	noKey := keyFile("nokey.env", "OTHER=x\n", 0o600)
	keyFile("home.env", "TYPESAFE_API_KEY=from-file\n", 0o600)
	cfAccount := keyFile("cf-account.env", "CLOUDFLARE_ACCOUNT_ID=acct\n", 0o600)
	cfBoth := keyFile("cf-both.env", "CLOUDFLARE_API_TOKEN=t\nCLOUDFLARE_ACCOUNT_ID=acct\n", 0o600)
	openaiFile := keyFile("openai.env", "OPENAI_API_KEY=k\n", 0o600)
	missing := filepath.Join(dir, "missing.env")
	withClassifier := func(name string) config.Config {
		cfg := config.Default()
		cfg.Classifier = name
		return cfg
	}
	withEnvFile := func(name, path string) config.Config {
		cfg := withClassifier(name)
		cc := cfg.Classifiers[name]
		cc.EnvFile = path
		cfg.Classifiers[name] = cc
		return cfg
	}
	withEndpoint := func(endpoint string) config.Config {
		cfg := config.Default()
		cc := cfg.Classifiers[cfg.Classifier]
		cc.Endpoint = endpoint
		cfg.Classifiers[cfg.Classifier] = cc
		return cfg
	}
	cases := []struct {
		name       string
		cfg        config.Config
		env        map[string]string
		wantOn     bool
		wantReason string
	}{
		{"key set", config.Default(), map[string]string{"TYPESAFE_API_KEY": "k"}, true, ""},
		{"key missing", config.Default(), nil, false, "TYPESAFE_API_KEY is not set"},
		{"key from envFile", withFile(good), nil, true, ""},
		{"envFile under ~/", withFile("~/home.env"), map[string]string{"HOME": dir}, true, ""},
		{"env wins over envFile", withFile(missing), map[string]string{"TYPESAFE_API_KEY": "k"}, true, ""},
		{"envFile shared", withFile(shared), nil, false,
			"TYPESAFE_API_KEY is not set; envFile " + shared + ": open to group or others; chmod 600 it"},
		{"envFile without key", withFile(noKey), nil, false,
			"TYPESAFE_API_KEY is not set; envFile " + noKey + ": TYPESAFE_API_KEY: key not in file"},
		{"envFile missing", withFile(missing), nil, false,
			"TYPESAFE_API_KEY is not set; envFile open " + missing + ": no such file or directory"},
		{"clef account and token in env", withClassifier("clef"), map[string]string{"CLOUDFLARE_API_TOKEN": "t", "CLOUDFLARE_ACCOUNT_ID": "a"}, true, ""},
		{"clef account in envFile", withEnvFile("clef", cfAccount), map[string]string{"CLOUDFLARE_API_TOKEN": "t"}, true, ""},
		{"clef token and account in envFile", withEnvFile("clef-omni", cfBoth), nil, true, ""},
		{"clef account missing", withClassifier("clef"), map[string]string{"CLOUDFLARE_API_TOKEN": "t"}, false, "CLOUDFLARE_ACCOUNT_ID is not set"},
		{"clef token missing wins", withClassifier("clef"), nil, false, "CLOUDFLARE_API_TOKEN is not set"},
		{"clef account not in envFile", withEnvFile("clef-flash", noKey), map[string]string{"CLOUDFLARE_API_TOKEN": "t"}, false,
			"CLOUDFLARE_ACCOUNT_ID is not set; envFile " + noKey + ": CLOUDFLARE_ACCOUNT_ID: key not in file"},
		{"clef account envFile shared", withEnvFile("clef", shared), map[string]string{"CLOUDFLARE_API_TOKEN": "t"}, false,
			"CLOUDFLARE_ACCOUNT_ID is not set; envFile " + shared + ": open to group or others; chmod 600 it"},
		{"openai key set", withClassifier("openai"), map[string]string{"OPENAI_API_KEY": "k"}, true, ""},
		{"openai key missing", withClassifier("openai"), nil, false, "OPENAI_API_KEY is not set"},
		{"openai key from envFile", withEnvFile("openai", openaiFile), nil, true, ""},
		{"bad placeholder", withEndpoint("https://x/{acct}"), map[string]string{"TYPESAFE_API_KEY": "k"}, false,
			"endpoint placeholder {acct}: name must match ^[A-Z][A-Z0-9_]*$"},
		{"no key needed", func() config.Config { c := config.Default(); c.Classifier = "ollama"; return c }(), nil, true, ""},
		{"not configured", unknown, nil, false, "no classifier configured"},
		{"off", func() config.Config { c := config.Default(); c.Classifier = config.ClassifierOff; return c }(),
			map[string]string{"TYPESAFE_API_KEY": "k"}, false, "classifier is off in the config"},
		{"unknown api", withAPI("bogus"), map[string]string{"TYPESAFE_API_KEY": "k"}, false, "unknown api bogus"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			asker, reason := newClassifier(tc.cfg, func(k string) string { return tc.env[k] })
			if on := asker != nil; on != tc.wantOn || reason != tc.wantReason {
				t.Errorf("newClassifier = on %v, %q; want on %v, %q", on, reason, tc.wantOn, tc.wantReason)
			}
		})
	}
}

func TestRouterDepsCarryTheReasonTheClassifierIsOff(t *testing.T) {
	tests := []struct {
		name   string
		off    bool
		env    []string
		reason string
	}{
		{"key unset", true, nil, "TYPESAFE_API_KEY is not set"},
		{"key set", false, []string{"TYPESAFE_API_KEY=k"}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			env := append([]string{"HOME=" + dir, "CODEX_HOME=" + dir}, tt.env...)
			d, _ := RouterDeps(Options{Config: config.Default(), Env: env}, slog.New(slog.DiscardHandler))
			if (d.Classifier == nil) != tt.off || d.ClassifierOffReason != tt.reason {
				t.Errorf("classifier nil %v, reason %q", d.Classifier == nil, d.ClassifierOffReason)
			}
			if got := route.New(d).SnapshotFor(""); got.ClassifierOff != tt.off || got.ClassifierOffReason != tt.reason {
				t.Errorf("snapshot off %v reason %q", got.ClassifierOff, got.ClassifierOffReason)
			}
		})
	}
}

func TestRouterDepsNeverLogsTheFileKey(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	if err := os.WriteFile(path, []byte("TYPESAFE_API_KEY=sk-file-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []os.FileMode{0o600, 0o644} {
		if err := os.Chmod(path, mode); err != nil {
			t.Fatal(err)
		}
		cfg := config.Default()
		cc := cfg.Classifiers[cfg.Classifier]
		cc.EnvFile = path
		cfg.Classifiers[cfg.Classifier] = cc
		var logs strings.Builder
		log := slog.New(slog.NewJSONHandler(&logs, nil))
		d, _ := RouterDeps(Options{Config: cfg, Env: []string{"HOME=" + dir, "CODEX_HOME=" + dir}}, log)
		if on := d.Classifier != nil; on != (mode == 0o600) {
			t.Errorf("mode %o: classifier on = %v", mode, on)
		}
		if strings.Contains(logs.String(), "sk-file-secret") {
			t.Errorf("mode %o: log leaks the key: %s", mode, logs.String())
		}
		if mode == 0o644 && strings.Count(logs.String(), "chmod 600") != 1 {
			t.Errorf("mode %o: want one chmod 600 log line: %s", mode, logs.String())
		}
	}
}

func TestMakeRuntimeDirCreatesPrivateDirs(t *testing.T) {
	base := filepath.Join(shortTempDir(t), "rt")
	dir, id, err := makeRuntimeDir(base, nil)
	if err != nil {
		t.Fatal(err)
	}
	if dir != filepath.Join(base, id) || len(id) != 2*runtimeIDBytes {
		t.Errorf("dir = %q, id = %q", dir, id)
	}
	for _, p := range []string{base, dir} {
		fi, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != 0o700 {
			t.Errorf("%s mode = %o, want 700", p, fi.Mode().Perm())
		}
	}
	other, _, err := makeRuntimeDir(base, nil)
	if err != nil || other == dir {
		t.Errorf("second session dir = %q, %v; want a fresh one", other, err)
	}
}

func TestMakeRuntimeDirRejectsUnsafeBase(t *testing.T) {
	cases := []struct {
		name    string
		prepare func(t *testing.T, base string)
		want    string
	}{
		{"group readable", func(t *testing.T, base string) { mkdirMode(t, base, 0o750) }, "must not be accessible"},
		{"world writable", func(t *testing.T, base string) { mkdirMode(t, base, 0o777) }, "must not be accessible"},
		{"symlink", func(t *testing.T, base string) {
			target := base + "-target"
			mkdirMode(t, target, 0o700)
			if err := os.Symlink(target, base); err != nil {
				t.Fatal(err)
			}
		}, "is not a directory"},
		{"regular file", func(t *testing.T, base string) {
			if err := os.WriteFile(base, nil, 0o600); err != nil {
				t.Fatal(err)
			}
		}, "runtime dir"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			base := filepath.Join(shortTempDir(t), "rt")
			tc.prepare(t, base)
			_, _, err := makeRuntimeDir(base, nil)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("makeRuntimeDir err = %v, want it to contain %q", err, tc.want)
			}
			if entries, _ := os.ReadDir(base); len(entries) != 0 {
				t.Errorf("created %v under an unsafe base", entries)
			}
		})
	}
}

func mkdirMode(t *testing.T, dir string, mode os.FileMode) {
	t.Helper()
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, mode); err != nil {
		t.Fatal(err)
	}
}
