package service

import (
	"context"
	"errors"
	"flag"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
)

var update = flag.Bool("update", false, "rewrite golden files")

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
		golden  map[string]string
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

type verbs struct {
	load, unload, reload string
	names                []string
}

var backends = map[string]verbs{
	"darwin": {load: "bootstrap", unload: "bootout", names: []string{"h1.edge", "h1.brain"}},
	"linux":  {load: "enable --now", unload: "disable --now", reload: "daemon-reload", names: []string{"edge@h1.socket", "brain@h1.service"}},
}

func indexOf(calls []string, parts ...string) int {
	return slices.IndexFunc(calls, func(c string) bool {
		return !slices.ContainsFunc(parts, func(p string) bool { return !strings.Contains(c, p) })
	})
}

func TestInstallLoadsAndReinstallUnloadsFirst(t *testing.T) {
	for goos, v := range backends {
		t.Run(goos, func(t *testing.T) {
			f := &fakeRunner{}
			m, _ := newManager(t, goos, f)
			if err := m.Install(context.Background(), spec("h1")); err != nil {
				t.Fatal(err)
			}
			first := f.take()
			for _, name := range v.names {
				if indexOf(first, v.load, name) < 0 {
					t.Errorf("first install did not load %s: %q", name, first)
				}
			}
			if indexOf(first, v.unload) >= 0 {
				t.Errorf("first install unloaded something: %q", first)
			}

			if err := m.Install(context.Background(), spec("h1")); err != nil {
				t.Fatal(err)
			}
			again := f.take()
			firstLoad := indexOf(again, v.load)
			for _, name := range v.names {
				if i := indexOf(again, v.unload, name); i < 0 || i > firstLoad {
					t.Errorf("reinstall did not unload %s before loading: %q", name, again)
				}
			}
		})
	}
}

func TestInstallAndEnsureReturnTheCommandError(t *testing.T) {
	down := reply{"inactive\n", errExit}
	failed := reply{"failed", errExit}
	tests := []struct {
		name    string
		goos    string
		ensure  bool
		replies map[string]reply
	}{
		{"darwin load", "darwin", false, map[string]reply{"launchctl bootstrap": failed}},
		{"linux load", "linux", false, map[string]reply{"systemctl --user enable": failed}},
		{"darwin start", "darwin", true, map[string]reply{"launchctl print": down, "launchctl bootstrap": failed}},
		{"linux start", "linux", true, map[string]reply{"systemctl --user is-active": down, "systemctl --user start": failed}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &fakeRunner{}
			m, _ := newManager(t, tt.goos, f)
			ctx := context.Background()
			var err error
			if tt.ensure {
				if err := m.Install(ctx, spec("h1")); err != nil {
					t.Fatal(err)
				}
				f.replies = tt.replies
				_, err = m.Ensure(ctx, "h1")
			} else {
				f.replies = tt.replies
				err = m.Install(ctx, spec("h1"))
			}
			if !errors.Is(err, errExit) {
				t.Errorf("error = %v, want the failing command's error", err)
			}
		})
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
	for goos, v := range backends {
		t.Run(goos, func(t *testing.T) {
			f := &fakeRunner{}
			m, _ := newManager(t, goos, f)
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
			got := f.take()
			last := -1
			for _, name := range v.names {
				i := indexOf(got, v.unload, name)
				if i < 0 {
					t.Errorf("%s not unloaded: %q", name, got)
				}
				last = max(last, i)
			}
			if v.reload != "" && indexOf(got, v.reload) <= last {
				t.Errorf("no %s after unloading: %q", v.reload, got)
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
			if got := states(m.Status(context.Background(), "h1")); !maps.Equal(got, tc.want) {
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
	tests := []struct {
		name string
		o    Options
		want string
	}{
		{"unsupported OS", Options{GOOS: "windows", Home: "/home/u"}, "windows has no supported service manager"},
		{"relative HOME", Options{GOOS: "linux", Home: "relative"}, "HOME is not an absolute path"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := New(tt.o); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("New = %v, want error containing %q", err, tt.want)
			}
		})
	}
}

func TestStateString(t *testing.T) {
	tests := []struct {
		s    State
		want string
	}{{Missing, "not installed"}, {Down, "down"}, {Up, "up"}}
	for _, tt := range tests {
		if got := tt.s.String(); got != tt.want {
			t.Errorf("State(%d) = %q, want %q", tt.s, got, tt.want)
		}
	}
}

func TestExecReturnsOutputAndWrapsFailure(t *testing.T) {
	out, err := Exec(context.Background(), "sh", "-c", "echo hi")
	if err != nil || out != "hi\n" {
		t.Errorf("Exec = %q, %v; want hi, nil", out, err)
	}
	out, err = Exec(context.Background(), "sh", "-c", "echo boom; exit 3")
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 3 || out != "boom\n" || !strings.Contains(err.Error(), "boom") {
		t.Errorf("Exec = %q, %v; want exit 3 with the output in the error", out, err)
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
