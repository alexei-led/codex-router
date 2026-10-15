package launcher

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/alexei-led/codex-router/internal/appserver"
	"github.com/alexei-led/codex-router/internal/catalog"
	"github.com/alexei-led/codex-router/internal/config"
	"github.com/alexei-led/codex-router/internal/control"
	"github.com/alexei-led/codex-router/internal/registry"
)

const evCompleted = "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"model\":\"gpt-6-luna\"," +
	"\"usage\":{\"input_tokens\":1200,\"input_tokens_details\":{\"cached_tokens\":800},\"output_tokens\":90,\"total_tokens\":1290}}}\n\n"

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

// upstream is the fake ChatGPT backend.
type upstream struct {
	*httptest.Server
	mu     sync.Mutex
	models []string
	auth   []string
}

func newUpstream(t *testing.T) *upstream {
	t.Helper()
	models, err := os.ReadFile(filepath.Join("testdata", catalog.CacheFile))
	if err != nil {
		t.Fatal(err)
	}
	u := &upstream{}
	u.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/models"):
			_, _ = w.Write(models)
		case strings.HasSuffix(r.URL.Path, "/responses"):
			var body struct {
				Model string `json:"model"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			u.mu.Lock()
			u.models = append(u.models, body.Model)
			u.auth = append(u.auth, r.Header.Get("Authorization"))
			u.mu.Unlock()
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, evCompleted)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(u.Close)
	return u
}

func (u *upstream) seen() (models, auth []string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	return slices.Clone(u.models), slices.Clone(u.auth)
}

// shortTempDir keeps unix socket paths under the 104-byte macOS limit.
func shortTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "crt")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

type harness struct {
	opts    Options
	base    string
	state   string // XDG_STATE_HOME: the registry lives under it
	records string
	stderr  *syncBuffer
	logs    *syncBuffer
	execed  [][]string
}

func newHarness(t *testing.T, extraEnv ...string) *harness {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	codexHome := t.TempDir()
	cache, err := os.ReadFile(filepath.Join("testdata", catalog.CacheFile))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(codexHome, catalog.CacheFile), cache, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cc := cfg.Classifiers[cfg.Classifier]
	cc.KeyEnv = "CODEX_ROUTER_TEST_UNSET_KEY"
	cfg.Classifiers[cfg.Classifier] = cc

	h := &harness{
		base:    shortTempDir(t),
		state:   t.TempDir(),
		records: filepath.Join(t.TempDir(), "records.jsonl"),
		stderr:  &syncBuffer{},
		logs:    &syncBuffer{},
	}
	env := append(os.Environ(),
		envFake+"=1", envRecord+"="+h.records, envExit+"=7",
		"CODEX_HOME="+codexHome, "XDG_STATE_HOME="+h.state)
	h.opts = Options{
		Config:     cfg,
		RuntimeDir: h.base,
		CodexBin:   exe,
		CodexArgs:  []string{"--search", "hello world"},
		Env:        append(env, extraEnv...),
		Stderr:     h.stderr,
		Logger:     slog.New(slog.NewJSONHandler(h.logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
		Exec: func(_ string, argv, _ []string) error {
			h.execed = append(h.execed, argv)
			return nil
		},
	}
	return h
}

func (h *harness) run(t *testing.T) int {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	code, err := Run(ctx, h.opts)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return code
}

func (h *harness) recorded(t *testing.T, kind string) []json.RawMessage {
	t.Helper()
	f, err := os.Open(h.records)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	var out []json.RawMessage
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var r record
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			t.Fatal(err)
		}
		if r.Kind == kind {
			out = append(out, r.Data)
		}
	}
	return out
}

func (h *harness) assertRuntimeRemoved(t *testing.T) {
	t.Helper()
	entries, err := os.ReadDir(h.base)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("runtime base still holds %v", entries)
	}
}

// registered lists the registry under the harness's state dir.
func (h *harness) registered(t *testing.T) []registry.Endpoint {
	t.Helper()
	root, err := registry.Root(h.opts.getenv)
	if err != nil {
		t.Fatal(err)
	}
	eps, skipped, err := registry.List(root)
	if err != nil || len(skipped) != 0 {
		t.Fatalf("registry.List: %v, skipped %v", err, skipped)
	}
	return eps
}

func (h *harness) assertUnregistered(t *testing.T) {
	t.Helper()
	if eps := h.registered(t); len(eps) != 0 {
		t.Errorf("registry still holds %+v", eps)
	}
}

func TestRunRoutesASessionAndReturnsCodexExitCode(t *testing.T) {
	cases := []struct {
		name string
		env  []string
	}{
		{"thread resumes", nil},
		{"thread without rollout", []string{envResumeFail + "=1"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			up := newUpstream(t)
			h := newHarness(t, tc.env...)
			h.opts.Upstream = up.URL + "/backend-api/codex"

			if code := h.run(t); code != 7 {
				t.Fatalf("exit code = %d, want 7; stderr: %s", code, h.stderr.String())
			}

			models, auth := up.seen()
			if want := []string{"gpt-6-luna", "gpt-6-luna", "gpt-6-luna"}; !slices.Equal(models, want) {
				t.Errorf("upstream models = %v, want %v", models, want)
			}
			for _, a := range auth {
				if a != fakeToken {
					t.Errorf("upstream Authorization not passed through")
				}
			}
			resumes := h.recorded(t, "resume")
			if len(resumes) != 1 || string(resumes[0]) != `{"threadId":"thread-1","excludeTurns":true}` {
				t.Errorf("resumes = %s, want one for thread-1", resumes)
			}
			updates := h.recorded(t, "update")
			if len(updates) != 1 || string(updates[0]) != `{"threadId":"thread-1","model":"gpt-6-luna","effort":"medium"}` {
				t.Errorf("settings writes = %s, want one pinned route", updates)
			}
			h.assertStart(t)
			h.assertRemoteArgs(t)
			h.assertRuntimeRemoved(t)
			h.assertUnregistered(t)
			if len(h.execed) != 0 {
				t.Errorf("fell back to plain codex: %v", h.execed)
			}
			logs := h.logs.String()
			if strings.Contains(logs, "SECRET-TOKEN") || strings.Contains(logs, "fix the retry test") {
				t.Error("log carries a header value or request text")
			}
			if !strings.Contains(logs, `"msg":"classifier-disabled"`) || !strings.Contains(logs, "CODEX_ROUTER_TEST_UNSET_KEY is not set") {
				t.Errorf("log does not say the classifier is off: %s", logs)
			}
		})
	}
}

func (h *harness) assertStart(t *testing.T) {
	t.Helper()
	starts := h.recorded(t, "start")
	if len(starts) != 1 {
		t.Fatalf("app-server starts = %d, want 1", len(starts))
	}
	var s struct {
		Overrides []string `json:"overrides"`
		Cwd       string   `json:"cwd"`
		Control   string   `json:"control"`
		Token     string   `json:"token"`
	}
	if err := json.Unmarshal(starts[0], &s); err != nil {
		t.Fatal(err)
	}
	wd, _ := os.Getwd()
	if s.Cwd != wd {
		t.Errorf("app-server cwd = %q, want %q", s.Cwd, wd)
	}
	if filepath.Base(s.Control) != control.SocketName || filepath.Dir(filepath.Dir(s.Control)) != h.base {
		t.Errorf("app-server %s = %q, want a control socket under %s", control.EnvSocket, s.Control, h.base)
	}
	if len(s.Overrides) != 2 || s.Overrides[0] != "model_provider=router" ||
		!strings.Contains(s.Overrides[1], `base_url="http://127.0.0.1:`) || !strings.Contains(s.Overrides[1], "requires_openai_auth=true") ||
		!strings.Contains(s.Overrides[1], `env_http_headers={"X-Codex-Router-Token"="CODEX_ROUTER_TOKEN"}`) {
		t.Errorf("app-server overrides = %q", s.Overrides)
	}
	if len(s.Token) != 32 {
		t.Errorf("app-server %s has %d chars, want 32 hex", EnvToken, len(s.Token))
	}
	if strings.Contains(strings.Join(s.Overrides, " "), s.Token) {
		t.Error("app-server argv carries the proxy token")
	}
	if strings.Contains(h.logs.String(), s.Token) {
		t.Error("log carries the proxy token")
	}
}

func (h *harness) assertRemoteArgs(t *testing.T) {
	t.Helper()
	remotes := h.recorded(t, "remote")
	if len(remotes) != 1 {
		t.Fatalf("codex --remote runs = %d, want 1", len(remotes))
	}
	var r struct {
		Args  []string `json:"args"`
		Token string   `json:"token"`
	}
	_ = json.Unmarshal(remotes[0], &r)
	if !slices.Equal(r.Args, h.opts.CodexArgs) {
		t.Errorf("codex args = %q, want %q", r.Args, h.opts.CodexArgs)
	}
	if r.Token != "" {
		t.Errorf("codex --remote got %s; only the app-server calls the proxy", EnvToken)
	}
}

func TestRunFallsBackToPlainCodex(t *testing.T) {
	cases := []struct {
		name       string
		env        []string
		probe      func(context.Context, int) error
		wantReason string
		appServer  bool
	}{
		{
			name:       "proxy probe fails",
			probe:      func(context.Context, int) error { return errors.New("probe refused") },
			wantReason: "proxy health probe: probe refused",
		},
		{
			name:       "app-server exits",
			env:        []string{envAppServerDies + "=1"},
			wantReason: errAppServerExited.Error(),
			appServer:  true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, tc.env...)
			h.opts.Probe = tc.probe

			if code := h.run(t); code != 0 {
				t.Errorf("exit code = %d, want 0", code)
			}

			want := "codex-router: " + tc.wantReason + "; running plain codex\n"
			if got := h.stderr.String(); got != want {
				t.Errorf("stderr = %q, want %q", got, want)
			}
			wantArgv := append([]string{h.opts.CodexBin}, h.opts.CodexArgs...)
			if len(h.execed) != 1 || !slices.Equal(h.execed[0], wantArgv) {
				t.Errorf("exec = %q, want %q", h.execed, wantArgv)
			}
			if started := len(h.recorded(t, "start")) > 0; started != tc.appServer {
				t.Errorf("app-server started = %v, want %v", started, tc.appServer)
			}
			if len(h.recorded(t, "remote")) != 0 {
				t.Error("codex --remote ran")
			}
			h.assertRuntimeRemoved(t)
			h.assertUnregistered(t)
		})
	}
}

