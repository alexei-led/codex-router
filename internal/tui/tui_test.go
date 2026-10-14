package tui

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/alexei-led/codex-router/internal/catalog"
	"github.com/alexei-led/codex-router/internal/classifier"
	"github.com/alexei-led/codex-router/internal/config"
	"github.com/alexei-led/codex-router/internal/control"
	"github.com/alexei-led/codex-router/internal/policy"
	"github.com/alexei-led/codex-router/internal/route"
)

var update = flag.Bool("update", false, "rewrite golden files")

func at(h, m int) time.Time { return time.Date(2026, 10, 13, h, m, 0, 0, time.UTC) }

func fixture() route.Snapshot {
	sol61 := config.Route{Model: "gpt-6.1-sol", Effort: config.EffortHigh}
	sol := config.Route{Model: "gpt-6-sol", Effort: config.EffortMedium}
	luna := config.Route{Model: "gpt-6-luna", Effort: config.EffortMedium}
	micro := config.Route{Model: "gpt-6-luna", Effort: config.EffortLow}
	last := route.Decision{
		Tier: config.TierHigh, Activity: config.ActivityDebug, Route: sol61, Original: sol, Rewritten: true,
		Reason: policy.ReasonEscalation, MidTurn: true, At: at(18, 41),
		Why:     "escalate: same test failure twice, edit in between",
		Support: "no classifier needed · switch ≈ 41k uncached tokens · hold 2 turns",
		Reading: &route.Reading{
			Tiers:    map[config.Tier]float64{config.TierHigh: 0.18, config.TierMedium: 0.71, config.TierLow: 0.09, config.TierMicro: 0.02},
			Activity: config.ActivityDebug, ActivityP: 0.72,
			Alternates: []route.Share{{Activity: config.ActivityCode, P: 0.15}, {Activity: config.ActivityExplore, P: 0.06}},
		},
	}
	earlier := route.Decision{Tier: config.TierMedium, Activity: config.ActivityDebug, Route: sol, Reason: policy.ReasonUpgrade, At: at(18, 38)}
	return route.Snapshot{
		ThreadID: "th", At: at(18, 42), Mode: route.ModeOn, Preset: config.PresetBalanced,
		Baseline: sol61, Footer: sol61, FooterSynced: true,
		Last: &last, Recent: []route.Decision{earlier, last},
		Classifier: classifier.Health{Latency: 412 * time.Millisecond}, ClassifierLabel: "Jev", ClassifierMedian: 388 * time.Millisecond,
		Routes:  map[config.Tier]config.Route{config.TierHigh: sol61, config.TierMedium: sol, config.TierLow: luna, config.TierMicro: micro},
		Group:   catalog.Group{CompHash: "3000"},
		Blocked: []string{"gpt-5.5: comp_hash 2911, no Lite"},
		Policy: config.Policy{UpgradeVotes: 2, DowngradeVotes: 2, EscalationHoldTurns: 2, MinDwellRequests: 8, ReviewEveryRequests: 6,
			UpgradeBase: 0.65, JumpConfidence: 0.85, DowngradeMass: 0.7, ActivityMass: 0.6},
		Stats: route.Stats{
			Since: at(18, 12), Replies: 48, Switches: 9, RepliesPerCell: 1,
			ByTier:          map[config.Tier]int{config.TierLow: 20, config.TierMedium: 22, config.TierHigh: 6},
			ByActivity:      map[config.Activity]int{config.ActivityCode: 16, config.ActivityExplore: 12, config.ActivityDebug: 9, config.ActivityOps: 6},
			Strip:           strip("lllllmmmmmlllmmmmmmmmhhhhmmmlllllmmmmmmlllhhhh"),
			LastSwitch:      &route.Switch{At: at(18, 41), From: config.TierMedium, To: config.TierHigh, Activity: config.ActivityDebug, MidTurn: true},
			CacheReusePct:   97,
			LastInputTokens: 44_000, LastCachedTokens: 43_120,
			UncachedSwitchTokens: 212_000,
			FitHeld:              2,
			RoutedUSD:            1.87, BaselineUSD: 2.71,
		},
		ApproxTokens: 44_600, Window: 272_000,
		Worth: []route.Finding{
			{Kind: route.FindingContext, Text: "context guard kept 2 steps on a larger window than the policy chose"},
			{Kind: route.FindingBlocked, Text: "routes to gpt-5.5 never run: outside gpt-6.1-sol's compatibility group"},
		},
	}
}

