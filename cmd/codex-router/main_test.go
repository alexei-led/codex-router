package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alexei-led/codex-router/internal/brain"
	"github.com/alexei-led/codex-router/internal/config"
	"github.com/alexei-led/codex-router/internal/control"
	"github.com/alexei-led/codex-router/internal/home"
	"github.com/alexei-led/codex-router/internal/link"
	"github.com/alexei-led/codex-router/internal/proxy"
	"github.com/alexei-led/codex-router/internal/registry"
	"github.com/alexei-led/codex-router/internal/responses"
	"github.com/alexei-led/codex-router/internal/route"
	"github.com/alexei-led/codex-router/internal/service"
	"github.com/alexei-led/codex-router/internal/setup"
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

const (
	edgeToken = "edge-token-under-test"
	edgeBody  = `{"model":"gpt-6-sol","reasoning":{"effort":"medium"},"input":[` +
		`{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}],"stream":true}`
	edgeTurn = `{"session_id":"s-1","thread_id":"th-1","turn_id":"tu-1","agent_name":"main"}`
)

// edgeSetup lays out a home's stage 2 state the way setup does: a private
// homes/<id> dir with a token and a free port. The state base is under
// /tmp so the brain socket path fits the macOS limit.
func edgeSetup(t *testing.T) (args []string, h home.Home, dir string, port int) {
	t.Helper()
	state, err := os.MkdirTemp("/tmp", "cre")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(state) })
	codexHome := t.TempDir()
	h, err = home.Resolve(func(k string) string {
		if k == "CODEX_HOME" {
			return codexHome
		}
		return ""
	})
	if err != nil {
		t.Fatal(err)
	}
	dir = brain.HomeDir(filepath.Join(state, "codex-router"), h.ID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port = ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	for name, v := range map[string]string{setup.TokenFile: edgeToken + "\n", setup.PortFile: strconv.Itoa(port) + "\n"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(v), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return []string{"edge", "--home", codexHome, "--state-dir", state}, h, dir, port
}

// syncBuffer is a bytes.Buffer the edge logs into while the test reads it.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// fakeBackend records what reaches upstream and answers one completed
// response.
type fakeBackend struct {
	mu     sync.Mutex
	bodies []string
	tokens []string
}

func (b *fakeBackend) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	b.mu.Lock()
	b.bodies = append(b.bodies, string(body))
	b.tokens = append(b.tokens, r.Header.Get(proxy.HeaderToken))
	b.mu.Unlock()
	w.Header().Set("Content-Type", "text/event-stream")
	_, _ = io.WriteString(w, "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"model\":\"m\",\"usage\":{}}}\n\n")
}

func (b *fakeBackend) seen() (bodies, tokens []string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return slices.Clone(b.bodies), slices.Clone(b.tokens)
}

// rewriteTo is a brain that routes every step to one model.
type rewriteTo string

func (m rewriteTo) Decide(context.Context, *responses.Request, responses.Meta) (responses.Rewrite, bool) {
	return responses.Rewrite{Model: string(m)}, true
}

func (rewriteTo) Observe(responses.Meta, string, responses.Usage, int) {}

