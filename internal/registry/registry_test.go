package registry

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/alexei-led/codex-router/internal/home"
)

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func newRoot(t *testing.T) string {
	t.Helper()
	root, err := Root(env(map[string]string{"XDG_STATE_HOME": t.TempDir()}))
	if err != nil {
		t.Fatalf("Root: %v", err)
	}
	return root
}

func endpoint(id string, started time.Time) Endpoint {
	return Endpoint{
		ID:       id,
		Kind:     KindSession,
		Protocol: 2,
		PID:      4242,
		Started:  started,
		Home:     home.Home{ID: "3fa9c0d1e2b4", Path: "/u/.codex-work", Name: "work"},
		Socket:   "/run/" + id + "/control.sock",
	}
}

func ids(eps []Endpoint) []string {
	out := make([]string, len(eps))
	for i, ep := range eps {
		out[i] = ep.ID
	}
	return out
}

func TestRootPath(t *testing.T) {
	state, h := t.TempDir(), t.TempDir()
	cases := []struct {
		name string
		env  map[string]string
		want string
	}{
		{"XDG_STATE_HOME", map[string]string{"XDG_STATE_HOME": state, "HOME": h}, filepath.Join(state, "codex-router", "registry")},
		{"relative XDG_STATE_HOME uses HOME", map[string]string{"XDG_STATE_HOME": "state", "HOME": h}, filepath.Join(h, ".local", "state", "codex-router", "registry")},
		{"unset XDG_STATE_HOME uses HOME", map[string]string{"HOME": h}, filepath.Join(h, ".local", "state", "codex-router", "registry")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Root(env(tc.env))
			if err != nil || got != tc.want {
				t.Fatalf("Root = %q, %v; want %q", got, err, tc.want)
			}
			fi, err := os.Stat(got)
			if err != nil || fi.Mode().Perm() != 0o700 {
				t.Errorf("registry dir mode = %v, %v; want 0700", fi.Mode().Perm(), err)
			}
		})
	}
	if _, err := Root(env(map[string]string{"HOME": "u"})); err == nil {
		t.Error("relative HOME, no XDG_STATE_HOME: want error")
	}
}