func TestRunStopsWhenInterruptedDuringStartup(t *testing.T) {
	cases := []struct {
		name      string
		interrupt func(cancel context.CancelFunc)
		wantCode  int
		wantErr   error
	}{
		{"context cancelled", func(cancel context.CancelFunc) { cancel() }, 0, context.Canceled},
		{"SIGTERM", func(context.CancelFunc) { _ = syscall.Kill(os.Getpid(), syscall.SIGTERM) }, 128 + int(syscall.SIGTERM), nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, envAppServerHang+"=1")
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			type result struct {
				code int
				err  error
			}
			done := make(chan result, 1)
			go func() {
				code, err := Run(ctx, h.opts)
				done <- result{code, err}
			}()
			deadline := time.Now().Add(5 * time.Second)
			for len(h.recorded(t, "start")) == 0 {
				if time.Now().After(deadline) {
					t.Fatal("app-server never started")
				}
				time.Sleep(5 * time.Millisecond)
			}

			tc.interrupt(cancel)

			select {
			case got := <-done:
				if got.code != tc.wantCode || !errors.Is(got.err, tc.wantErr) {
					t.Errorf("Run = %d, %v; want %d, %v", got.code, got.err, tc.wantCode, tc.wantErr)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("Run did not return")
			}
			if len(h.execed) != 0 || h.stderr.String() != "" {
				t.Errorf("fell back to plain codex: exec %v, stderr %q", h.execed, h.stderr.String())
			}
			h.assertRuntimeRemoved(t)
			h.assertUnregistered(t)
		})
	}
}

