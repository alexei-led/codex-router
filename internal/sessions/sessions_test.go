package sessions

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/alexei-led/codex-router/internal/config"
	"github.com/alexei-led/codex-router/internal/control"
	"github.com/alexei-led/codex-router/internal/home"
	"github.com/alexei-led/codex-router/internal/registry"
	"github.com/alexei-led/codex-router/internal/route"
)

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

func listed(s route.SessionInfo) Session { return Session{SessionInfo: s, base: s.Label} }

func listedTwins(thread ...string) []Session {
	const shared = "work · repo · main"
	var sessions []route.SessionInfo
	var ids, labels []string
	for i, th := range thread {
		s := testSession(homeWork, th, shared, i+1)
		sessions = append(sessions, s)
		ids, labels = append(ids, s.ID), append(labels, shared)
	}
	var out []Session
	for i, label := range home.Disambiguate(labels, ids) {
		l := listed(sessions[i])
		l.Label = label
		out = append(out, l)
	}
	return out
}

func TestResolve(t *testing.T) {
	plain := []Session{listed(sessWorkMain), listed(sessWorkFeat), listed(sessDefault)}
	twins := listedTwins("t-a", "t-b")
	cases := []struct {
		name     string
		sessions []Session
		query    string
		want     []string
	}{
		{"exact id", plain, sessDefault.ID, []string{sessDefault.ID}},
		{"exact thread id", plain, "019a-t1", []string{sessWorkMain.ID}},
		{"unique id prefix", plain, homeDefault.ID[:6], []string{sessDefault.ID}},
		{"unique thread id prefix", plain, "019b", []string{sessWorkFeat.ID}},
		{"exact label", plain, "work · codex-router · main", []string{sessWorkMain.ID}},
		{"label suffix", plain, "codex-router · feat-x", []string{sessWorkFeat.ID}},
		{"ambiguous thread prefix", plain, "019a", []string{sessWorkMain.ID, sessDefault.ID}},
		{"ambiguous label suffix", plain, "main", []string{sessWorkMain.ID, sessDefault.ID}},
		{"ambiguous home prefix", plain, homeWork.ID, []string{sessWorkMain.ID, sessWorkFeat.ID}},
		{"home name", plain, "default", []string{sessDefault.ID}},
		{"ambiguous home name", plain, "work", []string{sessWorkMain.ID, sessWorkFeat.ID}},
		{"home and repo", plain, "default · notes", []string{sessDefault.ID}},
		{"suffix rule wins over prefix", plain, "codex-router · main", []string{sessWorkMain.ID}},
		{"prefix inside a word", plain, "wor", nil},
		{"suffix inside a word", plain, "ain", nil},
		{"not found", plain, "nope", nil},
		{"shared full label", twins, "work · repo · main", []string{twins[0].ID, twins[1].ID}},
		{"shared branch suffix", twins, "main", []string{twins[0].ID, twins[1].ID}},
		{"shared repo and branch suffix", twins, "repo · main", []string{twins[0].ID, twins[1].ID}},
		{"disambiguated label", twins, twins[1].Label, []string{twins[1].ID}},
		{"hex suffix", twins, strings.TrimPrefix(twins[0].Label, twins[0].base+home.LabelSep), []string{twins[0].ID}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got []string
			for _, s := range Resolve(tc.sessions, tc.query) {
				got = append(got, s.ID)
			}
			slices.Sort(got)
			want := slices.Sorted(slices.Values(tc.want))
			if !slices.Equal(got, want) {
				t.Errorf("Resolve(%q) = %v, want %v", tc.query, got, want)
			}
		})
	}
}

