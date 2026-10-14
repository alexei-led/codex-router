package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/alexei-led/codex-router/internal/config"
	"github.com/alexei-led/codex-router/internal/control"
	"github.com/alexei-led/codex-router/internal/home"
	"github.com/alexei-led/codex-router/internal/registry"
	"github.com/alexei-led/codex-router/internal/route"
	"github.com/alexei-led/codex-router/internal/tui"
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
		{"stray sessions argument", []string{"sessions", "extra"}, exitUsage, "", `unexpected argument "extra"`},
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

func TestTUIFailsWithoutARegistry(t *testing.T) {
	notDir := filepath.Join(t.TempDir(), "state")
	if err := os.WriteFile(notDir, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	env := append(testEnv(t), "XDG_STATE_HOME="+notDir)
	code := run(context.Background(), []string{"tui"}, env, strings.NewReader(""), &stdout, &stderr)
	if code != exitError || !strings.Contains(stderr.String(), "registry:") {
		t.Errorf("exit %d, stderr %q", code, stderr.String())
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

var (
	homeWork    = home.Home{ID: "aaaa11112222", Path: "/u/.codex-work", Name: "work"}
	homeDefault = home.Home{ID: "bbbb33334444", Path: "/u/.codex", Name: "default"}
)

func testSession(h home.Home, thread, label string, minute int) route.SessionInfo {
	return route.SessionInfo{
		ID: control.SessionID(h.ID, thread), Home: h.ID, ThreadID: thread, Label: label,
		Mode: route.ModeOn, Route: config.Route{Model: "gpt-5.5", Effort: "low"},
		LastActive: time.Date(2026, 10, 14, 18, minute, 0, 0, time.UTC),
	}
}

var (
	sessWorkMain = testSession(homeWork, "019a-t1", "work · codex-router · main", 3)
	sessWorkFeat = testSession(homeWork, "019b-t2", "work · codex-router · feat-x", 2)
	sessDefault  = testSession(homeDefault, "019a-t3", "default · notes · main", 1)
)

// endpointHandler is a control.Handler serving fixed sessions.
type endpointHandler struct {
	hello    control.Hello
	sessions []route.SessionInfo
}

func (h endpointHandler) Apply(route.Command) error         { return nil }
func (h endpointHandler) Sessions() []route.SessionInfo     { return h.sessions }
func (h endpointHandler) SnapshotFor(string) route.Snapshot { return route.Snapshot{} }
func (h endpointHandler) Hello() control.Hello              { return h.hello }

// fleetFixture is a registry with two live endpoints, "ep-work" (two work
// sessions) and "ep-default", plus a dead entry. It returns the env that
// points at the registry and the sockets by endpoint id.
func fleetFixture(t *testing.T) (env []string, sockets map[string]string) {
	t.Helper()
	env, root, sockDir := testRegistry(t)
	sockets = map[string]string{
		"ep-work":    serveEndpoint(t, root, sockDir, "ep-work", homeWork, sessWorkFeat, sessWorkMain),
		"ep-default": serveEndpoint(t, root, sockDir, "ep-default", homeDefault, sessDefault),
	}
	dead := registry.Endpoint{ID: "ep-dead", Kind: registry.KindSession, Home: homeWork, Socket: filepath.Join(sockDir, "dead.sock")}
	if err := registry.Write(root, dead); err != nil {
		t.Fatal(err)
	}
	return env, sockets
}

// testRegistry returns the env that points at a fresh registry, its root,
// and a dir for sockets.
func testRegistry(t *testing.T) (env []string, root, sockDir string) {
	t.Helper()
	state := t.TempDir()
	env = append(testEnv(t), "XDG_STATE_HOME="+state)
	root, err := registry.Root(func(k string) string {
		if k == "XDG_STATE_HOME" {
			return state
		}
		return ""
	})
	if err != nil {
		t.Fatal(err)
	}
	// Unix socket paths are short on macOS, so not under t.TempDir.
	sockDir, err = os.MkdirTemp("", "crm")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(sockDir) })
	return env, root, sockDir
}

// serveEndpoint serves sessions as endpoint id of home h on a socket in
// sockDir, registers it under root, and returns the socket path.
func serveEndpoint(t *testing.T, root, sockDir, id string, h home.Home, sessions ...route.SessionInfo) string {
	t.Helper()
	sock := filepath.Join(sockDir, id+".sock")
	srv, err := control.Listen(sock, endpointHandler{
		hello:    control.Hello{EndpointID: id, Kind: registry.KindSession, Home: h},
		sessions: sessions,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	if err := registry.Write(root, registry.Endpoint{ID: id, Kind: registry.KindSession, Home: h, Socket: sock}); err != nil {
		t.Fatal(err)
	}
	return sock
}

// stubPanel replaces the panel for one test and returns the options it got.
func stubPanel(t *testing.T) *[]tui.Options {
	t.Helper()
	var got []tui.Options
	old := runPanel
	runPanel = func(_ context.Context, o tui.Options) error {
		got = append(got, o)
		return nil
	}
	t.Cleanup(func() { runPanel = old })
	return &got
}

func TestTUIResolvesSession(t *testing.T) {
	env, _ := fleetFixture(t)
	cases := []struct {
		name       string
		query      string
		wantCode   int
		wantID     string
		wantStderr []string
	}{
		{"exact id", sessDefault.ID, exitOK, sessDefault.ID, nil},
		{"exact thread id", "019a-t1", exitOK, sessWorkMain.ID, nil},
		{"unique id prefix", homeDefault.ID[:6], exitOK, sessDefault.ID, nil},
		{"unique thread id prefix", "019b", exitOK, sessWorkFeat.ID, nil},
		{"exact label", "work · codex-router · main", exitOK, sessWorkMain.ID, nil},
		{"label suffix", "codex-router · feat-x", exitOK, sessWorkFeat.ID, nil},
		{"ambiguous thread prefix", "019a", exitUsage, "", []string{`"019a" matches 2 sessions`, sessWorkMain.ID, sessDefault.ID}},
		{"ambiguous label suffix", "main", exitUsage, "", []string{"default · notes · main", "work · codex-router · main"}},
		{"ambiguous home prefix", homeWork.ID, exitUsage, "", []string{sessWorkMain.ID, sessWorkFeat.ID}},
		{"suffix inside a word", "ain", exitError, "", []string{`no running session matches "ain"`}},
		{"not found", "nope", exitError, "", []string{`no running session matches "nope"`}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := stubPanel(t)
			var stderr bytes.Buffer
			code := run(context.Background(), []string{"tui", "--session", tc.query}, env, nil, io.Discard, &stderr)
			if code != tc.wantCode {
				t.Fatalf("exit %d, want %d (stderr %q)", code, tc.wantCode, stderr.String())
			}
			for _, w := range tc.wantStderr {
				if !strings.Contains(stderr.String(), w) {
					t.Errorf("stderr %q, want it to contain %q", stderr.String(), w)
				}
			}
			switch {
			case tc.wantID == "" && len(*got) > 0:
				t.Errorf("panel ran with %+v", *got)
			case tc.wantID != "" && (len(*got) != 1 || (*got)[0].Session != tc.wantID || (*got)[0].Registry == ""):
				t.Errorf("panel options %+v, want session %s over the registry", *got, tc.wantID)
			}
		})
	}
}

func TestTUIResolvesCollidingLabels(t *testing.T) {
	env, root, sockDir := testRegistry(t)
	const shared = "work · repo · main"
	twinA := testSession(homeWork, "t-a", shared, 2)
	twinB := testSession(homeWork, "t-b", shared, 1)
	serveEndpoint(t, root, sockDir, "ep", homeWork, twinA, twinB)
	shown := home.Disambiguate([]string{shared, shared}, []string{twinA.ID, twinB.ID})
	hexA := strings.TrimPrefix(shown[0], shared+labelSep)

	cases := []struct {
		name     string
		query    string
		wantCode int
		wantID   string
	}{
		{"shared full label", shared, exitUsage, ""},
		{"shared branch suffix", "main", exitUsage, ""},
		{"shared repo and branch suffix", "repo · main", exitUsage, ""},
		{"disambiguated label", shown[1], exitOK, twinB.ID},
		{"hex suffix", hexA, exitOK, twinA.ID},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := stubPanel(t)
			var stderr bytes.Buffer
			code := run(context.Background(), []string{"tui", "--session", tc.query}, env, nil, io.Discard, &stderr)
			if code != tc.wantCode {
				t.Fatalf("exit %d, want %d (stderr %q)", code, tc.wantCode, stderr.String())
			}
			if tc.wantCode == exitUsage {
				for _, w := range shown {
					if !strings.Contains(stderr.String(), w) {
						t.Errorf("stderr %q, want it to list %q", stderr.String(), w)
					}
				}
				return
			}
			if len(*got) != 1 || (*got)[0].Session != tc.wantID {
				t.Errorf("panel options %+v, want session %s", *got, tc.wantID)
			}
		})
	}
}