func TestSessionRegister(t *testing.T) {
	file := filepath.Join(t.TempDir(), "state")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	cases := []struct {
		name       string
		state      string
		wantListed bool
	}{
		{"state dir usable", dir, true},
		{"state dir is a file", file, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var stderr, logs syncBuffer
			s := &session{
				o:   Options{Env: []string{"XDG_STATE_HOME=" + tc.state}, Stderr: &stderr},
				log: slog.New(slog.NewJSONHandler(&logs, nil)),
			}

			s.register(registry.Endpoint{ID: "abcd", Kind: registry.KindSession})

			if listed := s.reg != ""; listed != tc.wantListed {
				t.Errorf("registered = %v, want %v", listed, tc.wantListed)
			}
			warned := strings.HasPrefix(stderr.String(), "codex-router: the panel will not list this session: registry:")
			if warned == tc.wantListed {
				t.Errorf("stderr = %q, want a warning only when registration fails", stderr.String())
			}
			if logged := strings.Contains(logs.String(), `"msg":"registry-write-failed"`); logged == tc.wantListed {
				t.Errorf("failure logged = %v, want %v", logged, !tc.wantListed)
			}
		})
	}
}

func TestServeAnswersHealthAndPrintsPort(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	ports := make(chan int, 1)
	done := make(chan error, 1)
	go func() {
		done <- Serve(ctx, Options{Config: config.Default(), Env: []string{}, Logger: slog.New(slog.DiscardHandler)},
			func(p int) { ports <- p })
	}()
	var port int
	select {
	case port = <-ports:
	case err := <-done:
		t.Fatalf("Serve: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("Serve never became ready")
	}
	if err := probeHealth(context.Background(), port); err != nil {
		t.Errorf("healthz: %v", err)
	}
	cancel()
	if err := <-done; err != nil {
		t.Errorf("Serve: %v", err)
	}
}

func TestKeepServingRelistensAfterServeStops(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	serving := make(chan struct{})
	failing := func(context.Context) error { return errors.New("accept failed") }
	relistens := 0
	relisten := func() (serveFunc, error) {
		relistens++
		if relistens == 1 {
			return nil, errors.New("port busy")
		}
		return func(ctx context.Context) error {
			close(serving)
			<-ctx.Done()
			return nil
		}, nil
	}
	done := make(chan struct{})
	go func() {
		keepServing(ctx, failing, relisten, slog.New(slog.DiscardHandler))
		close(done)
	}()
	select {
	case <-serving:
	case <-time.After(5 * time.Second):
		t.Fatal("proxy never served again")
	}
	cancel()
	<-done
	if relistens != 2 {
		t.Errorf("relistens = %d, want 2", relistens)
	}
}

type fakeAPI struct {
	mu      sync.Mutex
	calls   []string
	written chan struct{}
}

func (f *fakeAPI) record(c string) {
	f.mu.Lock()
	f.calls = append(f.calls, c)
	f.mu.Unlock()
}

func (f *fakeAPI) Initialize(context.Context) error { return nil }
func (f *fakeAPI) Resume(_ context.Context, thread string) (appserver.Resumed, error) {
	f.record("resume " + thread)
	return appserver.Resumed{}, nil
}

func (f *fakeAPI) UpdateSettings(_ context.Context, thread string, s appserver.Settings) error {
	f.record("update " + thread + " " + s.Model + "/" + string(s.Effort))
	f.written <- struct{}{}
	return nil
}
func (f *fakeAPI) Updates() <-chan appserver.SettingsUpdated { return nil }
func (f *fakeAPI) Close() error                              { return nil }

func TestSettingsWriterDedupesAndRewritesAfterManualPick(t *testing.T) {
	sol := config.Route{Model: "gpt-6-sol", Effort: config.EffortMedium}
	luna := config.Route{Model: "gpt-6-luna", Effort: config.EffortLow}
	manual := config.Route{Model: "gpt-6.1-sol", Effort: config.EffortHigh}
	api := &fakeAPI{written: make(chan struct{}, 8)}
	w := NewSettingsWriter(slog.New(slog.DiscardHandler), true)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		w.Run(ctx, api)
		close(done)
	}()
	written := func() {
		t.Helper()
		select {
		case <-api.written:
		case <-time.After(2 * time.Second):
			t.Fatal("no settings write")
		}
	}

	w.Enqueue("t", sol)
	written()
	w.Enqueue("t", sol)
	w.Enqueue("t", luna)
	written()
	w.Reported("t", manual)
	w.Enqueue("t", luna)
	written()
	cancel()
	<-done

	want := []string{"resume t", "update t gpt-6-sol/medium", "update t gpt-6-luna/low", "update t gpt-6-luna/low"}
	if !slices.Equal(api.calls, want) {
		t.Errorf("calls = %q, want %q", api.calls, want)
	}
}