func TestRootRefusesUnsafeDir(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T, app string)
	}{
		{"registry open to others", func(t *testing.T, app string) {
			mkdir(t, filepath.Join(app, "registry"), 0o755)
		}},
		{"app dir open to group", func(t *testing.T, app string) {
			mkdir(t, filepath.Join(app, "registry"), 0o700)
			chmod(t, app, 0o750)
		}},
		{"registry is a file", func(t *testing.T, app string) {
			if err := os.WriteFile(filepath.Join(app, "registry"), nil, 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{"registry is a symlink to a private dir", func(t *testing.T, app string) {
			target := filepath.Join(app, "elsewhere")
			mkdir(t, target, 0o700)
			if err := os.Symlink(target, filepath.Join(app, "registry")); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			state := t.TempDir()
			app := filepath.Join(state, "codex-router")
			mkdir(t, app, 0o700)
			tc.setup(t, app)
			if got, err := Root(env(map[string]string{"XDG_STATE_HOME": state})); err == nil {
				t.Errorf("Root = %q, want error", got)
			}
		})
	}
}

func mkdir(t *testing.T, dir string, mode os.FileMode) {
	t.Helper()
	if err := os.Mkdir(dir, mode); err != nil {
		t.Fatal(err)
	}
	chmod(t, dir, mode)
}

func chmod(t *testing.T, p string, mode os.FileMode) {
	t.Helper()
	if err := os.Chmod(p, mode); err != nil {
		t.Fatal(err)
	}
}

func TestWriteListRemove(t *testing.T) {
	root := newRoot(t)
	t0 := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	older, newer := endpoint("bbbb", t0), endpoint("aaaa", t0.Add(time.Minute))
	for _, ep := range []Endpoint{newer, older} {
		if err := Write(root, ep); err != nil {
			t.Fatalf("Write %s: %v", ep.ID, err)
		}
	}
	fi, err := os.Stat(filepath.Join(root, "aaaa.json"))
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("entry mode = %v, %v; want 0600", fi.Mode().Perm(), err)
	}

	eps, skipped, err := List(root)
	if err != nil || len(skipped) != 0 {
		t.Fatalf("List: %v, skipped %v", err, skipped)
	}
	if !slices.Equal(eps, []Endpoint{older, newer}) {
		t.Errorf("List = %+v, want oldest first %+v", eps, []Endpoint{older, newer})
	}

	newer.Socket = "/run/moved/control.sock"
	if err := Write(root, newer); err != nil {
		t.Fatal(err)
	}
	if err := Remove(root, older.ID); err != nil {
		t.Fatal(err)
	}
	if err := Remove(root, older.ID); err != nil {
		t.Errorf("Remove of a missing entry: %v, want nil", err)
	}
	eps, _, _ = List(root)
	if !slices.Equal(eps, []Endpoint{newer}) {
		t.Errorf("after replace and remove: List = %+v, want %+v", eps, []Endpoint{newer})
	}
}

func TestBadEndpointIDRefused(t *testing.T) {
	root := newRoot(t)
	for _, id := range []string{"", ".", "..", ".hidden", "a/b", "../escape", `a\b`} {
		if err := Write(root, endpoint(id, time.Now())); err == nil {
			t.Errorf("Write(%q): want error", id)
		}
		if err := Remove(root, id); err == nil {
			t.Errorf("Remove(%q): want error", id)
		}
	}
}

func TestWriteIsAtomicUnderConcurrentWriters(t *testing.T) {
	root := newRoot(t)
	const writers, rounds = 8, 50
	var wg sync.WaitGroup
	stop := make(chan struct{})
	readerErr := make(chan error, 1)
	go func() {
		defer close(readerErr)
		for {
			select {
			case <-stop:
				return
			default:
			}
			eps, skipped, err := List(root)
			if err != nil || len(skipped) != 0 || len(eps) > 1 {
				readerErr <- fmt.Errorf("List = %d entries, skipped %v, err %w", len(eps), skipped, err)
				return
			}
		}
	}()
	for w := range writers {
		wg.Go(func() {
			for r := range rounds {
				ep := endpoint("shared", time.Now())
				ep.Socket = fmt.Sprintf("/run/w%d-r%d/control.sock", w, r)
				if err := Write(root, ep); err != nil {
					t.Errorf("Write: %v", err)
					return
				}
			}
		})
	}
	wg.Wait()
	close(stop)
	if err := <-readerErr; err != nil {
		t.Fatal(err)
	}

	des, err := os.ReadDir(root)
	if err != nil || len(des) != 1 || des[0].Name() != "shared.json" {
		t.Errorf("dir after writes = %v, %v; want only shared.json (no temp files left)", des, err)
	}
	eps, skipped, err := List(root)
	if err != nil || len(skipped) != 0 || len(eps) != 1 {
		t.Errorf("final List = %+v, skipped %v, err %v", eps, skipped, err)
	}
}

func TestListSkipsBadEntries(t *testing.T) {
	root := newRoot(t)
	if err := Write(root, endpoint("good", time.Now())); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"garbage.json":  "{not json",
		"renamed.json":  `{"endpoint_id":"other"}`,
		".tmp123.json":  "{partial",
		"notes.txt":     "ignored",
		".good.0.extra": "{partial",
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	mkdir(t, filepath.Join(root, "dir.json"), 0o700)

	eps, skipped, err := List(root)
	if err != nil {
		t.Fatal(err)
	}
	if got := ids(eps); !slices.Equal(got, []string{"good"}) {
		t.Errorf("List ids = %q, want [good]", got)
	}
	var paths []string
	for _, s := range skipped {
		if s.Err == nil {
			t.Errorf("skipped %s without a reason", s.Path)
		}
		paths = append(paths, filepath.Base(s.Path))
	}
	slices.Sort(paths)
	if want := []string{"dir.json", "garbage.json", "renamed.json"}; !slices.Equal(paths, want) {
		t.Errorf("skipped = %q, want %q", paths, want)
	}
}

func TestListMissingRoot(t *testing.T) {
	if _, _, err := List(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Error("List of a missing dir: want error")
	}
}

func TestPruneKeepsLiveDropsDead(t *testing.T) {
	root := newRoot(t)
	t0 := time.Now()
	for i, id := range []string{"live1", "dead1", "live2", "dead2"} {
		if err := Write(root, endpoint(id, t0.Add(time.Duration(i)*time.Second))); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "garbage.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}

	removed, err := Prune(root, func(ep Endpoint) bool { return ep.ID == "live1" || ep.ID == "live2" })
	if err != nil {
		t.Fatal(err)
	}
	if got := ids(removed); !slices.Equal(got, []string{"dead1", "dead2"}) {
		t.Errorf("removed = %q, want [dead1 dead2]", got)
	}
	eps, skipped, _ := List(root)
	if got := ids(eps); !slices.Equal(got, []string{"live1", "live2"}) {
		t.Errorf("left = %q, want [live1 live2]", got)
	}
	if len(skipped) != 1 {
		t.Errorf("skipped = %v, want the unparseable file kept", skipped)
	}
}

func TestAlive(t *testing.T) {
	dir, err := os.MkdirTemp("", "reg") // short path: macOS caps unix sockets at 104 bytes
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	listening := filepath.Join(dir, "live.sock")
	ln, err := net.Listen("unix", listening)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()

	stale := filepath.Join(dir, "stale.sock")
	sl, err := net.Listen("unix", stale)
	if err != nil {
		t.Fatal(err)
	}
	sl.(*net.UnixListener).SetUnlinkOnClose(false)
	_ = sl.Close()

	exited := exec.Command("true")
	if err := exited.Run(); err != nil {
		t.Fatal(err)
	}
	gone := exited.Process.Pid

	cases := []struct {
		name   string
		socket string
		pid    int
		want   bool
	}{
		{"socket answers, pid gone", listening, gone, true},
		{"stale socket, pid alive", stale, os.Getpid(), true},
		{"stale socket, pid gone", stale, gone, false},
		{"missing socket, pid gone", filepath.Join(dir, "missing.sock"), gone, false},
		{"no socket, no pid", "", 0, false},
		{"no socket, negative pid", "", -1, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Alive(Endpoint{ID: "x", Socket: tc.socket, PID: tc.pid}); got != tc.want {
				t.Errorf("Alive = %v, want %v", got, tc.want)
			}
		})
	}
}
