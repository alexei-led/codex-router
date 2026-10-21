package brain

import (
	"fmt"
	"log/slog"
	"math/rand/v2"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"

	"github.com/alexei-led/codex-router/internal/appserver"
	"github.com/alexei-led/codex-router/internal/config"
	"github.com/alexei-led/codex-router/internal/responses"
	"github.com/alexei-led/codex-router/internal/route"
)

func (r *rig) apply(t *testing.T, c route.Command) {
	t.Helper()
	if err := r.b.eng.Apply(c); err != nil {
		t.Fatalf("Apply(%+v) = %v", c, err)
	}
}

func (r *rig) intentPath() string { return filepath.Join(r.homeDir(), intentFile) }

func (r *rig) session(t *testing.T, id string) route.SessionInfo {
	t.Helper()
	for _, s := range r.b.eng.Sessions() {
		if s.ThreadID == id {
			return s
		}
	}
	t.Fatalf("no session %q", id)
	return route.SessionInfo{}
}

func withoutRev(in route.Intent) route.Intent {
	in.Rev = 0
	return in
}

func TestBrainKeepsIntentAcrossARestart(t *testing.T) {
	r := newRig(t, "")
	r.d.load("t3", &fakeThread{cwd: "/w3", source: "user", route: native})
	r.start(t)
	eventually(t, "seed t3", func() bool { return r.b.eng.SnapshotFor("t3").Baseline == native })
	r.d.notify(appserver.MethodSettingsUpdated, map[string]any{
		"threadId": "t3", "threadSettings": map[string]any{"model": luna.Model, "effort": luna.Effort},
	})
	eventually(t, "t3 manual", func() bool { return r.b.eng.SnapshotFor("t3").Mode == route.ModeManual })
	high := config.TierHigh
	r.apply(t, route.Command{Op: route.OpPin, Session: "t1", Tier: high})
	r.apply(t, route.Command{Op: route.OpPause, Session: "t2"})
	r.apply(t, route.Command{Op: route.OpMode, Session: "t2", Mode: config.ModeCost})
	r.apply(t, route.Command{Op: route.OpPause, Scope: route.ScopeHome})
	want := route.Intent{Paused: true, Threads: map[string]route.ThreadIntent{
		"t1": {Pin: &high},
		"t2": {Paused: true},
		"t3": {Manual: true},
	}}
	r.b.Close()

	info, err := os.Stat(r.intentPath())
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("intent file: %v, %v; want mode 0600", info, err)
	}
	r.start(t)

	if got := withoutRev(r.b.eng.Intent()); !reflect.DeepEqual(got, want) {
		t.Fatalf("intent after restart = %+v, want %+v", got, want)
	}
	if s := r.b.eng.SnapshotFor("t2"); s.RoutingMode != config.ModeCost || s.RoutingModeSource != route.SourceSession {
		t.Fatalf("t2 mode after restart = %s (%s), want the session's cost", s.RoutingMode, s.RoutingModeSource)
	}
}

func TestBrainSeedsResumedThreadsWithTheirReportedRoute(t *testing.T) {
	r := newRig(t, "")
	def := config.Route{Model: "gpt-6-sol"}
	r.d.load("t1", &fakeThread{cwd: "/w1", source: "user", route: luna})
	r.d.load("t2", &fakeThread{cwd: "/w2", source: "user", route: def})
	r.start(t)
	for _, id := range []string{"t1", "t2"} {
		eventually(t, "resume "+id, func() bool { return r.d.called(appserver.MethodResume, id, config.Route{}) })
	}

	for id, want := range map[string]config.Route{"t1": luna, "t2": def} {
		eventually(t, "seed "+id, func() bool { return r.b.eng.SnapshotFor(id).Baseline == want })
		if s := r.b.eng.SnapshotFor(id); s.Footer != want || !s.FooterSynced {
			t.Errorf("%s: footer %+v synced %v, want %+v", id, s.Footer, s.FooterSynced, want)
		}
		rw, ok := linkHandler{r.b}.Decide(t.Context(), &responses.Request{Model: want.Model, Effort: want.Effort}, userStep(id))
		if ok {
			t.Errorf("%s: rewrite %+v on a held thread", id, rw)
		}
		if s := r.session(t, id); s.Attention != route.AttentionUnknown || s.Route != want {
			t.Errorf("%s: session %+v, want held on %+v", id, s, want)
		}
	}
	if r.d.called(appserver.MethodSettingsUpdate, "t1", config.Route{}) || r.d.called(appserver.MethodSettingsUpdate, "t2", config.Route{}) {
		t.Error("a seeded footer was written")
	}
}

