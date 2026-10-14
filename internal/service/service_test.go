package service

import (
	"context"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
)

var update = flag.Bool("update", false, "rewrite golden files")

// fakeRunner records commands and answers them from replies, keyed by
// command line prefix. Unknown commands succeed with no output.
type fakeRunner struct {
	mu      sync.Mutex
	calls   []string
	replies map[string]reply
}

type reply struct {
	out string
	err error
}

func (f *fakeRunner) run(_ context.Context, name string, args ...string) (string, error) {
	line := strings.Join(append([]string{name}, args...), " ")
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, line)
	for prefix, r := range f.replies {
		if strings.HasPrefix(line, prefix) {
			return r.out, r.err
		}
	}
	return "", nil
}

func (f *fakeRunner) take() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := f.calls
	f.calls = nil
	return out
}

var errExit = errors.New("exit status 1")

func newManager(t *testing.T, goos string, f *fakeRunner) (*Manager, string) {
	t.Helper()
	home := t.TempDir()
	m, err := New(Options{GOOS: goos, Home: home, UID: 501, Run: f.run})
	if err != nil {
		t.Fatal(err)
	}
	return m, home
}

func spec(homeID string) Spec {
	return Spec{
		HomeID:   homeID,
		HomePath: "/Users/u/.codex-work",
		StateDir: "/Users/u/.local/state",
		Config:   "/Users/u/.config/codex-router/config.json",
		Bin:      "/opt/homebrew/bin/codex-router",
		Port:     53611,
		LogDir:   "/Users/u/.local/state/codex-router/homes/3fa9c0d1e2b4/logs",
	}
}

func TestUnitFilesMatchGolden(t *testing.T) {
	cases := []struct {
		goos    string
		dir     string
		golden  map[string]string // unit file -> golden file
		reloads bool
	}{
		{"darwin", "Library/LaunchAgents", map[string]string{
			"com.github.alexei-led.codex-router.3fa9c0d1e2b4.edge.plist":  "launchd-edge.plist",
			"com.github.alexei-led.codex-router.3fa9c0d1e2b4.brain.plist": "launchd-brain.plist",
		}, false},
		{"linux", ".config/systemd/user", map[string]string{
			"codex-router-edge@3fa9c0d1e2b4.socket":   "systemd-edge.socket",
			"codex-router-edge@3fa9c0d1e2b4.service":  "systemd-edge.service",
			"codex-router-brain@3fa9c0d1e2b4.service": "systemd-brain.service",
		}, true},
	}
	for _, tc := range cases {
		t.Run(tc.goos, func(t *testing.T) {
			f := &fakeRunner{}
			m, home := newManager(t, tc.goos, f)
			if err := m.Install(context.Background(), spec("3fa9c0d1e2b4")); err != nil {
				t.Fatal(err)
			}
			des, err := os.ReadDir(filepath.Join(home, tc.dir))
			if err != nil || len(des) != len(tc.golden) {
				t.Fatalf("unit dir = %v, %v; want %d files", des, err, len(tc.golden))
			}
			for file, golden := range tc.golden {
				got, err := os.ReadFile(filepath.Join(home, tc.dir, file))
				if err != nil {
					t.Fatal(err)
				}
				gp := filepath.Join("testdata", golden)
				if *update {
					if err := os.WriteFile(gp, got, 0o644); err != nil {
						t.Fatal(err)
					}
				}
				want, err := os.ReadFile(gp)
				if err != nil {
					t.Fatal(err)
				}
				if string(got) != string(want) {
					t.Errorf("%s differs from %s:\n%s", file, gp, got)
				}
			}
		})
	}
}