func TestTUISocketBypassesTheRegistry(t *testing.T) {
	_, sockets := fleetFixture(t)
	notDir := filepath.Join(t.TempDir(), "state")
	if err := os.WriteFile(notDir, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	env := append(testEnv(t), "XDG_STATE_HOME="+notDir)
	sock := sockets["ep-work"]

	cases := []struct {
		name     string
		args     []string
		wantCode int
		want     tui.Options
	}{
		{"all sessions", []string{"tui", "--socket", sock}, exitOK, tui.Options{Socket: sock}},
		{"locked by label", []string{"tui", "--socket", sock, "--session", "feat-x"}, exitOK, tui.Options{Socket: sock, Session: sessWorkFeat.ID}},
		{"other endpoint's session", []string{"tui", "--socket", sock, "--session", "notes · main"}, exitError, tui.Options{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := stubPanel(t)
			var stderr bytes.Buffer
			code := run(context.Background(), tc.args, env, nil, io.Discard, &stderr)
			if code != tc.wantCode {
				t.Fatalf("exit %d, want %d (stderr %q)", code, tc.wantCode, stderr.String())
			}
			if tc.wantCode == exitOK && (len(*got) != 1 || (*got)[0] != tc.want) {
				t.Errorf("panel options %+v, want %+v", *got, tc.want)
			}
		})
	}
}

func TestSessionsJSON(t *testing.T) {
	env, _ := fleetFixture(t)
	var stdout, stderr bytes.Buffer
	if code := run(context.Background(), []string{"sessions", "--json"}, env, nil, &stdout, &stderr); code != exitOK {
		t.Fatalf("exit %d, stderr %q", code, stderr.String())
	}
	var got []map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("output %q: %v", stdout.String(), err)
	}
	want := []struct{ id, endpoint, label string }{
		{sessDefault.ID, "ep-default", sessDefault.Label},
		{sessWorkMain.ID, "ep-work", sessWorkMain.Label},
		{sessWorkFeat.ID, "ep-work", sessWorkFeat.Label},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d sessions, want %d: %s", len(got), len(want), stdout.String())
	}
	for i, w := range want {
		g := got[i]
		if g["id"] != w.id || g["endpoint_id"] != w.endpoint || g["label"] != w.label {
			t.Errorf("session %d = id %v endpoint %v label %v, want %+v", i, g["id"], g["endpoint_id"], g["label"], w)
		}
		for _, key := range []string{"home", "thread_id", "cwd", "mode", "tier", "route", "replies", "strip", "last_active"} {
			if _, ok := g[key]; !ok {
				t.Errorf("session %d has no %q: %v", i, key, g)
			}
		}
	}
}

