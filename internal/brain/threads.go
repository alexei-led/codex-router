package brain

import (
	"sync"
	"time"
)

// tracked is one main thread of the home's daemon.
type tracked struct {
	cwd        string
	subscribed bool      // resumed on the current daemon connection
	retry      bool      // resume at the daemon worker's next pass
	seen       time.Time // the last turn, settings event or routed request
}

// threads is the set of main threads the brain follows, and the thread ids
// seen in routed requests that it does not follow yet. Safe for concurrent
// use.
type threads struct {
	now func() time.Time

	mu      sync.Mutex
	m       map[string]*tracked
	unknown map[string]bool
}

func newThreads(now func() time.Time) *threads {
	return &threads{now: now, m: map[string]*tracked{}, unknown: map[string]bool{}}
}

// add follows id, due for a resume. A known thread keeps its state; a
// non-empty cwd replaces the one it had. It reports whether id is new.
func (t *threads) add(id, cwd string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.unknown, id)
	th, ok := t.m[id]
	if !ok {
		th = &tracked{retry: true, seen: t.now()}
		t.m[id] = th
	}
	if cwd != "" {
		th.cwd = cwd
	}
	return !ok
}

// cwd is the thread's working directory, "" when unknown. It is the engine's
// Deps.Cwd.
func (t *threads) cwd(id string) string {
	t.mu.Lock()
	defer t.mu.Unlock()
	if th, ok := t.m[id]; ok {
		return th.cwd
	}
	return ""
}

// known reports whether id is followed.
func (t *threads) known(id string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	_, ok := t.m[id]
	return ok
}

// touch records activity on id. An unsubscribed thread becomes due for a
// resume (its first turn makes a thread resumable), and with lookup an
// unknown id is queued to be read. It reports whether the daemon worker has
// new work.
func (t *threads) touch(id string, lookup bool) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if th, ok := t.m[id]; ok {
		th.seen = t.now()
		if th.subscribed || th.retry {
			return false
		}
		th.retry = true
		return true
	}
	if !lookup || t.unknown[id] {
		return false
	}
	t.unknown[id] = true
	return true
}

// subscribed records a successful resume and the cwd it reported.
func (t *threads) subscribed(id, cwd string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if th, ok := t.m[id]; ok {
		th.subscribed = true
		if cwd != "" {
			th.cwd = cwd
		}
	}
}

// remove stops following id and reports whether it was subscribed.
func (t *threads) remove(id string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	th, ok := t.m[id]
	delete(t.m, id)
	return ok && th.subscribed
}

// lost marks every thread unsubscribed and due for a resume: the daemon
// connection ended, and its subscriptions with it.
func (t *threads) lost() {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, th := range t.m {
		th.subscribed, th.retry = false, true
	}
}

// takeLookups returns the unknown ids to read and forgets them: a failed
// read is retried at the thread's next routed request.
func (t *threads) takeLookups() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	ids := make([]string, 0, len(t.unknown))
	for id := range t.unknown {
		ids = append(ids, id)
	}
	clear(t.unknown)
	return ids
}

// takeResumes returns the threads due for a resume and clears the mark.
func (t *threads) takeResumes() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	var ids []string
	for id, th := range t.m {
		if th.retry {
			th.retry = false
			ids = append(ids, id)
		}
	}
	return ids
}

// idle returns the threads with no activity for d.
func (t *threads) idle(d time.Duration) []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.now()
	var ids []string
	for id, th := range t.m {
		if now.Sub(th.seen) >= d {
			ids = append(ids, id)
		}
	}
	return ids
}
