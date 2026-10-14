//go:build live

// Package stage2 holds the gate spike for the brain's daemon client (plan
// Task 13, gate G1). It talks to a test home's shared Codex daemon through
// internal/appserver's transport and logs what it sees; the gate report
// records the output. Run against a test home only:
//
//	SPIKE_CODEX_HOME=~/.cache/codex-router-live/home-a \
//	  go test -tags live -v -count=1 ./scripts/spikes/stage2/
//
// Optional: SPIKE_WANT_THREADS=N fails unless N main threads are loaded;
// SPIKE_MODEL (and SPIKE_EFFORT) writes those settings to every loaded main
// thread, so the gate can watch the Codex footers repaint; SPIKE_WAIT=60s
// waits that long for a thread/started notification.
package stage2

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/alexei-led/codex-router/internal/appserver"
)

const (
	methodLoadedList  = "thread/loaded/list"
	methodRead        = "thread/read"
	methodUnsubscribe = "thread/unsubscribe"
	methodStarted     = "thread/started"
	callTimeout       = 15 * time.Second
)

// thread is the part of the Thread object the brain needs.
type thread struct {
	ID            string          `json:"id"`
	Cwd           string          `json:"cwd"`
	Ephemeral     bool            `json:"ephemeral"`
	ModelProvider string          `json:"modelProvider"`
	Source        json.RawMessage `json:"source"`
	ThreadSource  json.RawMessage `json:"threadSource"`
	ParentID      *string         `json:"parentThreadId"`
}

// main reports whether the brain would track this thread: not ephemeral,
// not a subagent or side thread.
func (t thread) main() bool {
	ts := strings.Trim(string(t.ThreadSource), `"`)
	return !t.Ephemeral && t.ParentID == nil && (ts == "" || ts == "null" || ts == "user")
}

type resumed struct {
	Model           string `json:"model"`
	ReasoningEffort string `json:"reasoningEffort"`
	ModelProvider   string `json:"modelProvider"`
	Cwd             string `json:"cwd"`
	Thread          thread `json:"thread"`
}

type inbound struct {
	ID     json.RawMessage     `json:"id"`
	Method string              `json:"method"`
	Params json.RawMessage     `json:"params"`
	Result json.RawMessage     `json:"result"`
	Error  *appserver.RPCError `json:"error"`
}

// client is a bare JSON-RPC client: unlike appserver.Client it surfaces
// every notification, and it leaves server requests unanswered so it never
// answers an approval meant for a Codex TUI.
type client struct {
	t     *testing.T
	conn  appserver.Conn
	msgs  chan inbound
	notes []inbound
	next  int64
}

func dial(t *testing.T) *client {
	t.Helper()
	home := testHome(t)
	sock := filepath.Join(home, "app-server-control", "app-server-control.sock")
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	conn, err := appserver.WebSocket().Dial(ctx, sock)
	if err != nil {
		t.Fatalf("dial %s: %v (is the home's daemon running?)", sock, err)
	}
	c := &client{t: t, conn: conn, msgs: make(chan inbound, 256)}
	t.Cleanup(func() { _ = conn.Close() })
	go c.readLoop()

	if err := c.call(appserver.MethodInitialize, map[string]any{
		"clientInfo":   map[string]string{"name": "codex-router-spike", "version": "dev"},
		"capabilities": map[string]bool{"experimentalApi": true},
	}, nil); err != nil {
		t.Fatalf("%s: %v", appserver.MethodInitialize, err)
	}
	c.notify(appserver.MethodInitialized)
	return c
}

// testHome refuses anything but a test home (plan safety rules).
func testHome(t *testing.T) string {
	t.Helper()
	home := os.Getenv("SPIKE_CODEX_HOME")
	if home == "" {
		t.Skip("SPIKE_CODEX_HOME not set")
	}
	userHome, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(userHome, ".cache", "codex-router-live") + string(filepath.Separator)
	abs, err := filepath.Abs(home)
	if err != nil {
		t.Fatal(err)
	}
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		abs = real
	}
	if realRoot, err := filepath.EvalSymlinks(root); err == nil {
		root = realRoot + string(filepath.Separator)
	}
	if !strings.HasPrefix(abs, root) {
		t.Fatalf("refusing %s: not a test home under %s", abs, root)
	}
	return abs
}