func edgePost(t *testing.T, base, token string) int {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, base+proxy.PathResponses, strings.NewReader(edgeBody))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set(responses.HeaderTurnMetadata, edgeTurn)
	if token != "" {
		req.Header.Set(proxy.HeaderToken, token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	return resp.StatusCode
}

// healthz returns the edge's /healthz body, or "" while it does not answer.
func healthz(base string) string {
	resp, err := http.Get(base + proxy.PathHealth)
	if err != nil {
		return ""
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return string(b)
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestEdgeForwardsWithBrainDownAndRoutesWithBrainUp(t *testing.T) {
	backend := &fakeBackend{}
	up := httptest.NewServer(backend)
	defer up.Close()
	edgeUpstream = up.URL + "/backend-api/codex"
	t.Cleanup(func() { edgeUpstream = "" })

	args, h, dir, port := edgeSetup(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var stderr syncBuffer
	done := make(chan int, 1)
	go func() { done <- run(ctx, args, testEnv(t), nil, io.Discard, &stderr) }()

	base := "http://127.0.0.1:" + strconv.Itoa(port)
	waitFor(t, "edge up", func() bool { return healthz(base) != "" })
	if got := healthz(base); got != "ok\nbrain: down\n" {
		t.Errorf("healthz with no brain = %q", got)
	}
	if !strings.Contains(stderr.String(), "edge-self-bind") {
		t.Errorf("stderr %q, want the self-bind warning", stderr.String())
	}

	for _, token := range []string{"", "wrong"} {
		if code := edgePost(t, base, token); code != http.StatusUnauthorized {
			t.Errorf("token %q: status %d, want 401", token, code)
		}
	}
	if bodies, _ := backend.seen(); len(bodies) != 0 {
		t.Fatalf("requests without the token reached upstream: %d", len(bodies))
	}

	if code := edgePost(t, base, edgeToken); code != http.StatusOK {
		t.Fatalf("brain down: status %d, want 200", code)
	}
	bodies, tokens := backend.seen()
	if len(bodies) != 1 || bodies[0] != edgeBody || tokens[0] != "" {
		t.Fatalf("brain down: upstream got %q (token %q), want the original body and no token", bodies, tokens)
	}

	brain, err := link.Listen(filepath.Join(dir, link.SocketName), h.ID, rewriteTo("gpt-6-luna"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = brain.Close() }()
	waitFor(t, "brain up", func() bool { return healthz(base) == "ok\nbrain: up\n" })
	if code := edgePost(t, base, edgeToken); code != http.StatusOK {
		t.Fatalf("brain up: status %d, want 200", code)
	}
	if bodies, _ = backend.seen(); len(bodies) != 2 || !strings.Contains(bodies[1], `"model":"gpt-6-luna"`) {
		t.Errorf("brain up: upstream got %q, want the brain's model", bodies)
	}

	cancel()
	select {
	case code := <-done:
		if code != exitOK {
			t.Errorf("exit %d, stderr %q", code, stderr.String())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("edge did not stop")
	}
	if strings.Contains(stderr.String(), edgeToken) {
		t.Error("the token reached the log")
	}
}

func TestEdgeRefusesToStart(t *testing.T) {
	cases := []struct {
		name     string
		edit     func(t *testing.T, args []string, dir string) []string
		wantCode int
		want     string
	}{
		{"not set up", func(t *testing.T, args []string, dir string) []string {
			if err := os.RemoveAll(dir); err != nil {
				t.Fatal(err)
			}
			return args
		}, exitError, "is not set up"},
		{"no token", func(t *testing.T, args []string, dir string) []string {
			if err := os.Remove(filepath.Join(dir, setup.TokenFile)); err != nil {
				t.Fatal(err)
			}
			return args
		}, exitError, "edge: token"},
		{"empty token", func(t *testing.T, args []string, dir string) []string {
			if err := os.WriteFile(filepath.Join(dir, setup.TokenFile), []byte("\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			return args
		}, exitError, "is empty"},
		{"state dir open to others", func(t *testing.T, args []string, dir string) []string {
			if err := os.Chmod(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			return args
		}, exitError, "must not be accessible"},
		{"homes dir open to group", func(t *testing.T, args []string, dir string) []string {
			if err := os.Chmod(filepath.Dir(dir), 0o750); err != nil {
				t.Fatal(err)
			}
			return args
		}, exitError, "must not be accessible"},
		{"bad port", func(t *testing.T, args []string, dir string) []string {
			if err := os.WriteFile(filepath.Join(dir, setup.PortFile), []byte("http"), 0o600); err != nil {
				t.Fatal(err)
			}
			return args
		}, exitError, "does not hold a port"},
		{"relative home", func(_ *testing.T, args []string, _ string) []string {
			return append(args, "--home", "rel")
		}, exitUsage, "not an absolute path"},
		{"relative state dir", func(_ *testing.T, args []string, _ string) []string {
			return append(args, "--state-dir", "rel")
		}, exitUsage, "not an absolute path"},
		{"stray argument", func(_ *testing.T, args []string, _ string) []string {
			return append(args, "extra")
		}, exitUsage, `unexpected argument "extra"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			args, _, dir, _ := edgeSetup(t)
			args = tc.edit(t, args, dir)
			var stderr bytes.Buffer
			code := run(context.Background(), args, testEnv(t), nil, io.Discard, &stderr)
			if code != tc.wantCode || !strings.Contains(stderr.String(), tc.want) {
				t.Errorf("exit %d, stderr %q; want %d and %q", code, stderr.String(), tc.wantCode, tc.want)
			}
		})
	}
}

func TestBrainServesTheEdgeAndListsItself(t *testing.T) {
	backend := &fakeBackend{}
	up := httptest.NewServer(backend)
	defer up.Close()
	edgeUpstream = up.URL + "/backend-api/codex"
	t.Cleanup(func() { edgeUpstream = "" })

	edgeArgs, h, _, port := edgeSetup(t)
	brainArgs := append([]string{"brain"}, edgeArgs[1:]...)
	state := edgeArgs[len(edgeArgs)-1]
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var brainErr, edgeErr syncBuffer
	brainDone, edgeDone := make(chan int, 1), make(chan int, 1)
	go func() { brainDone <- run(ctx, brainArgs, testEnv(t), nil, io.Discard, &brainErr) }()
	go func() { edgeDone <- run(ctx, edgeArgs, testEnv(t), nil, io.Discard, &edgeErr) }()

	base := "http://127.0.0.1:" + strconv.Itoa(port)
	waitFor(t, "brain up behind the edge", func() bool { return healthz(base) == "ok\nbrain: up\n" })
	if code := edgePost(t, base, edgeToken); code != http.StatusOK {
		t.Fatalf("status %d, want 200", code)
	}

	env := append(testEnv(t), "XDG_STATE_HOME="+state)
	root, err := registry.Root(func(k string) string {
		if k == "XDG_STATE_HOME" {
			return state
		}
		return ""
	})
	if err != nil {
		t.Fatal(err)
	}
	eps, _, err := registry.List(root)
	if err != nil || len(eps) != 1 || eps[0].Kind != registry.KindBrain || eps[0].Home.ID != h.ID {
		t.Fatalf("registry = %+v, %v; want the brain of home %s", eps, err, h.ID)
	}
	var stdout, stderr bytes.Buffer
	if code := run(context.Background(), []string{"sessions", "--json"}, env, nil, &stdout, &stderr); code != exitOK {
		t.Errorf("sessions: exit %d, stderr %q", code, stderr.String())
	}
	if eps, _, _ := registry.List(root); len(eps) != 1 {
		t.Errorf("sessions pruned the live brain: %+v", eps)
	}

	cancel()
	for name, done := range map[string]chan int{"brain": brainDone, "edge": edgeDone} {
		select {
		case code := <-done:
			if code != exitOK {
				t.Errorf("%s exit %d, stderr %q / %q", name, code, brainErr.String(), edgeErr.String())
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("%s did not stop", name)
		}
	}
	if !strings.Contains(brainErr.String(), "daemon-unreachable") {
		t.Errorf("brain log %q, want the daemon warning", brainErr.String())
	}
	if eps, _, _ := registry.List(root); len(eps) != 0 {
		t.Errorf("registry after the brain stopped = %+v, want empty", eps)
	}
}

func TestEdgeDialsTheRecordedBrainSocket(t *testing.T) {
	args, h, dir, port := edgeSetup(t)
	sock := filepath.Join(filepath.Dir(filepath.Dir(dir)), "elsewhere.sock")
	if err := os.WriteFile(filepath.Join(dir, "brain.path"), []byte(sock+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	brainSrv, err := link.Listen(sock, h.ID, rewriteTo("gpt-6-luna"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = brainSrv.Close() }()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan int, 1)
	go func() { done <- run(ctx, args, testEnv(t), nil, io.Discard, io.Discard) }()
	base := "http://127.0.0.1:" + strconv.Itoa(port)
	waitFor(t, "edge reaches the brain at the recorded path", func() bool { return healthz(base) == "ok\nbrain: up\n" })
	cancel()
	<-done
}

func TestBrainRefusesToStart(t *testing.T) {
	bad := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(bad, []byte(`{"bogus": 1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name     string
		args     []string
		wantCode int
		want     string
	}{
		{"relative home", []string{"brain", "--home", "rel"}, exitUsage, "not an absolute path"},
		{"relative state dir", []string{"brain", "--state-dir", "rel"}, exitUsage, "not an absolute path"},
		{"stray argument", []string{"brain", "extra"}, exitUsage, `brain: unexpected argument "extra"`},
		{"bad config", []string{"--config", bad, "brain"}, exitError, "bogus"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var stderr bytes.Buffer
			code := run(context.Background(), tc.args, testEnv(t), nil, io.Discard, &stderr)
			if code != tc.wantCode || !strings.Contains(stderr.String(), tc.want) {
				t.Errorf("exit %d, stderr %q; want %d and %q", code, stderr.String(), tc.wantCode, tc.want)
			}
		})
	}
}

// fakeServices replaces launchctl, systemctl and the binary path; every
// service command succeeds with no output.
func fakeServices(t *testing.T) func() []string {
	t.Helper()
	var mu sync.Mutex
	var calls []string
	serviceRunner = func(_ context.Context, name string, args ...string) (string, error) {
		mu.Lock()
		defer mu.Unlock()
		calls = append(calls, name+" "+strings.Join(args, " "))
		return "", nil
	}
	executable = func() (string, error) { return "/usr/local/bin/codex-router", nil }
	t.Cleanup(func() { serviceRunner, executable = service.Exec, os.Executable })
	return func() []string {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(calls)
	}
}

func TestSetupThenEdgeThenUndo(t *testing.T) {
	calls := fakeServices(t)
	backend := &fakeBackend{}
	up := httptest.NewServer(backend)
	defer up.Close()
	edgeUpstream = up.URL + "/backend-api/codex"
	t.Cleanup(func() { edgeUpstream = "" })

	state, err := os.MkdirTemp("/tmp", "cre")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(state) })
	codexHome := t.TempDir()
	const userConfig = "# mine\nmodel = \"gpt-5.5\"\n"
	cfgPath := filepath.Join(codexHome, "config.toml")
	if err := os.WriteFile(cfgPath, []byte(userConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	env := testEnv(t)
	homeArgs := []string{"--home", codexHome, "--state-dir", state}
	cmd := func(args ...string) (int, string, string) {
		var stdout, stderr bytes.Buffer
		code := run(context.Background(), args, env, strings.NewReader(""), &stdout, &stderr)
		return code, stdout.String(), stderr.String()
	}

	code, out, errOut := cmd(append([]string{"setup"}, homeArgs...)...)
	if code != exitOK {
		t.Fatalf("setup: exit %d, stderr %q", code, errOut)
	}
	if !strings.HasPrefix(out, "Codex home ") || !strings.Contains(out, "<redacted>") {
		t.Errorf("setup output wants the home first and a redacted diff:\n%s", out)
	}
	if len(calls()) == 0 {
		t.Error("setup loaded no units")
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
	dir := brain.HomeDir(filepath.Join(state, "codex-router"), h.ID)
	token, err := os.ReadFile(filepath.Join(dir, setup.TokenFile))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, strings.TrimSpace(string(token))) {
		t.Error("setup printed the token")
	}
	port, err := readPort(dir)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan int, 1)
	var edgeErr syncBuffer
	go func() { done <- run(ctx, append([]string{"edge"}, homeArgs...), env, nil, io.Discard, &edgeErr) }()
	base := "http://127.0.0.1:" + strconv.Itoa(port)
	waitFor(t, "edge up on the stored port", func() bool { return healthz(base) != "" })
	if code := edgePost(t, base, strings.TrimSpace(string(token))); code != http.StatusOK {
		t.Errorf("edge with setup's token: status %d, want 200", code)
	}
	cancel()
	<-done

	code, out, _ = cmd("service", "status", "--home", codexHome)
	if code != exitOK || !strings.Contains(out, "edge") || !strings.Contains(out, "brain") || strings.Contains(out, "not installed") {
		t.Errorf("service status: exit %d\n%s", code, out)
	}
	if code, out, _ = cmd("service", "ensure", "--home", codexHome); code != exitOK || out != "" {
		t.Errorf("service ensure on a set-up home: exit %d, stdout %q", code, out)
	}

	if code, _, errOut = cmd(append([]string{"setup", "--undo"}, homeArgs...)...); code != exitOK {
		t.Fatalf("setup --undo: exit %d, stderr %q", code, errOut)
	}
	if got, _ := os.ReadFile(cfgPath); string(got) != userConfig {
		t.Errorf("config.toml after undo = %q, want the original", got)
	}
	code, out, _ = cmd("service", "ensure", "--home", codexHome)
	if code != exitOK || !strings.Contains(out, "codex-router setup --home "+h.Path) || strings.Count(out, "\n") != 1 {
		t.Errorf("service ensure on a home not set up: exit %d, want one hint line, got %q", code, out)
	}
}

func TestSetupAndServiceUsage(t *testing.T) {
	fakeServices(t)
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"purge without undo", []string{"setup", "--purge"}, "--purge needs --undo"},
		{"relative home", []string{"setup", "--home", "rel"}, "not an absolute path"},
		{"service without a subcommand", []string{"service"}, "want ensure or status"},
		{"service unknown subcommand", []string{"service", "restart"}, "want ensure or status"},
		{"service stray argument", []string{"service", "status", "extra"}, `unexpected argument "extra"`},
		{"service takes no state dir", []string{"service", "ensure", "--state-dir", "/x"}, "flag provided but not defined"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var stderr bytes.Buffer
			code := run(context.Background(), tc.args, testEnv(t), strings.NewReader(""), io.Discard, &stderr)
			if code != exitUsage || !strings.Contains(stderr.String(), tc.want) {
				t.Errorf("exit %d, stderr %q; want %d and %q", code, stderr.String(), exitUsage, tc.want)
			}
		})
	}
}

func TestSetupRefusalExitsOne(t *testing.T) {
	fakeServices(t)
	codexHome := t.TempDir()
	if err := os.WriteFile(filepath.Join(codexHome, "config.toml"), []byte("model_provider = \"openai\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"setup", "--home", codexHome, "--state-dir", t.TempDir()}, testEnv(t), strings.NewReader(""), &stdout, &stderr)
	if code != exitError || !strings.Contains(stderr.String(), "already sets model_provider") || !strings.HasPrefix(stdout.String(), "Codex home ") {
		t.Errorf("exit %d, stdout %q, stderr %q", code, stdout.String(), stderr.String())
	}
}

// recordingHandler serves fixed sessions and records the commands it gets.
type recordingHandler struct {
	endpointHandler
	mu  *sync.Mutex
	got *[]route.Command
}

func (h recordingHandler) Apply(c route.Command) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	*h.got = append(*h.got, c)
	return nil
}

// ctlFixture serves, for a real Codex home, sessions in repo, in repo/sub
// and two in twin, plus a session of another home in repo/sub/deep. It
// returns the env, the home path, the project dir and a func that returns
// the commands the home's endpoint got.
func ctlFixture(t *testing.T) (env []string, codexHome, projects string, applied func() []route.Command) {
	t.Helper()
	env, root, sockDir := testRegistry(t)
	codexHome, projects = t.TempDir(), t.TempDir()
	for _, d := range []string{"repo/sub/deep", "twin"} {
		if err := os.MkdirAll(filepath.Join(projects, d), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	h, err := home.Resolve(func(k string) string { return map[string]string{"CODEX_HOME": codexHome}[k] })
	if err != nil {
		t.Fatal(err)
	}
	inDir := func(s route.SessionInfo, dir string) route.SessionInfo {
		s.Cwd = filepath.Join(projects, dir)
		return s
	}
	paused := testSession(h, "t-sub", "work · repo · sub", 2)
	paused.Mode, paused.Attention = route.ModePaused, route.AttentionPaused
	sessions := []route.SessionInfo{
		inDir(testSession(h, "t-repo", "work · repo · main", 1), "repo"),
		inDir(paused, "repo/sub"),
		inDir(testSession(h, "t-twin1", "work · twin · a", 3), "twin"),
		inDir(testSession(h, "t-twin2", "work · twin · b", 4), "twin"),
	}
	var mu sync.Mutex
	var got []route.Command
	sock := filepath.Join(sockDir, "ep.sock")
	srv, err := control.Listen(sock, recordingHandler{
		endpointHandler: endpointHandler{hello: control.Hello{EndpointID: "ep", Kind: registry.KindBrain, Home: h}, sessions: sessions},
		mu:              &mu, got: &got,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	if err := registry.Write(root, registry.Endpoint{ID: "ep", Kind: registry.KindBrain, Home: h, Socket: sock}); err != nil {
		t.Fatal(err)
	}
	serveEndpoint(t, root, sockDir, "ep-other", homeWork, inDir(testSession(homeWork, "t-other", "other · repo · deep", 5), "repo/sub/deep"))
	return env, codexHome, projects, func() []route.Command {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(got)
	}
}

func TestCtlResolvesTheShellSession(t *testing.T) {
	env, codexHome, projects, _ := ctlFixture(t)
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(filepath.Join(projects, "repo"), link); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name       string
		cwd        string
		thread     string
		wantCode   int
		wantStdout []string
		wantStderr []string
	}{
		{"cwd of a session", filepath.Join(projects, "repo"), "", exitOK, []string{"label    work · repo · main\n", "mode     on\n", "route    gpt-5.5 · low\n"}, nil},
		{"deepest cwd wins", filepath.Join(projects, "repo/sub"), "", exitOK, []string{"work · repo · sub", "attention  paused\n"}, nil},
		{"below a session's cwd, other home deeper", filepath.Join(projects, "repo/sub/deep"), "", exitOK, []string{"work · repo · sub"}, nil},
		{"cwd through a symlink", link, "", exitOK, []string{"work · repo · main"}, nil},
		{"thread id beats cwd", filepath.Join(projects, "twin"), "t-repo", exitOK, []string{"work · repo · main"}, nil},
		{"unknown thread id falls back to cwd", filepath.Join(projects, "repo"), "t-gone", exitOK, []string{"work · repo · main"}, nil},
		{"other home's thread id is not used", filepath.Join(projects, "repo"), "t-other", exitOK, []string{"work · repo · main"}, nil},
		{"two sessions share the cwd", filepath.Join(projects, "twin"), "", exitUsage, nil, []string{"2 sessions", "work · twin · a", "work · twin · b"}},
		{"no session works there", projects, "", exitError, nil, []string{"no running session of Codex home"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := append(slices.Clone(env), envThread+"="+tc.thread)
			var stdout, stderr bytes.Buffer
			code := run(context.Background(), []string{"ctl", "status", "--home", codexHome, "--cwd", tc.cwd}, env, nil, &stdout, &stderr)
			if code != tc.wantCode {
				t.Fatalf("exit %d, want %d (stdout %q, stderr %q)", code, tc.wantCode, stdout.String(), stderr.String())
			}
			for _, w := range tc.wantStdout {
				if !strings.Contains(stdout.String(), w) {
					t.Errorf("stdout %q, want it to contain %q", stdout.String(), w)
				}
			}
			for _, w := range tc.wantStderr {
				if !strings.Contains(stderr.String(), w) {
					t.Errorf("stderr %q, want it to contain %q", stderr.String(), w)
				}
			}
		})
	}
}

func TestCtlSendsSessionCommands(t *testing.T) {
	cases := []struct {
		name       string
		args       []string
		wantStdout string
		want       route.Command
	}{
		{"pause", []string{"pause"}, "paused routing of work · repo · sub", route.Command{Op: route.OpPause, Session: "t-sub", Scope: route.ScopeSession}},
		{"resume", []string{"resume"}, "resumed routing of work · repo · sub", route.Command{Op: route.OpResume, Session: "t-sub", Scope: route.ScopeSession}},
		{"pin", []string{"pin", "--tier", "high"}, "pinned the next turn of work · repo · sub to high", route.Command{Op: route.OpPin, Session: "t-sub", Scope: route.ScopeSession, Tier: config.TierHigh}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env, codexHome, projects, applied := ctlFixture(t)
			args := append([]string{"ctl"}, tc.args...)
			args = append(args, "--home", codexHome, "--cwd", filepath.Join(projects, "repo/sub"))
			var stdout, stderr bytes.Buffer
			if code := run(context.Background(), args, env, nil, &stdout, &stderr); code != exitOK {
				t.Fatalf("exit %d, stderr %q", code, stderr.String())
			}
			if !strings.Contains(stdout.String(), tc.wantStdout) {
				t.Errorf("stdout %q, want %q", stdout.String(), tc.wantStdout)
			}
			got := slices.DeleteFunc(applied(), func(c route.Command) bool { return c.Op == control.OpSubscribe })
			if len(got) != 1 {
				t.Fatalf("endpoint got %+v, want one command", got)
			}
			got[0].ID = ""
			if got[0] != tc.want {
				t.Errorf("endpoint got %+v, want %+v", got[0], tc.want)
			}
		})
	}
}

func TestCtlUsage(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"no subcommand", []string{"ctl"}, "want status, pause, resume or pin"},
		{"unknown subcommand", []string{"ctl", "preset"}, "want status, pause, resume or pin"},
		{"pin without a tier", []string{"ctl", "pin"}, `--tier "" is not`},
		{"pin with a bad tier", []string{"ctl", "pin", "--tier", "huge"}, `--tier "huge" is not`},
		{"tier on status", []string{"ctl", "status", "--tier", "high"}, "flag provided but not defined"},
		{"stray argument", []string{"ctl", "pause", "now"}, `unexpected argument "now"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var stderr bytes.Buffer
			code := run(context.Background(), tc.args, testEnv(t), nil, io.Discard, &stderr)
			if code != exitUsage || !strings.Contains(stderr.String(), tc.want) {
				t.Errorf("exit %d, stderr %q; want %d and %q", code, stderr.String(), exitUsage, tc.want)
			}
		})
	}
}