func strip(s string) []config.Tier {
	tiers := map[rune]config.Tier{'l': config.TierLow, 'm': config.TierMedium, 'h': config.TierHigh}
	var out []config.Tier
	for _, r := range s {
		out = append(out, tiers[r])
	}
	return out
}

func TestRenderGolden(t *testing.T) {
	for _, tab := range Tabs {
		for _, width := range []int{80, 120} {
			t.Run(fmt.Sprintf("%s-%d", tab, width), func(t *testing.T) {
				got := RenderPlain(fixture(), tab, width)
				path := filepath.Join("testdata", fmt.Sprintf("%s-%d.golden", tab, width))
				if *update {
					if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
						t.Fatal(err)
					}
				}
				want, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				if got != string(want) {
					t.Errorf("render differs from %s\n--- got\n%s--- want\n%s", path, got, want)
				}
			})
		}
	}
}

func TestRenderProperties(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*route.Snapshot)
		tab    Tab
		want   []string
	}{
		{"manual holds", func(s *route.Snapshot) { s.Mode = route.ModeManual }, TabNow, []string{"◐ manual", "picked with /model"}},
		{"paused", func(s *route.Snapshot) { s.Mode = route.ModePaused }, TabNow, []string{"○ paused", "unchanged"}},
		{"pinned", func(s *route.Snapshot) { p := config.TierLow; s.Pin = &p }, TabNow, []string{"▌low", "pinned"}},
		{"classifier paused", func(s *route.Snapshot) { s.Classifier.PausedUntil = s.At.Add(time.Minute) }, TabClassifier, []string{"○ Jev", "forwarded unchanged"}},
		{"classifier off header", func(s *route.Snapshot) { s.ClassifierOff = true }, TabNow, []string{"Jev off"}},
		{"classifier off tab", func(s *route.Snapshot) { s.ClassifierOff = true }, TabClassifier, []string{"○ Jev", "keep their model unless pinned"}},
		{"footer not synced", func(s *route.Snapshot) { s.FooterSynced = false }, TabNow, []string{"footer not synced"}},
		{"empty session", func(s *route.Snapshot) { s.Stats = route.Stats{}; s.Last = nil }, TabNow, []string{"no routed replies yet", "no routed request yet"}},
		{"routing costs more", func(s *route.Snapshot) { s.Stats.RoutedUSD = 3 }, TabUsage, []string{"Routing cost"}},
		{"tier the answer left out", func(s *route.Snapshot) { delete(s.Last.Reading.Tiers, config.TierMicro) }, TabNow, []string{"low 9%", "micro —"}},
		{"uncertain activity answer", func(s *route.Snapshot) { s.Last.Reading.Activity, s.Last.Reading.ActivityP = "uncertain", 0.5 }, TabNow, []string{"uncertain 50%"}},
		{"manual finding", func(s *route.Snapshot) {
			s.Worth = []route.Finding{{Kind: route.FindingManual, Text: "footer picked with /model"}}
		}, TabNow, []string{"WORTH A LOOK", "footer picked with /model · space resumes"}},
		{"no last reply reading", func(s *route.Snapshot) { s.Stats.LastInputTokens = 0 }, TabNow, []string{"97% reused this session"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := fixture()
			tt.mutate(&s)
			got := RenderPlain(s, tt.tab, 100)
			for _, w := range tt.want {
				if !strings.Contains(got, w) {
					t.Errorf("missing %q in\n%s", w, got)
				}
			}
		})
	}
}

