package appserver

import (
	"context"
	"log/slog"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/alexei-led/codex-router/internal/config"
)

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
func (f *fakeAPI) Resume(_ context.Context, thread string) (Resumed, error) {
	f.record("resume " + thread)
	return Resumed{}, nil
}

func (f *fakeAPI) UpdateSettings(_ context.Context, thread string, s Settings) error {
	f.record("update " + thread + " " + s.Model + "/" + string(s.Effort))
	f.written <- struct{}{}
	return nil
}
func (f *fakeAPI) Updates() <-chan SettingsUpdated { return nil }
func (f *fakeAPI) Close() error                    { return nil }

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
		case <-time.After(waitFor):
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

type gatedAPI struct {
	*fakeAPI
	started chan struct{}
	release chan struct{}
}

func (g *gatedAPI) UpdateSettings(ctx context.Context, thread string, s Settings) error {
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
		case <-time.After(waitFor):
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
		case <-time.After(waitFor):
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