// gatedAPI holds each settings write until the test releases it.
type gatedAPI struct {
	*fakeAPI
	started chan struct{}
	release chan struct{}
}

func (g *gatedAPI) UpdateSettings(ctx context.Context, thread string, s appserver.Settings) error {
	g.started <- struct{}{}
	<-g.release
	return g.fakeAPI.UpdateSettings(ctx, thread, s)
}

func TestSettingsWriterKeepsARouteQueuedDuringAWrite(t *testing.T) {
	native := config.Route{Model: "gpt-6-sol", Effort: config.EffortMedium}
	routed := config.Route{Model: "gpt-6-luna", Effort: config.EffortLow}
	api := &gatedAPI{fakeAPI: &fakeAPI{written: make(chan struct{}, 8)}, started: make(chan struct{}), release: make(chan struct{})}
	w := NewSettingsWriter(slog.New(slog.DiscardHandler), true)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		w.Run(ctx, api)
		close(done)
	}()
	wait := func(c chan struct{}, what string) {
		t.Helper()
		select {
		case <-c:
		case <-time.After(2 * time.Second):
			t.Fatal("no " + what)
		}
	}
	write := func() {
		t.Helper()
		wait(api.started, "settings write")
		api.release <- struct{}{}
		wait(api.written, "settings write done")
	}

	w.Enqueue("t", native)
	write()
	w.Enqueue("t", routed)
	wait(api.started, "settings write")
	w.Enqueue("t", native)
	api.release <- struct{}{}
	wait(api.written, "settings write done")
	write()
	cancel()
	<-done

	want := []string{"resume t", "update t gpt-6-sol/medium", "update t gpt-6-luna/low", "update t gpt-6-sol/medium"}
	if !slices.Equal(api.calls, want) {
		t.Errorf("calls = %q, want %q", api.calls, want)
	}
}