func TestRenderLeavesOutWhatTheSnapshotLacks(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*route.Snapshot)
		tab    Tab
		absent []string
	}{
		{"no classifier answer", func(s *route.Snapshot) { s.Last.Reading = nil }, TabNow, []string{"high 18%", "applies at", "code 15%"}},
		{"nothing worth a look", func(s *route.Snapshot) { s.Worth = nil }, TabNow, []string{"WORTH A LOOK"}},
		{"no timed answers", func(s *route.Snapshot) { s.ClassifierMedian = 0 }, TabClassifier, []string{"median"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := fixture()
			tt.mutate(&s)
			got := RenderPlain(s, tt.tab, 100)
			for _, a := range tt.absent {
				if strings.Contains(got, a) {
					t.Errorf("unexpected %q in\n%s", a, got)
				}
			}
		})
	}
}

func TestRenderIsDeterministicAndWidthBound(t *testing.T) {
	s := fixture()
	for _, tab := range Tabs {
		for _, width := range []int{40, 80, 120} {
			a, b := RenderPlain(s, tab, width), RenderPlain(s, tab, width)
			if a != b {
				t.Fatalf("%s/%d not deterministic", tab, width)
			}
			for _, line := range strings.Split(a, "\n") {
				if n := len([]rune(line)); n > width {
					t.Errorf("%s/%d: line of %d runes: %q", tab, width, n, line)
				}
			}
		}
	}
}

func TestPlainHasNoEscapesAndStyledDoes(t *testing.T) {
	s := fixture()
	if strings.Contains(RenderPlain(s, TabNow, 100), "\x1b") {
		t.Error("plain render contains ANSI")
	}
	if !strings.Contains(Render(s, TabNow, 100), "\x1b[") {
		t.Error("styled render has no ANSI")
	}
}

const testSession = "h1/th"

type fakeConn struct {
	updates    chan route.Snapshot
	sessions   chan []route.SessionInfo
	sent       []route.Command
	subscribed []string
	err        error
	closed     bool
}

func newFakeConn() *fakeConn {
	return &fakeConn{updates: make(chan route.Snapshot, 4), sessions: make(chan []route.SessionInfo, 1)}
}

func (f *fakeConn) Snapshots() <-chan route.Snapshot     { return f.updates }
func (f *fakeConn) Sessions() <-chan []route.SessionInfo { return f.sessions }
func (f *fakeConn) Subscribe(s string) error             { f.subscribed = append(f.subscribed, s); return f.err }
func (f *fakeConn) Send(c route.Command) error           { f.sent = append(f.sent, c); return f.err }
func (f *fakeConn) Close() error                         { f.closed = true; return nil }

// listed feeds a sessions list and runs the commands it starts.
func listed(m *model, f *fakeConn, ids ...string) {
	items := make([]route.SessionInfo, len(ids))
	for i, id := range ids {
		items[i] = route.SessionInfo{ID: id}
	}
	f.sessions <- nil // the next-list wait returns at once instead of blocking
	_, cmd := m.Update(sessionsMsg{items, m.gen})
	if batch, ok := cmd().(tea.BatchMsg); ok {
		for _, c := range batch {
			if msg, ok := c().(sentMsg); ok {
				m.Update(msg)
			}
		}
	}
}

func connected(t *testing.T, snap route.Snapshot) (*model, *fakeConn) {
	t.Helper()
	f := newFakeConn()
	m := newModel("sock", func(string) (conn, error) { return f, nil })
	m.Update(dialedMsg{c: f})
	listed(m, f, testSession)
	m.Update(snapMsg(snap))
	return m, f
}

// run executes a command and feeds the resulting message back into the model.
func run(m *model, cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	if msg, ok := cmd().(sentMsg); ok {
		m.Update(msg)
	}
}

func key(m *model, s string) tea.Cmd {
	k := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
	if s == " " {
		k = tea.KeyMsg{Type: tea.KeySpace, Runes: []rune(" ")}
	}
	if s == "tab" {
		k = tea.KeyMsg{Type: tea.KeyTab}
	}
	_, cmd := m.Update(k)
	return cmd
}

