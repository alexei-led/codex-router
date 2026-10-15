package sessions

import (
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
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
