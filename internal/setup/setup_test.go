package setup

import (
	"bytes"
	"context"
	"flag"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alexei-led/codex-router/internal/brain"
	"github.com/alexei-led/codex-router/internal/home"
	"github.com/alexei-led/codex-router/internal/service"
)

var update = flag.Bool("update", false, "rewrite golden files")

var testProvider = Provider{Port: 53611, Token: "test-token"}

func TestAddProviderGolden(t *testing.T) {
	inputs, err := filepath.Glob(filepath.Join("testdata", "*.toml"))
	if err != nil || len(inputs) == 0 {
		t.Fatalf("no inputs: %v", err)
	}
	for _, in := range inputs {
		t.Run(filepath.Base(in), func(t *testing.T) {
			src, err := os.ReadFile(in)
			if err != nil {
				t.Fatal(err)
			}
			got, err := AddProvider(src, testProvider)
			if err != nil {
				t.Fatalf("AddProvider: %v", err)
			}
			golden := in + ".golden"
			if *update {
				if err := os.WriteFile(golden, got, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(golden)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, want) {
				t.Errorf("AddProvider differs from %s:\n%s", golden, got)
			}

			again, err := AddProvider(got, testProvider)
			if err != nil || !bytes.Equal(again, got) {
				t.Errorf("AddProvider on its own output changed it (err %v):\n%s", err, again)
			}
			back, found, err := RemoveBlocks(got)
			if err != nil || !found || !bytes.Equal(back, src) {
				t.Errorf("RemoveBlocks = found %v, err %v, and not the input byte for byte:\n%q\nwant\n%q", found, err, back, src)
			}
		})
	}
}

func TestAddProviderReplacesAnEarlierSetup(t *testing.T) {
	src := []byte("model = \"gpt-5.5\"\n\n[tui]\nnotifications = true\n")
	first, err := AddProvider(src, testProvider)
	if err != nil {
		t.Fatal(err)
	}
	moved := Provider{Port: 40000, Token: "second-token"}
	second, err := AddProvider(first, moved)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(second, []byte(testProvider.Token)) || !bytes.Contains(second, []byte(":40000/v1")) {
		t.Errorf("second setup kept the old provider:\n%s", second)
	}
	if n := bytes.Count(second, []byte(blockBegin)); n != 2 {
		t.Errorf("second setup has %d blocks, want 2:\n%s", n, second)
	}
}

func TestAddProviderRefuses(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want string
	}{
		{"model_provider set", "model_provider = \"openai\"\n", "already sets model_provider"},
		{"router table defined", "[model_providers.router]\nname = \"mine\"\n", "already defines model_providers.router"},
		{"router dotted key", "model_providers.router.name = \"mine\"\n", "already defines model_providers.router"},
		{"does not parse", "model = \n", "does not parse"},
		{"block without end", blockBegin + "\nmodel_provider = \"router\"\n", "without its end marker"},
		{"model_providers not a table", "model_providers = 1\n", "found no place"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := AddProvider([]byte(tc.src), testProvider)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("AddProvider = %q, %v; want error containing %q", got, err, tc.want)
			}
		})
	}
}

func TestRemoveBlocksWithoutBlocks(t *testing.T) {
	src := []byte("# a comment\nmodel = \"x\"\n# <<< codex-router <<<\n")
	got, found, err := RemoveBlocks(src)
	if err != nil || found || !bytes.Equal(got, src) {
		t.Errorf("RemoveBlocks = %q, %v, %v; want the input unchanged and found false", got, found, err)
	}
}

func TestDiffShowsHunksAndRedactsTheToken(t *testing.T) {
	src := []byte("model = \"x\"\n\n[tui]\nnotifications = true\n")
	out, err := AddProvider(src, testProvider)
	if err != nil {
		t.Fatal(err)
	}
	want := `@@ -3 +3 @@
+ # >>> codex-router >>>
+ model_provider = "router"
+ # <<< codex-router <<<
@@ -5 +8 @@
+ # >>> codex-router >>>
+ [model_providers.router]
+ name = "router"
+ base_url = "http://127.0.0.1:53611/v1"
+ wire_api = "responses"
+ requires_openai_auth = true
+ http_headers = { "X-Codex-Router-Token" = "<redacted>" }
+ # <<< codex-router <<<
`
	if got := Diff(src, out); got != want {
		t.Errorf("Diff =\n%s\nwant\n%s", got, want)
	}
	back, _, _ := RemoveBlocks(out)
	if got := Diff(out, back); strings.Contains(got, testProvider.Token) || !strings.Contains(got, `- http_headers = { "X-Codex-Router-Token" = "<redacted>" }`) {
		t.Errorf("undo Diff leaks or misses the token line:\n%s", got)
	}
	if got := Diff(src, src); got != "" {
		t.Errorf("Diff of equal files = %q, want empty", got)
	}
}