func TestKeysSendCommands(t *testing.T) {
	low := config.TierLow
	micro := config.TierMicro
	tests := []struct {
		name  string
		setup func(*route.Snapshot)
		tab   Tab
		key   string
		want  route.Command
	}{
		{"pin from none", func(*route.Snapshot) {}, TabNow, "p", route.Command{Op: route.OpPin, Session: testSession, Tier: config.TierHigh}},
		{"pin advances", func(s *route.Snapshot) { s.Pin = &low }, TabNow, "p", route.Command{Op: route.OpPin, Session: testSession, Tier: config.TierMicro}},
		{"pin wraps to unpin", func(s *route.Snapshot) { s.Pin = &micro }, TabNow, "p", route.Command{Op: route.OpUnpin, Session: testSession}},
		{"space pauses", func(*route.Snapshot) {}, TabNow, " ", route.Command{Op: route.OpPause}},
		{"space resumes paused", func(s *route.Snapshot) { s.Mode = route.ModePaused }, TabNow, " ", route.Command{Op: route.OpResume}},
		{"space resumes manual", func(s *route.Snapshot) { s.Mode = route.ModeManual }, TabNow, " ", route.Command{Op: route.OpResume}},
		{"preset cycles on policy", func(*route.Snapshot) {}, TabPolicy, "c", route.Command{Op: route.OpPreset, Preset: config.PresetQuality}},
		{"preset wraps", func(s *route.Snapshot) { s.Preset = config.PresetQuality }, TabPolicy, "c", route.Command{Op: route.OpPreset, Preset: config.PresetThrifty}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := fixture()
			tt.setup(&s)
			m, f := connected(t, s)
			m.tab = tt.tab
			run(m, key(m, tt.key))
			if len(f.sent) != 1 || f.sent[0] != tt.want {
				t.Errorf("sent %+v, want [%+v]", f.sent, tt.want)
			}
		})
	}
}

func TestFollowsTheMostRecentSession(t *testing.T) {
	m, f := connected(t, fixture())
	listed(m, f, testSession, "h1/older")
	listed(m, f, "h1/newer", testSession)
	if want := []string{testSession, "h1/newer"}; !slices.Equal(f.subscribed, want) {
		t.Fatalf("subscribed %v, want %v", f.subscribed, want)
	}
	listed(m, f)
	if len(f.subscribed) != 2 {
		t.Errorf("an empty list changed the subscription: %v", f.subscribed)
	}
}

func TestPresetKeyIgnoredOutsidePolicy(t *testing.T) {
	m, f := connected(t, fixture())
	run(m, key(m, "c"))
	if len(f.sent) != 0 {
		t.Errorf("sent %+v", f.sent)
	}
}

func TestTabKeys(t *testing.T) {
	m, _ := connected(t, fixture())
	key(m, "4")
	if m.tab != TabClassifier {
		t.Errorf("tab = %s", m.tab)
	}
	key(m, "tab")
	if m.tab != TabUsage {
		t.Errorf("tab = %s", m.tab)
	}
	key(m, "tab")
	if m.tab != TabNow {
		t.Errorf("tab = %s", m.tab)
	}
}

func TestQuitClosesConnection(t *testing.T) {
	m, f := connected(t, fixture())
	if cmd := key(m, "q"); cmd == nil {
		t.Fatal("no quit command")
	}
	if !f.closed {
		t.Error("connection left open")
	}
}

func TestDisconnectedHeaderAndRedial(t *testing.T) {
	m, f := connected(t, fixture())
	if strings.Contains(m.View(), "disconnected") {
		t.Fatal("connected view says disconnected")
	}
	_, cmd := m.Update(closedMsg{gen: m.gen})
	if cmd == nil || !m.disconnected {
		t.Fatal("loss not detected")
	}
	view := m.View()
	for _, w := range []string{"✕ disconnected", "retrying", "last snapshot 18:42:00"} {
		if !strings.Contains(view, w) {
			t.Errorf("view missing %q", w)
		}
	}
	if k := key(m, "p"); k != nil || !strings.Contains(m.View(), "command not sent") || len(f.sent) != 0 {
		t.Error("command went out while disconnected")
	}

	dials := 0
	g := newFakeConn()
	m.dial = func(string) (conn, error) {
		dials++
		if dials < 3 {
			return nil, errors.New("refused")
		}
		return g, nil
	}
	for range 3 {
		msg := m.dialCmd()()
		m.Update(msg)
	}
	if m.c != conn(g) {
		t.Fatal("did not reconnect")
	}
	m.Update(snapMsg(fixture()))
	if strings.Contains(m.View(), "disconnected") {
		t.Error("still disconnected after snapshot")
	}
}