func TestForShell(t *testing.T) {
	projects := t.TempDir()
	for _, d := range []string{"repo/sub/deep", "twin"} {
		if err := os.MkdirAll(filepath.Join(projects, d), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(filepath.Join(projects, "repo"), link); err != nil {
		t.Fatal(err)
	}
	in := func(thread, dir string) Session {
		return Session{SessionInfo: route.SessionInfo{ThreadID: thread, Cwd: filepath.Join(projects, dir)}}
	}
	sessions := []Session{in("t-repo", "repo"), in("t-sub", "repo/sub"), in("t-twin1", "twin"), in("t-twin2", "twin")}
	cases := []struct {
		name   string
		dir    string
		thread string
		want   []string
	}{
		{"cwd of a session", filepath.Join(projects, "repo"), "", []string{"t-repo"}},
		{"deepest cwd wins", filepath.Join(projects, "repo/sub"), "", []string{"t-sub"}},
		{"below a session's cwd", filepath.Join(projects, "repo/sub/deep"), "", []string{"t-sub"}},
		{"through a symlink", link, "", []string{"t-repo"}},
		{"thread id beats cwd", filepath.Join(projects, "twin"), "t-repo", []string{"t-repo"}},
		{"unknown thread id falls back to cwd", filepath.Join(projects, "repo"), "t-gone", []string{"t-repo"}},
		{"sessions sharing the cwd", filepath.Join(projects, "twin"), "", []string{"t-twin1", "t-twin2"}},
		{"no session works there", projects, "", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got []string
			for _, s := range ForShell(sessions, tc.thread, tc.dir) {
				got = append(got, s.ThreadID)
			}
			if !slices.Equal(got, tc.want) {
				t.Errorf("ForShell(thread %q, %s) = %v, want %v", tc.thread, tc.dir, got, tc.want)
			}
		})
	}
}

func TestBlocked(t *testing.T) {
	dialErr := func(errno syscall.Errno) error {
		return &net.OpError{Op: "dial", Net: "unix", Err: os.NewSyscallError("connect", errno)}
	}
	cases := []struct {
		name    string
		home    home.Home
		dialErr error
		blocked bool
	}{
		{"sandbox refuses with EPERM", homeWork, dialErr(syscall.EPERM), true},
		{"sandbox refuses with EACCES", homeWork, dialErr(syscall.EACCES), true},
		{"router gone", homeWork, dialErr(syscall.ENOENT), false},
		{"another home's router refuses", homeDefault, dialErr(syscall.EPERM), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			state := t.TempDir()
			root, err := registry.Root(func(k string) string { return map[string]string{"XDG_STATE_HOME": state}[k] })
			if err != nil {
				t.Fatal(err)
			}
			ep := registry.Endpoint{ID: "brain-x", Kind: registry.KindBrain, PID: os.Getpid(), Home: tc.home, Socket: filepath.Join(state, "c.sock")}
			if err := registry.Write(root, ep); err != nil {
				t.Fatal(err)
			}
			orig := dialSocket
			dialSocket = func(string) error { return tc.dialErr }
			t.Cleanup(func() { dialSocket = orig })

			if err := Blocked(root, homeWork.ID); (err != nil) != tc.blocked {
				t.Errorf("Blocked = %v, want blocked %v", err, tc.blocked)
			}
		})
	}
}

type endpointHandler struct {
	hello    control.Hello
	sessions []route.SessionInfo
	err      error
	mu       *sync.Mutex
	got      *[]route.Command
}

func (h endpointHandler) Apply(c route.Command) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	*h.got = append(*h.got, c)
	return h.err
}

func (h endpointHandler) Sessions() []route.SessionInfo     { return h.sessions }
func (h endpointHandler) SnapshotFor(string) route.Snapshot { return route.Snapshot{} }
func (h endpointHandler) Hello() control.Hello              { return h.hello }

func testRegistry(t *testing.T) (root, sockDir string) {
	t.Helper()
	state := t.TempDir()
	root, err := registry.Root(func(k string) string { return map[string]string{"XDG_STATE_HOME": state}[k] })
	if err != nil {
		t.Fatal(err)
	}
	sockDir, err = os.MkdirTemp("", "crs")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(sockDir) })
	return root, sockDir
}

func listen(t *testing.T, sockDir, id string, h home.Home, err error, sessions ...route.SessionInfo) (sock string, applied func() []route.Command) {
	t.Helper()
	sock = filepath.Join(sockDir, id+".sock")
	var mu sync.Mutex
	var got []route.Command
	srv, lerr := control.Listen(sock, endpointHandler{
		hello:    control.Hello{EndpointID: id, Kind: registry.KindSession, Home: h},
		sessions: sessions, err: err, mu: &mu, got: &got,
	})
	if lerr != nil {
		t.Fatal(lerr)
	}
	t.Cleanup(func() { _ = srv.Close() })
	return sock, func() []route.Command {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(got)
	}
}

func register(t *testing.T, root string, eps ...registry.Endpoint) {
	t.Helper()
	for _, ep := range eps {
		if err := registry.Write(root, ep); err != nil {
			t.Fatal(err)
		}
	}
}

func serveEndpoint(t *testing.T, root, sockDir, id string, h home.Home, sessions ...route.SessionInfo) string {
	t.Helper()
	sock, _ := listen(t, sockDir, id, h, nil, sessions...)
	register(t, root, registry.Endpoint{ID: id, Kind: registry.KindSession, Home: h, Socket: sock})
	return sock
}

func entry(endpoint string, s route.SessionInfo) string { return endpoint + " " + s.ID + " " + s.Label }