// fakeRunner records service manager commands; they all succeed.
type fakeRunner struct {
	mu    sync.Mutex
	calls []string
}

func (f *fakeRunner) run(_ context.Context, name string, args ...string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, name+" "+strings.Join(args, " "))
	return "", nil
}

func (f *fakeRunner) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

type fixture struct {
	h      home.Home
	state  string
	runner *fakeRunner
	mgr    *service.Manager
	out    *bytes.Buffer
}

func newFixture(t *testing.T, config string) *fixture {
	t.Helper()
	base := t.TempDir()
	codexHome := filepath.Join(base, ".codex-work")
	if err := os.Mkdir(codexHome, 0o700); err != nil {
		t.Fatal(err)
	}
	if config != "" {
		if err := os.WriteFile(filepath.Join(codexHome, ConfigFile), []byte(config), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	h, err := home.Resolve(func(k string) string {
		if k == "CODEX_HOME" {
			return codexHome
		}
		return ""
	})
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{h: h, state: filepath.Join(base, "state"), runner: &fakeRunner{}, out: &bytes.Buffer{}}
	f.mgr, err = service.New(service.Options{GOOS: "darwin", Home: filepath.Join(base, "user"), UID: 501, Run: f.runner.run})
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *fixture) options() Options {
	return Options{
		Home: f.h, StateDir: f.state, Bin: "/usr/local/bin/codex-router", Service: f.mgr, Out: f.out,
		Now: func() time.Time { return time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC) },
	}
}

func (f *fixture) run(t *testing.T, o Options) error {
	t.Helper()
	f.out.Reset()
	err := Run(context.Background(), o)
	if first, _, _ := strings.Cut(f.out.String(), "\n"); first != "Codex home work: "+f.h.Path+" (id "+f.h.ID+")" {
		t.Errorf("first line = %q, want the resolved home", first)
	}
	return err
}

func (f *fixture) configPath() string { return filepath.Join(f.h.Path, ConfigFile) }

func (f *fixture) stateDir() string {
	return brain.HomeDir(filepath.Join(f.state, "codex-router"), f.h.ID)
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func mode(t *testing.T, p string) os.FileMode {
	t.Helper()
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	return fi.Mode().Perm()
}

func backups(t *testing.T, dir string) []string {
	t.Helper()
	got, err := filepath.Glob(filepath.Join(dir, ConfigFile+".codex-router-*.bak"))
	if err != nil {
		t.Fatal(err)
	}
	return got
}

const userConfig = "# my settings\nmodel = \"gpt-5.5\" # default\n\n[profiles.fast]\nmodel = \"gpt-5.5-mini\"\n"

var hexToken = regexp.MustCompile(`[0-9a-f]{32}`)

func TestSetupAndUndoRoundTrip(t *testing.T) {
	f := newFixture(t, userConfig)
	if err := f.run(t, f.options()); err != nil {
		t.Fatalf("setup: %v\n%s", err, f.out)
	}
	dir := f.stateDir()
	token := strings.TrimSpace(readFile(t, filepath.Join(dir, TokenFile)))
	port := strings.TrimSpace(readFile(t, filepath.Join(dir, PortFile)))
	if !hexToken.MatchString(token) {
		t.Errorf("token = %q, want 32 hex", token)
	}
	if strings.Contains(f.out.String(), token) {
		t.Errorf("setup output contains the token:\n%s", f.out)
	}
	cfg := readFile(t, f.configPath())
	for _, want := range []string{"# my settings\n", `"X-Codex-Router-Token" = "` + token + `"`, "http://127.0.0.1:" + port + "/v1"} {
		if !strings.Contains(cfg, want) {
			t.Errorf("config.toml lacks %q:\n%s", want, cfg)
		}
	}
	pluginEnv := filepath.Join(f.h.Path, PluginEnvFile)
	if got, want := readFile(t, pluginEnv), "CODEX_ROUTER_BIN=/usr/local/bin/codex-router\nXDG_STATE_HOME="+f.state+"\n"; !strings.HasSuffix(got, want) {
		t.Errorf("%s =\n%s\nwant it to end with\n%s", PluginEnvFile, got, want)
	}
	for _, p := range []string{f.configPath(), filepath.Join(dir, TokenFile), filepath.Join(dir, PortFile), pluginEnv} {
		if m := mode(t, p); m != 0o600 {
			t.Errorf("%s mode = %v, want 0600", p, m)
		}
	}
	if m := mode(t, filepath.Join(dir, logsDir)); m != 0o700 {
		t.Errorf("logs dir mode = %v, want 0700", m)
	}
	bak := backups(t, f.h.Path)
	if len(bak) != 1 || readFile(t, bak[0]) != userConfig || mode(t, bak[0]) != 0o600 {
		t.Fatalf("backups = %v; want one 0600 copy of the old config", bak)
	}
	if got := len(f.mgr.Installed(f.h.ID)); got != 2 {
		t.Errorf("installed units = %d, want 2", got)
	}
	unit := readFile(t, f.mgr.Units(f.h.ID)[0].Path)
	for _, want := range []string{"<string>--home</string>\n\t\t<string>" + f.h.Path + "</string>", "<string>--state-dir</string>\n\t\t<string>" + f.state + "</string>", "<string>" + port + "</string>"} {
		if !strings.Contains(unit, want) {
			t.Errorf("edge unit lacks %q:\n%s", want, unit)
		}
	}

	undo := f.options()
	undo.Undo = true
	if err := f.run(t, undo); err != nil {
		t.Fatalf("undo: %v\n%s", err, f.out)
	}
	if got := readFile(t, f.configPath()); got != userConfig {
		t.Errorf("config after undo is not the original byte for byte:\n%q", got)
	}
	if got := f.mgr.Installed(f.h.ID); len(got) != 0 {
		t.Errorf("units left after undo: %v", got)
	}
	if _, err := os.Stat(pluginEnv); !os.IsNotExist(err) {
		t.Errorf("%s after undo: %v, want gone", PluginEnvFile, err)
	}
	if _, err := os.Stat(filepath.Join(dir, TokenFile)); err != nil {
		t.Errorf("undo without --purge removed state: %v", err)
	}
	if strings.Contains(f.out.String(), token) {
		t.Errorf("undo output contains the token:\n%s", f.out)
	}

	undo.Purge = true
	if err := f.run(t, undo); err != nil {
		t.Fatalf("undo --purge: %v", err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("state dir after --purge: %v, want gone", err)
	}
}

func TestSetupWithoutConfigCreatesOne(t *testing.T) {
	f := newFixture(t, "")
	if err := f.run(t, f.options()); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, f.configPath()); !strings.HasPrefix(got, blockBegin+"\nmodel_provider") {
		t.Errorf("new config.toml =\n%s", got)
	}
	if bak := backups(t, f.h.Path); len(bak) != 0 {
		t.Errorf("backups of a missing config: %v", bak)
	}
	undo := f.options()
	undo.Undo = true
	if err := f.run(t, undo); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, f.configPath()); got != "" {
		t.Errorf("config after undo = %q, want empty", got)
	}
}

func TestSetupRerunKeepsPortAndToken(t *testing.T) {
	f := newFixture(t, userConfig)
	if err := f.run(t, f.options()); err != nil {
		t.Fatal(err)
	}
	dir := f.stateDir()
	port, token, cfg := readFile(t, filepath.Join(dir, PortFile)), readFile(t, filepath.Join(dir, TokenFile)), readFile(t, f.configPath())
	o := f.options()
	o.FreePort = func() (int, error) { t.Error("re-run picked a new port"); return 1, nil }
	if err := f.run(t, o); err != nil {
		t.Fatal(err)
	}
	if readFile(t, filepath.Join(dir, PortFile)) != port || readFile(t, filepath.Join(dir, TokenFile)) != token || readFile(t, f.configPath()) != cfg {
		t.Error("re-run changed the port, token or config")
	}
	if !strings.Contains(f.out.String(), "no change") {
		t.Errorf("re-run output lacks 'no change':\n%s", f.out)
	}
	if bak := backups(t, f.h.Path); len(bak) != 1 {
		t.Errorf("re-run made another backup: %v", bak)
	}
}

func TestSetupDryRunWritesNothing(t *testing.T) {
	for _, undo := range []bool{false, true} {
		t.Run("undo="+strconv.FormatBool(undo), func(t *testing.T) {
			f := newFixture(t, userConfig)
			if undo {
				if err := f.run(t, f.options()); err != nil {
					t.Fatal(err)
				}
			}
			before := readFile(t, f.configPath())
			homeBefore, _ := os.ReadDir(f.h.Path)
			stateBefore, _ := os.ReadDir(f.stateDir())
			unitsBefore, calls := len(f.mgr.Installed(f.h.ID)), f.runner.count()
			bakBefore := backups(t, f.h.Path)

			o := f.options()
			o.DryRun, o.Undo, o.Purge = true, undo, undo
			if err := f.run(t, o); err != nil {
				t.Fatal(err)
			}
			if readFile(t, f.configPath()) != before {
				t.Error("dry run changed config.toml")
			}
			if homeAfter, _ := os.ReadDir(f.h.Path); len(homeAfter) != len(homeBefore) {
				t.Errorf("dry run changed the Codex home: %v -> %v", homeBefore, homeAfter)
			}
			if stateAfter, _ := os.ReadDir(f.stateDir()); len(stateAfter) != len(stateBefore) {
				t.Errorf("dry run changed the state dir: %v -> %v", stateBefore, stateAfter)
			}
			if len(f.mgr.Installed(f.h.ID)) != unitsBefore || f.runner.count() != calls {
				t.Error("dry run touched the units or ran the service manager")
			}
			if len(backups(t, f.h.Path)) != len(bakBefore) {
				t.Error("dry run wrote a backup")
			}
			out := f.out.String()
			if !strings.Contains(out, "<redacted>") || !strings.Contains(out, "dry run: would") || hexToken.MatchString(out) {
				t.Errorf("dry-run output wants a redacted diff and the planned steps:\n%s", out)
			}
		})
	}
}

func TestSetupSymlinkedConfig(t *testing.T) {
	f := newFixture(t, "")
	dotfiles := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(dotfiles, []byte(userConfig), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(dotfiles, f.configPath()); err != nil {
		t.Fatal(err)
	}
	err := f.run(t, f.options())
	if err == nil || !strings.Contains(err.Error(), "--follow-symlink") {
		t.Fatalf("setup with a symlinked config = %v, want a refusal naming --follow-symlink", err)
	}
	if readFile(t, dotfiles) != userConfig {
		t.Error("refused setup changed the symlink target")
	}
	if _, err := os.Stat(f.stateDir()); !os.IsNotExist(err) || len(f.mgr.Installed(f.h.ID)) != 0 {
		t.Error("refused setup wrote state or units")
	}

	o := f.options()
	o.FollowSymlink = true
	if err := f.run(t, o); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Lstat(f.configPath()); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Error("setup replaced the symlink instead of its target")
	}
	if got := readFile(t, dotfiles); !strings.Contains(got, blockBegin) || mode(t, dotfiles) != 0o600 {
		t.Errorf("target not edited to 0600:\n%s", got)
	}
}

func TestSetupRefusesWithoutWriting(t *testing.T) {
	cases := []struct {
		name   string
		config string
		prep   func(t *testing.T, f *fixture)
		want   string
	}{
		{"model_provider set", "model_provider = \"openai\"\n", nil, "already sets model_provider"},
		{"home missing", "", func(t *testing.T, f *fixture) {
			if err := os.Remove(f.h.Path); err != nil {
				t.Fatal(err)
			}
		}, "does not exist"},
		{"state dir open to others", "", func(t *testing.T, f *fixture) {
			if err := os.MkdirAll(filepath.Join(f.state, "codex-router"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(filepath.Join(f.state, "codex-router"), 0o755); err != nil {
				t.Fatal(err)
			}
		}, "group or others"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t, tc.config)
			if tc.prep != nil {
				tc.prep(t, f)
			}
			err := f.run(t, f.options())
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("setup = %v, want error containing %q", err, tc.want)
			}
			if len(f.mgr.Installed(f.h.ID)) != 0 {
				t.Error("refused setup installed units")
			}
			if b, err := os.ReadFile(f.configPath()); err == nil && string(b) != tc.config {
				t.Error("refused setup changed config.toml")
			}
		})
	}
}