func TestRedialFollowsLocatedSocket(t *testing.T) {
	sessions := []string{"old/control.sock", "new/control.sock"}
	var dialed []string
	dial := dialer(func() (string, error) {
		s := sessions[0]
		sessions = sessions[1:]
		return s, nil
	}, func(path string) (conn, error) {
		dialed = append(dialed, path)
		return newFakeConn(), nil
	})
	for range 2 {
		if _, err := dial("old/control.sock"); err != nil {
			t.Fatal(err)
		}
	}
	if want := []string{"old/control.sock", "new/control.sock"}; !slices.Equal(dialed, want) {
		t.Errorf("dialed %v, want %v", dialed, want)
	}
}

func TestRedialWithoutLiveSessionFails(t *testing.T) {
	dial := dialer(func() (string, error) { return "", errors.New("no running session") },
		func(string) (conn, error) { t.Fatal("dialed without a socket"); return nil, nil })
	if _, err := dial("old/control.sock"); err == nil {
		t.Error("want an error")
	}
}

func TestHeartbeatDetectsLostSocket(t *testing.T) {
	m, f := connected(t, fixture())
	f.err = control.ErrDisconnected
	_, cmd := m.Update(beatMsg{gen: m.gen})
	if cmd == nil {
		t.Fatal("no heartbeat command")
	}
	_, cmd = m.Update(sentMsg{f.Send(route.Command{Op: route.OpSnapshot}), m.gen})
	if !m.disconnected || !f.closed {
		t.Fatalf("lost socket: disconnected %v, closed %v", m.disconnected, f.closed)
	}
	if cmd == nil {
		t.Fatal("no redial scheduled")
	}
	if _, ok := cmd().(redialMsg); !ok {
		t.Error("lost socket does not lead to a redial")
	}
}

func TestStaleCloseKeepsNewConnection(t *testing.T) {
	m, _ := connected(t, fixture())
	oldGen := m.gen
	m.Update(closedMsg{gen: oldGen})
	g := newFakeConn()
	m.Update(dialedMsg{c: g})
	m.Update(closedMsg{gen: oldGen})
	if m.c != conn(g) || g.closed {
		t.Error("close of the old connection dropped the new one")
	}
}

func TestRejectedCommandShowsNotice(t *testing.T) {
	m, f := connected(t, fixture())
	f.err = errors.New("unknown tier")
	run(m, key(m, "p"))
	if m.disconnected || !strings.Contains(m.View(), "command failed: unknown tier") {
		t.Errorf("view:\n%s", m.View())
	}
}

func TestWindowSizeSetsWidth(t *testing.T) {
	m, _ := connected(t, fixture())
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	if m.width != 120 {
		t.Errorf("width = %d", m.width)
	}
}

func TestStaleHeartbeatAndErrorsAreIgnoredAfterReconnect(t *testing.T) {
	m, _ := connected(t, fixture())
	oldGen := m.gen
	m.Update(closedMsg{gen: oldGen})
	g := newFakeConn()
	m.Update(dialedMsg{c: g})

	if _, cmd := m.Update(beatMsg{gen: oldGen}); cmd != nil {
		t.Error("stale heartbeat chain kept running")
	}
	m.Update(sentMsg{err: control.ErrDisconnected, gen: oldGen})
	m.Update(snapMsg(fixture()))
	if m.disconnected {
		t.Error("stale error from old connection marked new one disconnected")
	}
}

func TestAddNeverExceedsWidth(t *testing.T) {
	for w := 1; w <= 6; w++ {
		c := &canvas{width: w}
		c.add(plain("abcdef"[:w]), plain("x"), plain("yz"))
		if got := utf8.RuneCountInString(c.out[0]); got > w {
			t.Errorf("width %d: line has %d runes", w, got)
		}
	}
}