func TestSessionsLines(t *testing.T) {
	env, _ := fleetFixture(t)
	var stdout, stderr bytes.Buffer
	if code := run(context.Background(), []string{"sessions"}, env, nil, &stdout, &stderr); code != exitOK {
		t.Fatalf("exit %d, stderr %q", code, stderr.String())
	}
	lines := strings.Split(strings.TrimSuffix(stdout.String(), "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("lines %q, want 3", lines)
	}
	first := strings.Join(strings.Fields(lines[0]), " ")
	if want := sessDefault.ID + " default default · notes · main on gpt-5.5 · low"; first != want {
		t.Errorf("first line %q, want %q", first, want)
	}
}

func TestSessionsPrunesDeadAndDisambiguates(t *testing.T) {
	state := t.TempDir()
	env := append(testEnv(t), "XDG_STATE_HOME="+state)
	root, err := registry.Root(func(k string) string { return map[string]string{"XDG_STATE_HOME": state}[k] })
	if err != nil {
		t.Fatal(err)
	}
	sockDir, err := os.MkdirTemp("", "crm")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(sockDir) })
	sock := filepath.Join(sockDir, "ep.sock")
	twinA := testSession(homeWork, "t-a", "work · repo · main", 1)
	twinB := testSession(homeWork, "t-b", "work · repo · main", 2)
	srv, err := control.Listen(sock, endpointHandler{
		hello:    control.Hello{EndpointID: "ep", Kind: registry.KindSession, Home: homeWork},
		sessions: []route.SessionInfo{twinA, twinB},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	for _, ep := range []registry.Endpoint{
		{ID: "ep", Kind: registry.KindSession, Home: homeWork, Socket: sock},
		{ID: "ep-dead", Kind: registry.KindSession, Home: homeWork, Socket: filepath.Join(sockDir, "dead.sock")},
		{ID: "ep-stale", Kind: registry.KindSession, Home: homeDefault, Socket: sock}, // hello names another endpoint
	} {
		if err := registry.Write(root, ep); err != nil {
			t.Fatal(err)
		}
	}

	var stdout, stderr bytes.Buffer
	if code := run(context.Background(), []string{"sessions", "--json"}, env, nil, &stdout, &stderr); code != exitOK {
		t.Fatalf("exit %d, stderr %q", code, stderr.String())
	}
	var got []route.SessionInfo
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Label == got[1].Label || !strings.HasPrefix(got[0].Label, "work · repo · main · ") {
		t.Errorf("sessions %+v, want two disambiguated labels", got)
	}
	eps, _, err := registry.List(root)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, ep := range eps {
		ids = append(ids, ep.ID)
	}
	if slices.Contains(ids, "ep-dead") || !slices.Contains(ids, "ep") {
		t.Errorf("registry after sessions = %v, want the dead entry pruned and the live one kept", ids)
	}
}

func TestSessionsEmptyRegistry(t *testing.T) {
	env := append(testEnv(t), "XDG_STATE_HOME="+t.TempDir())
	cases := []struct {
		args []string
		want string
	}{
		{[]string{"sessions", "--json"}, "[]\n"},
		{[]string{"sessions"}, ""},
	}
	for _, tc := range cases {
		var stdout, stderr bytes.Buffer
		if code := run(context.Background(), tc.args, env, nil, &stdout, &stderr); code != exitOK || stdout.String() != tc.want {
			t.Errorf("%v: exit %d, stdout %q, stderr %q; want 0 and %q", tc.args, code, stdout.String(), stderr.String(), tc.want)
		}
	}
}
