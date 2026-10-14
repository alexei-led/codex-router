package launcher

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/alexei-led/codex-router/internal/appserver"
	"github.com/alexei-led/codex-router/internal/config"
	"github.com/alexei-led/codex-router/internal/control"
	"github.com/alexei-led/codex-router/internal/route"
)

const rpcTimeout = 5 * time.Second

// sink fans engine output out: decisions to the settings writer's queue,
// snapshots to the control socket. Both calls return at once; the engine
// calls OnDecision while it holds the thread's step lock.
type sink struct {
	writer *settingsWriter
	ctl    atomic.Pointer[control.Server]
}

func (s *sink) OnDecision(d route.Decision) {
	if d.ThreadID != "" && d.Route.Model != "" {
		s.writer.enqueue(d.ThreadID, d.Route)
	}
}

func (s *sink) OnSnapshot(snap route.Snapshot) {
	if c := s.ctl.Load(); c != nil {
		c.Publish(snap)
	}
}

// settingsWriter mirrors routes into the Codex footer. Per thread it keeps
// only the newest wanted route (the engine accepts an echo of any later
// write as covering earlier ones), resumes the thread once so this client
// gets its notifications, and at write time skips a route equal to the
// thread's current one: the last route written or reported by the app-server. Counting
// reports matters: after a manual /model pick and a resume, writing the
// router's old route again must not be skipped.
type settingsWriter struct {
	log  *slog.Logger
	wake chan struct{}

	mu      sync.Mutex
	queue   map[string]config.Route
	order   []string
	current map[string]config.Route
	resumed map[string]bool
}

func newSettingsWriter(log *slog.Logger) *settingsWriter {
	return &settingsWriter{
		log:     log,
		wake:    make(chan struct{}, 1),
		queue:   map[string]config.Route{},
		current: map[string]config.Route{},
		resumed: map[string]bool{},
	}
}

func (w *settingsWriter) enqueue(thread string, r config.Route) {
	// Always queue: current is stale while a write for the thread is in
	// flight, so next() dedupes once that write has finished.
	w.mu.Lock()
	if _, queued := w.queue[thread]; !queued {
		w.order = append(w.order, thread)
	}
	w.queue[thread] = r
	w.mu.Unlock()
	select {
	case w.wake <- struct{}{}:
	default:
	}
}

// reported records a thread/settings/updated value.
func (w *settingsWriter) reported(thread string, r config.Route) {
	w.mu.Lock()
	w.current[thread] = r
	w.mu.Unlock()
}

// next pops the oldest queued thread whose route still needs writing.
func (w *settingsWriter) next() (thread string, r config.Route, resume, ok bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for len(w.order) > 0 {
		thread = w.order[0]
		w.order = slices.Delete(w.order, 0, 1)
		r = w.queue[thread]
		delete(w.queue, thread)
		if w.current[thread] != r {
			return thread, r, !w.resumed[thread], true
		}
	}
	return "", config.Route{}, false, false
}

func (w *settingsWriter) run(ctx context.Context, api appserver.API) {
	for {
		for {
			thread, r, resume, ok := w.next()
			if !ok {
				break
			}
			w.write(ctx, api, thread, r, resume)
		}
		select {
		case <-ctx.Done():
			return
		case <-w.wake:
		}
	}
}

func (w *settingsWriter) write(ctx context.Context, api appserver.API, thread string, r config.Route, resume bool) {
	ctx, cancel := context.WithTimeout(ctx, rpcTimeout)
	defer cancel()
	if resume {
		var rpcErr *appserver.RPCError
		switch err := api.Resume(ctx, thread); {
		case err == nil:
			w.markResumed(thread)
		case errors.As(err, &rpcErr):
			w.markResumed(thread)
			w.log.Info("appserver-resume-skipped", "thread", thread, "code", rpcErr.Code)
		default:
			w.log.Warn("appserver-resume-failed", "thread", thread, "err", err)
		}
	}
	if err := api.UpdateSettings(ctx, thread, appserver.Settings{Model: r.Model, Effort: r.Effort}); err != nil {
		w.log.Warn("appserver-settings-failed", "thread", thread, "err", err)
		return
	}
	w.mu.Lock()
	w.current[thread] = r
	w.mu.Unlock()
	w.log.Info("appserver-settings", "thread", thread, "model", r.Model, "effort", r.Effort)
}

func (w *settingsWriter) markResumed(thread string) {
	w.mu.Lock()
	w.resumed[thread] = true
	w.mu.Unlock()
}