func TestInstallLoadsAndReinstallUnloadsFirst(t *testing.T) {
	cases := []struct {
		goos      string
		first     []string
		reinstall []string
	}{
		{"darwin",
			[]string{
				"launchctl bootstrap gui/501 {dir}/com.github.alexei-led.codex-router.h1.edge.plist",
				"launchctl bootstrap gui/501 {dir}/com.github.alexei-led.codex-router.h1.brain.plist",
			},
			[]string{
				"launchctl bootout gui/501/com.github.alexei-led.codex-router.h1.edge",
				"launchctl bootout gui/501/com.github.alexei-led.codex-router.h1.brain",
				"launchctl bootstrap gui/501 {dir}/com.github.alexei-led.codex-router.h1.edge.plist",
				"launchctl bootstrap gui/501 {dir}/com.github.alexei-led.codex-router.h1.brain.plist",
			}},
		{"linux",
			[]string{
				"systemctl --user daemon-reload",
				"systemctl --user enable --now codex-router-edge@h1.socket codex-router-brain@h1.service",
			},
			[]string{
				"systemctl --user disable --now codex-router-edge@h1.socket codex-router-edge@h1.service codex-router-brain@h1.service",
				"systemctl --user daemon-reload",
				"systemctl --user enable --now codex-router-edge@h1.socket codex-router-brain@h1.service",
			}},
	}
	for _, tc := range cases {
		t.Run(tc.goos, func(t *testing.T) {
			f := &fakeRunner{}
			m, _ := newManager(t, tc.goos, f)
			dir := filepath.Dir(m.Units("h1")[0].Path)
			expand := func(lines []string) []string {
				out := make([]string, len(lines))
				for i, l := range lines {
					out[i] = strings.ReplaceAll(l, "{dir}", dir)
				}
				return out
			}
			for _, want := range [][]string{tc.first, tc.reinstall} {
				if err := m.Install(context.Background(), spec("h1")); err != nil {
					t.Fatal(err)
				}
				if got := f.take(); !slices.Equal(got, expand(want)) {
					t.Errorf("commands:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(expand(want), "\n"))
				}
			}
		})
	}
}

func TestInstallFailsWhenLoadFails(t *testing.T) {
	f := &fakeRunner{replies: map[string]reply{"launchctl bootstrap": {"Bootstrap failed: 5", errExit}}}
	m, _ := newManager(t, "darwin", f)
	if err := m.Install(context.Background(), spec("h1")); err == nil {
		t.Error("Install with a failing bootstrap: want error")
	}
}

func TestTwoHomesGetTheirOwnUnits(t *testing.T) {
	for _, goos := range []string{"darwin", "linux"} {
		t.Run(goos, func(t *testing.T) {
			f := &fakeRunner{}
			m, _ := newManager(t, goos, f)
			for _, id := range []string{"aaaaaaaaaaaa", "bbbbbbbbbbbb"} {
				s := spec(id)
				if err := m.Install(context.Background(), s); err != nil {
					t.Fatal(err)
				}
			}
			a, b := m.Installed("aaaaaaaaaaaa"), m.Installed("bbbbbbbbbbbb")
			if len(a) == 0 || len(a) != len(b) {
				t.Fatalf("installed: %d and %d units", len(a), len(b))
			}
			for i := range a {
				if a[i].Name == b[i].Name || a[i].Path == b[i].Path {
					t.Errorf("homes share unit %s", a[i].Name)
				}
				if !strings.Contains(a[i].Name, "aaaaaaaaaaaa") {
					t.Errorf("unit %s does not name its home", a[i].Name)
				}
			}

			if _, err := m.Uninstall(context.Background(), "aaaaaaaaaaaa"); err != nil {
				t.Fatal(err)
			}
			if got := m.Installed("bbbbbbbbbbbb"); len(got) != len(b) {
				t.Errorf("uninstalling home a left home b with %d units, want %d", len(got), len(b))
			}
		})
	}
}

func TestUninstall(t *testing.T) {
	cases := []struct {
		goos string
		want []string
	}{
		{"darwin", []string{
			"launchctl bootout gui/501/com.github.alexei-led.codex-router.h1.edge",
			"launchctl bootout gui/501/com.github.alexei-led.codex-router.h1.brain",
		}},
		{"linux", []string{
			"systemctl --user disable --now codex-router-edge@h1.socket codex-router-edge@h1.service codex-router-brain@h1.service",
			"systemctl --user daemon-reload",
		}},
	}
	for _, tc := range cases {
		t.Run(tc.goos, func(t *testing.T) {
			f := &fakeRunner{}
			m, _ := newManager(t, tc.goos, f)
			if removed, err := m.Uninstall(context.Background(), "h1"); err != nil || removed != nil {
				t.Errorf("Uninstall of nothing = %v, %v; want nil, nil", removed, err)
			}
			if got := f.take(); len(got) != 0 {
				t.Errorf("Uninstall of nothing ran %v", got)
			}
			if err := m.Install(context.Background(), spec("h1")); err != nil {
				t.Fatal(err)
			}
			f.take()
			removed, err := m.Uninstall(context.Background(), "h1")
			if err != nil || len(removed) != len(m.Units("h1")) {
				t.Fatalf("Uninstall = %d units, %v", len(removed), err)
			}
			if got := f.take(); !slices.Equal(got, tc.want) {
				t.Errorf("commands = %q, want %q", got, tc.want)
			}
			if left := m.Installed("h1"); len(left) != 0 {
				t.Errorf("unit files left: %v", left)
			}
		})
	}
}

func states(us []Unit) map[string]State {
	out := map[string]State{}
	for _, u := range us {
		out[u.Role] = u.State
	}
	return out
}

func TestStatusAndEnsure(t *testing.T) {
	const (
		edgeL  = "gui/501/com.github.alexei-led.codex-router.h1.edge"
		brainL = "gui/501/com.github.alexei-led.codex-router.h1.brain"
	)
	cases := []struct {
		name      string
		goos      string
		install   bool
		replies   map[string]reply
		want      map[string]State
		ensured   Ensured
		startCmds []string
	}{
		{"darwin not set up", "darwin", false, nil,
			map[string]State{RoleEdge: Missing, RoleBrain: Missing}, NotSetUp, nil},
		{"darwin up", "darwin", true, map[string]reply{
			"launchctl print " + edgeL:  {"state = waiting", nil},
			"launchctl print " + brainL: {"state = running", nil},
		}, map[string]State{RoleEdge: Up, RoleBrain: Up}, AlreadyUp, nil},
		{"darwin brain loaded but not running, edge not loaded", "darwin", true, map[string]reply{
			"launchctl print " + edgeL:  {"Could not find service", errExit},
			"launchctl print " + brainL: {"state = not running", nil},
		}, map[string]State{RoleEdge: Down, RoleBrain: Down}, Started, []string{
			"launchctl bootstrap gui/501 {dir}/com.github.alexei-led.codex-router.h1.edge.plist",
			"launchctl kickstart " + brainL,
		}},
		{"linux not set up", "linux", false, nil,
			map[string]State{RoleEdge: Missing, RoleBrain: Missing}, NotSetUp, nil},
		{"linux up", "linux", true, map[string]reply{
			"systemctl --user is-active": {"active\n", nil},
		}, map[string]State{RoleEdge: Up, RoleBrain: Up}, AlreadyUp, nil},
		{"linux brain down", "linux", true, map[string]reply{
			"systemctl --user is-active codex-router-edge@h1.socket":   {"active\n", nil},
			"systemctl --user is-active codex-router-brain@h1.service": {"inactive\n", errExit},
		}, map[string]State{RoleEdge: Up, RoleBrain: Down}, Started, []string{
			"systemctl --user start --no-block codex-router-brain@h1.service",
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeRunner{}
			m, _ := newManager(t, tc.goos, f)
			if tc.install {
				if err := m.Install(context.Background(), spec("h1")); err != nil {
					t.Fatal(err)
				}
			}
			f.take()
			f.replies = tc.replies
			if got := states(m.Status(context.Background(), "h1")); !equalStates(got, tc.want) {
				t.Errorf("Status = %v, want %v", got, tc.want)
			}
			f.take()
			got, err := m.Ensure(context.Background(), "h1")
			if err != nil || got != tc.ensured {
				t.Errorf("Ensure = %v, %v; want %v", got, err, tc.ensured)
			}
			dir := filepath.Dir(m.Units("h1")[0].Path)
			var starts []string
			for _, c := range f.take() {
				if !strings.Contains(c, " print ") && !strings.Contains(c, " is-active ") {
					starts = append(starts, c)
				}
			}
			want := make([]string, len(tc.startCmds))
			for i, c := range tc.startCmds {
				want[i] = strings.ReplaceAll(c, "{dir}", dir)
			}
			if !slices.Equal(starts, want) {
				t.Errorf("Ensure ran %q, want %q", starts, want)
			}
		})
	}
}

func equalStates(a, b map[string]State) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

func TestEnsureReportsAStartFailure(t *testing.T) {
	f := &fakeRunner{}
	m, _ := newManager(t, "linux", f)
	if err := m.Install(context.Background(), spec("h1")); err != nil {
		t.Fatal(err)
	}
	f.replies = map[string]reply{
		"systemctl --user is-active": {"inactive\n", errExit},
		"systemctl --user start":     {"Failed to start", errExit},
	}
	if _, err := m.Ensure(context.Background(), "h1"); err == nil {
		t.Error("Ensure with a failing start: want error")
	}
}

func TestExecLineQuotes(t *testing.T) {
	got := execLine([]string{"/opt/bin/codex-router", "--home", `/Users/u/my "codex" 100%`, `$HOME\x`})
	want := `"/opt/bin/codex-router" "--home" "/Users/u/my \"codex\" 100%%" "$$HOME\\x"`
	if got != want {
		t.Errorf("execLine = %s\nwant      %s", got, want)
	}
}

func TestPlistEscapesPaths(t *testing.T) {
	f := &fakeRunner{}
	m, _ := newManager(t, "darwin", f)
	s := spec("h1")
	s.HomePath = "/Users/u/a&b <c>"
	if err := m.Install(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(m.Units("h1")[0].Path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "<string>/Users/u/a&amp;b &lt;c&gt;</string>") {
		t.Errorf("plist does not escape the home path:\n%s", b)
	}
}

func TestNewRejects(t *testing.T) {
	if _, err := New(Options{GOOS: "windows", Home: t.TempDir()}); err == nil {
		t.Error("windows: want error")
	}
	if _, err := New(Options{GOOS: "linux", Home: "relative"}); err == nil {
		t.Error("relative HOME: want error")
	}
}

func TestSystemdHonorsXDGConfigHome(t *testing.T) {
	cfg := t.TempDir()
	m, err := New(Options{GOOS: "linux", Home: t.TempDir(), ConfigHome: cfg, Run: (&fakeRunner{}).run})
	if err != nil {
		t.Fatal(err)
	}
	if got := filepath.Dir(m.Units("h1")[0].Path); got != filepath.Join(cfg, "systemd", "user") {
		t.Errorf("unit dir = %s, want under XDG_CONFIG_HOME", got)
	}
}