func TestTwoHomesGetDistinctPortsTokensAndUnits(t *testing.T) {
	a := newFixture(t, "")
	b := newFixture(t, "")
	b.state, b.mgr = a.state, a.mgr // one user: one state dir, one unit dir
	ports := []int{41001, 41001, 41002}
	next := 0
	free := func() (int, error) {
		p := ports[next]
		next++
		return p, nil
	}
	for _, f := range []*fixture{a, b} {
		o := f.options()
		o.FreePort = free
		if err := f.run(t, o); err != nil {
			t.Fatal(err)
		}
	}
	pa, pb := readFile(t, filepath.Join(a.stateDir(), PortFile)), readFile(t, filepath.Join(b.stateDir(), PortFile))
	if pa != "41001\n" || pb != "41002\n" {
		t.Errorf("ports = %q, %q; want 41001 and 41002 (41001 is home a's)", pa, pb)
	}
	if readFile(t, filepath.Join(a.stateDir(), TokenFile)) == readFile(t, filepath.Join(b.stateDir(), TokenFile)) {
		t.Error("homes share a token")
	}
	ua, ub := a.mgr.Installed(a.h.ID), a.mgr.Installed(b.h.ID)
	if len(ua) != 2 || len(ub) != 2 || ua[0].Name == ub[0].Name || ua[1].Name == ub[1].Name {
		t.Errorf("units: %v and %v; want 2 each, distinct", ua, ub)
	}
}