func TestSettingsWriterResyncAndForget(t *testing.T) {
	sol := config.Route{Model: "gpt-6-sol", Effort: config.EffortMedium}
	luna := config.Route{Model: "gpt-6-luna", Effort: config.EffortLow}
	api := &fakeAPI{written: make(chan struct{}, 8)}
	w := NewSettingsWriter(slog.New(slog.DiscardHandler), false)
	w.Enqueue("gone", sol)
	w.Forget("gone")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		w.Run(ctx, api)
		close(done)
	}()
	written := func() {
		t.Helper()
		select {
		case <-api.written:
		case <-time.After(2 * time.Second):
			t.Fatal("no settings write")
		}
	}

	w.Enqueue("t", sol)
	written()
	w.Resync("t", luna)
	written()
	w.Resync("t", sol)
	w.Forget("t")
	w.Resync("t", luna)
	w.Enqueue("u", luna)
	written()
	cancel()
	<-done

	want := []string{"update t gpt-6-sol/medium", "update t gpt-6-sol/medium", "update u gpt-6-luna/low"}
	if !slices.Equal(api.calls, want) {
		t.Errorf("calls = %q, want %q", api.calls, want)
	}
}

func TestRuntimeBase(t *testing.T) {
	cases := []struct {
		name string
		env  map[string]string
		want string
	}{
		{"xdg runtime dir", map[string]string{"XDG_RUNTIME_DIR": "/run/user/1", "TMPDIR": "/tmp/x"}, "/run/user/1/codex-router"},
		{"tmpdir", map[string]string{"TMPDIR": "/var/folders/t"}, "/var/folders/t/codex-router"},
		{"relative values ignored", map[string]string{"XDG_RUNTIME_DIR": "run", "TMPDIR": "tmp"}, "/tmp/codex-router"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := RuntimeBase(func(k string) string { return tc.env[k] }); got != tc.want {
				t.Errorf("RuntimeBase = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestOpenLogWritesPrivateFile(t *testing.T) {
	state := t.TempDir()
	log, restore := openLog(Options{Env: []string{"XDG_STATE_HOME=" + state}, Stderr: io.Discard}, "abcd1234")
	log.Info("hello", "port", 1)
	restore()

	dir := filepath.Join(state, RuntimeDirName, logDirName)
	for path, want := range map[string]os.FileMode{dir: 0o700, filepath.Join(dir, "abcd1234.log"): 0o600} {
		fi, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != want {
			t.Errorf("%s mode = %o, want %o", path, fi.Mode().Perm(), want)
		}
	}
	b, _ := os.ReadFile(filepath.Join(dir, "abcd1234.log"))
	if !strings.Contains(string(b), `"msg":"hello"`) {
		t.Errorf("log = %s", b)
	}
}

func TestProviderOverrides(t *testing.T) {
	got := ProviderOverrides(47911)
	want := []string{
		"model_provider=router",
		`model_providers.router={name="router", base_url="http://127.0.0.1:47911/v1", wire_api="responses", requires_openai_auth=true, env_http_headers={"X-Codex-Router-Token"="CODEX_ROUTER_TOKEN"}}`,
	}
	if !slices.Equal(got, want) {
		t.Errorf("ProviderOverrides = %q, want %q", got, want)
	}
}