func TestBrainStartsPausedOnAnUnreadableIntentFileAndLeavesItUntouched(t *testing.T) {
	r := newRig(t, "")
	if err := os.MkdirAll(r.homeDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	bad := []byte(`{"version":1,"paused":`)
	if err := os.WriteFile(r.intentPath(), bad, 0o600); err != nil {
		t.Fatal(err)
	}
	r.start(t)
	if s := r.b.eng.SnapshotFor(""); !s.StateLost || s.Mode != route.ModePaused {
		t.Fatalf("after start: state lost %v, mode %s; want lost and paused", s.StateLost, s.Mode)
	}

	r.apply(t, route.Command{Op: route.OpPin, Session: "t1", Tier: config.TierHigh})
	r.b.Close()
	if got, err := os.ReadFile(r.intentPath()); err != nil || string(got) != string(bad) {
		t.Fatalf("intent file after commands while lost = %q, %v; want it untouched", got, err)
	}

	r.start(t)
	if !r.b.eng.SnapshotFor("").StateLost {
		t.Fatal("state lost cleared by a restart")
	}
	r.apply(t, route.Command{Op: route.OpResume, Scope: route.ScopeHome})
	r.b.Close()
	r.start(t)
	if s := r.b.eng.SnapshotFor(""); s.StateLost || s.Mode != route.ModeOn {
		t.Fatalf("after resume and restart: state lost %v, mode %s; want neither", s.StateLost, s.Mode)
	}
}

func TestIntentStoreLoad(t *testing.T) {
	tests := []struct {
		name     string
		content  *string
		wantLost bool
	}{
		{"no file", nil, false},
		{"valid", new(`{"version":1,"paused":true}`), false},
		{"v0.8.0 preset is ignored", new(`{"version":1,"preset":"quality"}`), false},
		{"empty", new(""), true},
		{"truncated", new(`{"version":1,"threads":{"t1":`), true},
		{"other version", new(`{"version":2,"paused":true}`), true},
		{"wrong type", new(`{"version":1,"paused":"yes"}`), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			if tt.content != nil {
				if err := os.WriteFile(filepath.Join(dir, intentFile), []byte(*tt.content), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			in, lost := newIntentStore(dir, slog.New(slog.DiscardHandler)).load()
			if lost != tt.wantLost {
				t.Fatalf("lost = %v, want %v", lost, tt.wantLost)
			}
			if lost && !reflect.DeepEqual(in, route.Intent{}) {
				t.Fatalf("a lost intent returned %+v", in)
			}
		})
	}
}

func TestIntentStoreKeepsTheNewestOfConcurrentWrites(t *testing.T) {
	dir := t.TempDir()
	s := newIntentStore(dir, slog.New(slog.DiscardHandler))
	const n = 64
	revs := rand.Perm(n)
	var wg sync.WaitGroup
	for _, i := range revs {
		wg.Go(func() {
			s.save(route.Intent{Rev: uint64(i + 1), Threads: map[string]route.ThreadIntent{fmt.Sprint("t", i+1): {Paused: true}}})
		})
	}
	wg.Wait()

	in, lost := s.load()
	if _, ok := in.Threads[fmt.Sprint("t", n)]; lost || len(in.Threads) != 1 || !ok {
		t.Fatalf("saved intent = %+v (lost %v), want rev %d's", in, lost, n)
	}
}

func TestBrainSavesTheLastOfConcurrentCommands(t *testing.T) {
	r := newRig(t, "")
	r.start(t)
	var wg sync.WaitGroup
	for i := range 32 {
		wg.Go(func() {
			tier := config.Tiers[i%len(config.Tiers)]
			id := fmt.Sprint("t", i%4)
			if err := r.b.eng.Apply(route.Command{Op: route.OpPin, Session: id, Tier: tier}); err != nil {
				t.Error(err)
			}
			if i%3 == 0 {
				_ = r.b.eng.Apply(route.Command{Op: route.OpPause, Session: id})
			}
		})
	}
	wg.Wait()

	saved, lost := newIntentStore(r.homeDir(), slog.New(slog.DiscardHandler)).load()
	if want := withoutRev(r.b.eng.Intent()); lost || !reflect.DeepEqual(saved, want) {
		t.Fatalf("saved = %+v (lost %v), want the engine's last intent %+v", saved, lost, want)
	}
}