func (c *client) readLoop() {
	defer close(c.msgs)
	for {
		b, err := c.conn.Read(context.Background())
		if err != nil {
			return
		}
		var in inbound
		if err := json.Unmarshal(b, &in); err != nil {
			c.t.Logf("bad message (%d bytes)", len(b))
			continue
		}
		c.msgs <- in
	}
}

func (c *client) write(m map[string]any) {
	c.t.Helper()
	m["jsonrpc"] = "2.0"
	b, err := json.Marshal(m)
	if err != nil {
		c.t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	if err := c.conn.Write(ctx, b); err != nil {
		c.t.Fatalf("write %v: %v", m["method"], err)
	}
}

func (c *client) notify(method string) { c.write(map[string]any{"method": method}) }

// call sends a request and waits for its reply, keeping notifications that
// arrive meanwhile for waitNote.
func (c *client) call(method string, params, result any) error {
	c.t.Helper()
	c.next++
	id := c.next
	c.write(map[string]any{"id": id, "method": method, "params": params})
	timeout := time.After(callTimeout)
	for {
		select {
		case in, ok := <-c.msgs:
			if !ok {
				c.t.Fatalf("%s: connection closed", method)
			}
			if c.keep(in) {
				continue
			}
			if string(in.ID) != strconv.FormatInt(id, 10) {
				continue
			}
			if in.Error != nil {
				return in.Error
			}
			if result != nil {
				if err := json.Unmarshal(in.Result, result); err != nil {
					c.t.Fatalf("%s result: %v", method, err)
				}
			}
			return nil
		case <-timeout:
			c.t.Fatalf("%s: no reply in %s", method, callTimeout)
		}
	}
}

// keep stores a notification and logs a server request; it reports false
// for a reply.
func (c *client) keep(in inbound) bool {
	switch {
	case in.Method == "":
		return false
	case len(in.ID) > 0 && string(in.ID) != "null":
		c.t.Logf("server request %s left to the Codex TUIs", in.Method)
	default:
		c.notes = append(c.notes, in)
	}
	return true
}

// waitNote returns the first notification with method that match accepts.
func (c *client) waitNote(method string, d time.Duration, match func(json.RawMessage) bool) (json.RawMessage, bool) {
	c.t.Helper()
	timeout := time.After(d)
	for {
		for i, n := range c.notes {
			if n.Method == method && match(n.Params) {
				c.notes = append(c.notes[:i], c.notes[i+1:]...)
				return n.Params, true
			}
		}
		select {
		case in, ok := <-c.msgs:
			if !ok {
				return nil, false
			}
			c.keep(in)
		case <-timeout:
			return nil, false
		}
	}
}

func (c *client) loaded() []thread {
	c.t.Helper()
	var ids []string
	cursor := ""
	for {
		params := map[string]any{}
		if cursor != "" {
			params["cursor"] = cursor
		}
		var page struct {
			Data       []string `json:"data"`
			NextCursor *string  `json:"nextCursor"`
		}
		if err := c.call(methodLoadedList, params, &page); err != nil {
			c.t.Fatalf("%s: %v", methodLoadedList, err)
		}
		ids = append(ids, page.Data...)
		if page.NextCursor == nil || *page.NextCursor == "" {
			break
		}
		cursor = *page.NextCursor
	}
	threads := make([]thread, 0, len(ids))
	for _, id := range ids {
		var r struct {
			Thread thread `json:"thread"`
		}
		if err := c.call(methodRead, map[string]any{"threadId": id, "includeTurns": false}, &r); err != nil {
			c.t.Logf("%s %s: %v", methodRead, id, err)
			continue
		}
		threads = append(threads, r.Thread)
	}
	return threads
}

// resume subscribes to the thread and unsubscribes at cleanup: the daemon
// unloads an idle thread only when it has no subscribers. A thread has no
// rollout to resume (-32600) until its first turn.
func (c *client) resume(id string) (resumed, error) {
	c.t.Helper()
	var r resumed
	if err := c.call(appserver.MethodResume, map[string]any{"threadId": id, "excludeTurns": true}, &r); err != nil {
		return r, fmt.Errorf("%s %s: %w", appserver.MethodResume, id, err)
	}
	c.t.Cleanup(func() {
		var u struct {
			Status string `json:"status"`
		}
		if err := c.call(methodUnsubscribe, map[string]any{"threadId": id}, &u); err != nil {
			c.t.Logf("%s %s: %v", methodUnsubscribe, id, err)
			return
		}
		c.t.Logf("%s %s: %s", methodUnsubscribe, id, u.Status)
	})
	return r, nil
}

func mainThreads(t *testing.T, c *client) []thread {
	t.Helper()
	var mains []thread
	for _, th := range c.loaded() {
		t.Logf("loaded %s cwd=%s ephemeral=%t provider=%s source=%s threadSource=%s parent=%t main=%t",
			th.ID, th.Cwd, th.Ephemeral, th.ModelProvider, th.Source, th.ThreadSource, th.ParentID != nil, th.main())
		if th.main() {
			mains = append(mains, th)
		}
	}
	return mains
}

// G1: the spike client sees the TUIs' threads with their cwd, and
// thread/resume returns each thread's current settings.
func TestLoadedThreadsAndResume(t *testing.T) {
	c := dial(t)
	mains := mainThreads(t, c)
	if want, _ := strconv.Atoi(os.Getenv("SPIKE_WANT_THREADS")); len(mains) < want {
		t.Errorf("main threads = %d, want at least %d", len(mains), want)
	}
	for _, th := range mains {
		r, err := c.resume(th.ID)
		if err != nil {
			t.Errorf("%v (send the thread a prompt first)", err)
			continue
		}
		t.Logf("resumed %s model=%s effort=%s provider=%s cwd=%s", th.ID, r.Model, r.ReasoningEffort, r.ModelProvider, r.Cwd)
		if r.Model == "" || r.Cwd == "" {
			t.Errorf("resume %s: model %q cwd %q, want both set", th.ID, r.Model, r.Cwd)
		}
	}
}

// G1: thread/settings/update from a second client is echoed as
// thread/settings/updated; the gate checks the Codex footers repaint.
func TestSettingsUpdate(t *testing.T) {
	model := os.Getenv("SPIKE_MODEL")
	if model == "" {
		t.Skip("SPIKE_MODEL not set")
	}
	c := dial(t)
	mains := mainThreads(t, c)
	if len(mains) == 0 {
		t.Fatal("no loaded main threads")
	}
	for _, th := range mains {
		// Without a subscription the write may still apply; the
		// notification below shows whether it reaches this client.
		if _, err := c.resume(th.ID); err != nil {
			t.Logf("%v; writing settings unsubscribed", err)
		}
		s := map[string]any{"threadId": th.ID, "model": model}
		if e := os.Getenv("SPIKE_EFFORT"); e != "" {
			s["effort"] = e
		}
		if err := c.call(appserver.MethodSettingsUpdate, s, nil); err != nil {
			t.Errorf("%s %s: %v", appserver.MethodSettingsUpdate, th.ID, err)
			continue
		}
		p, ok := c.waitNote(appserver.MethodSettingsUpdated, 10*time.Second, func(p json.RawMessage) bool {
			var u struct {
				ThreadID string `json:"threadId"`
			}
			return json.Unmarshal(p, &u) == nil && u.ThreadID == th.ID
		})
		if !ok {
			t.Errorf("no %s for %s", appserver.MethodSettingsUpdated, th.ID)
			continue
		}
		t.Logf("updated %s: %s", th.ID, p)
	}
}

// Brain discovery: a thread started by a Codex TUI after the client
// connected arrives as thread/started.
func TestThreadStarted(t *testing.T) {
	wait, err := time.ParseDuration(os.Getenv("SPIKE_WAIT"))
	if err != nil {
		t.Skip("SPIKE_WAIT not set; start a codex TUI in the home while the test waits")
	}
	c := dial(t)
	t.Logf("waiting %s for %s", wait, methodStarted)
	p, ok := c.waitNote(methodStarted, wait, func(json.RawMessage) bool { return true })
	if !ok {
		t.Fatalf("no %s in %s", methodStarted, wait)
	}
	var n struct {
		Thread thread `json:"thread"`
	}
	if err := json.Unmarshal(p, &n); err != nil {
		t.Fatal(err)
	}
	t.Logf("started %s cwd=%s ephemeral=%t source=%s threadSource=%s main=%t",
		n.Thread.ID, n.Thread.Cwd, n.Thread.Ephemeral, n.Thread.Source, n.Thread.ThreadSource, n.Thread.main())
}
