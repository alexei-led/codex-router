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

// TestNewRouterReportsTheClassifierOff keeps both modes of the launcher root:
// with no key the engine says the classifier is off and why, with the key it
// carries a classifier.
func TestNewRouterReportsTheClassifierOff(t *testing.T) {
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
			eng, _, _ := newRouter(Options{Config: config.Default(), Env: env}, slog.New(slog.DiscardHandler), nil)
			if got := eng.SnapshotFor(""); got.ClassifierOff != tt.off || got.ClassifierOffReason != tt.reason {
				t.Errorf("snapshot off %v, reason %q; want off %v, reason %q", got.ClassifierOff, got.ClassifierOffReason, tt.off, tt.reason)
			}
		})
	}
}

// TestNewRouterNeverLogsTheFileKey keeps the credential out of the launcher's
// log: the reason it logs names the file and the fix, never the value.
func TestNewRouterNeverLogsTheFileKey(t *testing.T) {
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
		eng, _, _ := newRouter(Options{Config: cfg, Env: []string{"HOME=" + dir, "CODEX_HOME=" + dir}}, log, nil)
		if on := !eng.SnapshotFor("").ClassifierOff; on != (mode == 0o600) {
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