func TestList(t *testing.T) {
	twinA := testSession(homeWork, "t-a", "work · repo · main", 1)
	twinB := testSession(homeWork, "t-b", "work · repo · main", 2)
	twinLabels := home.Disambiguate([]string{twinB.Label, twinA.Label}, []string{twinB.ID, twinA.ID})
	cases := []struct {
		name         string
		setup        func(t *testing.T, root, sockDir string) (socket string)
		want         []string
		wantRegistry []string
	}{
		{"two homes in panel order", func(t *testing.T, root, sockDir string) string {
			serveEndpoint(t, root, sockDir, "ep-work", homeWork, sessWorkFeat, sessWorkMain)
			serveEndpoint(t, root, sockDir, "ep-default", homeDefault, sessDefault)
			return ""
		}, []string{entry("ep-default", sessDefault), entry("ep-work", sessWorkMain), entry("ep-work", sessWorkFeat)},
			[]string{"ep-default", "ep-work"}},
		{"dead entry pruned and skipped", func(t *testing.T, root, sockDir string) string {
			serveEndpoint(t, root, sockDir, "ep-work", homeWork, sessWorkMain)
			register(t, root, registry.Endpoint{ID: "ep-dead", Kind: registry.KindSession, Home: homeDefault, Socket: filepath.Join(sockDir, "dead.sock")})
			return ""
		}, []string{entry("ep-work", sessWorkMain)}, []string{"ep-work"}},
		{"hello of another endpoint or home ignored", func(t *testing.T, root, sockDir string) string {
			serveEndpoint(t, root, sockDir, "ep-work", homeWork, sessWorkMain)
			sock, _ := listen(t, sockDir, "ep-real", homeDefault, nil, sessDefault)
			register(t, root,
				registry.Endpoint{ID: "ep-other", Kind: registry.KindSession, Home: homeDefault, Socket: sock},
				registry.Endpoint{ID: "ep-real", Kind: registry.KindSession, Home: homeWork, Socket: sock})
			return ""
		}, []string{entry("ep-work", sessWorkMain)}, []string{"ep-other", "ep-real", "ep-work"}},
		{"socket bypasses the registry", func(t *testing.T, root, sockDir string) string {
			serveEndpoint(t, root, sockDir, "ep-work", homeWork, sessWorkMain)
			sock, _ := listen(t, sockDir, "ep-default", homeDefault, nil, sessDefault)
			return sock
		}, []string{entry("ep-default", sessDefault)}, []string{"ep-work"}},
		{"shared labels disambiguated", func(t *testing.T, root, sockDir string) string {
			serveEndpoint(t, root, sockDir, "ep-work", homeWork, twinA, twinB)
			return ""
		}, []string{"ep-work " + twinB.ID + " " + twinLabels[0], "ep-work " + twinA.ID + " " + twinLabels[1]},
			[]string{"ep-work"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root, sockDir := testRegistry(t)
			socket := tc.setup(t, root, sockDir)
			all, err := List(socket, root)
			if err != nil {
				t.Fatal(err)
			}
			var got []string
			for _, s := range all {
				got = append(got, s.Endpoint+" "+s.ID+" "+s.Label)
			}
			if !slices.Equal(got, tc.want) {
				t.Errorf("List = %q, want %q", got, tc.want)
			}
			eps, _, err := registry.List(root)
			if err != nil {
				t.Fatal(err)
			}
			var ids []string
			for _, ep := range eps {
				ids = append(ids, ep.ID)
			}
			slices.Sort(ids)
			if !slices.Equal(ids, tc.wantRegistry) {
				t.Errorf("registry = %v, want %v", ids, tc.wantRegistry)
			}
		})
	}
}

func TestListFailsWithoutARegistry(t *testing.T) {
	if _, err := List("", filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Error("List of a missing registry succeeded")
	}
}

func TestSend(t *testing.T) {
	cases := []struct {
		name     string
		applyErr error
		endpoint string
		wantErr  string
		wantSent bool
	}{
		{"command reaches the handler", nil, "", "", true},
		{"handler error comes back", errors.New("thread is gone"), "", "thread is gone", true},
		{"router changed since listing", nil, "ep-old", "the session's router changed; try again", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, sockDir := testRegistry(t)
			sock, applied := listen(t, sockDir, "ep-work", homeWork, tc.applyErr, sessWorkMain)
			all, err := List(sock, "")
			if err != nil || len(all) != 1 {
				t.Fatalf("List = %+v, %v; want one session", all, err)
			}
			s := all[0]
			if tc.endpoint != "" {
				s.Endpoint = tc.endpoint
			}
			cmd := route.Command{Op: route.OpPin, Session: s.ID, Scope: route.ScopeSession, Tier: config.TierHigh}

			err = Send(s, cmd)
			if (err == nil) != (tc.wantErr == "") || (err != nil && !strings.Contains(err.Error(), tc.wantErr)) {
				t.Fatalf("Send = %v, want error %q", err, tc.wantErr)
			}
			got := slices.DeleteFunc(applied(), func(c route.Command) bool { return c.Op == control.OpSubscribe })
			if !tc.wantSent {
				if len(got) != 0 {
					t.Errorf("handler got %+v, want nothing", got)
				}
				return
			}
			if len(got) != 1 {
				t.Fatalf("handler got %+v, want one command", got)
			}
			got[0].ID = ""
			want := route.Command{Op: route.OpPin, Session: sessWorkMain.ThreadID, Scope: route.ScopeSession, Tier: config.TierHigh}
			if got[0] != want {
				t.Errorf("handler got %+v, want %+v", got[0], want)
			}
		})
	}
}

func TestRouteText(t *testing.T) {
	cases := []struct {
		route config.Route
		want  string
	}{
		{config.Route{}, "-"},
		{config.Route{Model: "gpt-5.5"}, "gpt-5.5"},
		{config.Route{Model: "gpt-5.5", Effort: config.EffortLow}, "gpt-5.5 · low"},
	}
	for _, tc := range cases {
		if got := RouteText(tc.route); got != tc.want {
			t.Errorf("RouteText(%+v) = %q, want %q", tc.route, got, tc.want)
		}
	}
}
