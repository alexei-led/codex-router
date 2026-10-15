package tui

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"

	"github.com/alexei-led/codex-router/internal/catalog"
	"github.com/alexei-led/codex-router/internal/classifier"
	"github.com/alexei-led/codex-router/internal/config"
	"github.com/alexei-led/codex-router/internal/control"
	"github.com/alexei-led/codex-router/internal/home"
	"github.com/alexei-led/codex-router/internal/policy"
	"github.com/alexei-led/codex-router/internal/registry"
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
		Classifier: classifier.Health{Latency: 412 * time.Millisecond}, ClassifierLabel: "Jev", ClassifierModel: "jev-1.13.0", ClassifierMedian: 388 * time.Millisecond,
		Routes:  map[config.Tier]config.Route{config.TierHigh: sol61, config.TierMedium: sol, config.TierLow: luna, config.TierMicro: micro},
		Group:   catalog.Group{CompHash: "3000"},
		Blocked: []string{"gpt-5.5"},
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
				checkGolden(t, fmt.Sprintf("%s-%d.golden", tab, width), renderPlain(fixture(), tab, width))
			})
		}
	}
}

func TestRenderReflectsTheSnapshot(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*route.Snapshot)
		tab     Tab
		want    []string
		without []string
	}{
		{"manual holds", func(s *route.Snapshot) { s.Mode = route.ModeManual }, TabNow, []string{"◐ manual", "picked with /model"}, nil},
		{"paused", func(s *route.Snapshot) { s.Mode = route.ModePaused }, TabNow, []string{"○ paused", "unchanged"}, nil},
		{"pinned", func(s *route.Snapshot) { p := config.TierLow; s.Pin = &p }, TabNow, []string{"▌low", "pinned"}, nil},
		{"classifier paused", func(s *route.Snapshot) {
			s.Classifier = classifier.Health{Failures: 3, LastError: "http 503", PausedUntil: s.At.Add(42 * time.Second)}
		}, TabClassifier, []string{"○ Jev · jev-1.13.0", "last error: http 503", "resumes in 42s", "forwarded unchanged"}, nil},
		{"classifier failing", func(s *route.Snapshot) { s.Classifier = classifier.Health{Failures: 1, LastError: "timeout"} },
			TabClassifier, []string{"◉ Jev", "consecutive failures 1", "last error: timeout"}, []string{"resumes in"}},
		{"classifier never called", func(s *route.Snapshot) { s.Classifier, s.ClassifierMedian = classifier.Health{}, 0 },
			TabClassifier, []string{"ready · no calls yet"}, []string{"latency", "failures"}},
		{"classifier unnamed", func(s *route.Snapshot) { s.ClassifierLabel, s.ClassifierModel = "", "" }, TabClassifier, []string{"◉ Classifier\n"}, nil},
		{"classifier off header", func(s *route.Snapshot) { s.ClassifierOff = true }, TabNow,
			[]string{"◉ pins only · gpt-6.1-sol high · Jev off"}, []string{"◉ on"}},
		{"classifier off tab", func(s *route.Snapshot) { s.ClassifierOff, s.ClassifierOffReason = true, "TYPESAFE_API_KEY is not set" }, TabClassifier,
			[]string{"○ Jev · jev-1.13.0", "off · TYPESAFE_API_KEY is not set", "only pins (p) and the context guard change it"}, []string{"latency"}},
		{"classifier off tab without a name", func(s *route.Snapshot) { s.ClassifierOff, s.ClassifierLabel, s.ClassifierModel = true, "", "" }, TabClassifier,
			[]string{"○ Classifier\n", "off\n"}, nil},
		{"classifier off next turn", func(s *route.Snapshot) { s.ClassifierOff, s.Last = true, nil }, TabNow,
			[]string{"classifier off · requests keep gpt-6.1-sol unless pinned (p)"}, []string{"no routed request yet"}},
		{"classifier off policy", func(s *route.Snapshot) { s.ClassifierOff = true }, TabPolicy,
			[]string{"no effect while the classifier is off"}, []string{"c preset"}},
		{"classifier on policy", func(*route.Snapshot) {}, TabPolicy, []string{"c preset · q quit"}, []string{"no effect"}},
		{"header shows the footer route", func(*route.Snapshot) {}, TabNow, []string{"ROUTING ◉ on · gpt-6.1-sol high · Jev ready\n"}, []string{"footer synced", "412 ms ·"}},
		{"footer not synced", func(s *route.Snapshot) { s.FooterSynced = false }, TabNow, []string{"Jev ready · footer not synced", "not synced yet"}, nil},
		{"footer in sync", func(*route.Snapshot) {}, TabNow, []string{"footer shows gpt-6.1-sol · high · in sync"}, []string{"thread/settings", "update"}},
		{"unknown reason code", func(s *route.Snapshot) { s.Last.Why, s.Last.Reason = "", "mystery" }, TabNow,
			[]string{"reason not shown by this panel version"}, []string{"mystery"}},
		{"known reason code", func(s *route.Snapshot) { s.Last.Why, s.Last.Reason = "", policy.ReasonDowngradeHeld }, TabNow,
			[]string{"waiting to go down"}, []string{"downgrade-held"}},
		{"routes avoid internal names", func(*route.Snapshot) {}, TabRoutes,
			[]string{"gpt-6.1-sol's compatibility group", "gpt-5.5 · outside gpt-6.1-sol's compatibility group", "escalate: same test failure twice"},
			[]string{"comp_hash", "3000", "escalation\n"}},
		{"routes group unknown", func(s *route.Snapshot) { s.Group = catalog.Group{} }, TabRoutes, []string{"compatibility group unknown"}, nil},
		{"kept decision", func(s *route.Snapshot) {
			kept := route.Decision{Tier: config.TierMicro, Route: s.Baseline, Original: s.Baseline, Reason: policy.ReasonNoClassifier, At: at(18, 40)}
			s.Recent, s.Last = append(s.Recent, kept), &kept
		}, TabRoutes, []string{"18:40 kept gpt-6.1-sol · high", "classifier off; request keeps Codex's model"}, []string{"▌micro gpt-6.1-sol"}},
		{"kept decision next turn", func(s *route.Snapshot) {
			s.Last = &route.Decision{Tier: config.TierMicro, Route: s.Baseline, Original: s.Baseline, Reason: policy.ReasonError, At: at(18, 40)}
		}, TabNow, []string{"Next turn  kept  gpt-6.1-sol · high"}, []string{"▌micro", "▶"}},
		{"empty session", func(s *route.Snapshot) { s.Stats = route.Stats{}; s.Last = nil }, TabNow,
			[]string{"no routed replies yet", "no routed request yet"}, []string{"Cache", "no replies yet"}},
		{"empty session usage", func(s *route.Snapshot) { s.Stats = route.Stats{} }, TabUsage,
			[]string{"no replies yet"}, []string{"cache reuse", "savings need", "prices"}},
		{"prices missing", func(s *route.Snapshot) { s.Stats.RoutedUSD, s.Stats.BaselineUSD = 0, 0 }, TabUsage,
			[]string{`savings need "prices" in the config`}, []string{"list-price baseline"}},
		{"routing costs more", func(s *route.Snapshot) { s.Stats.RoutedUSD = 3 }, TabUsage, []string{"Routing cost"}, nil},
		{"tier the answer left out", func(s *route.Snapshot) { delete(s.Last.Reading.Tiers, config.TierMicro) }, TabNow, []string{"low 9%", "micro —"}, nil},
		{"uncertain activity answer", func(s *route.Snapshot) { s.Last.Reading.Activity, s.Last.Reading.ActivityP = "uncertain", 0.5 }, TabNow, []string{"uncertain 50%"}, nil},
		{"manual finding", func(s *route.Snapshot) {
			s.Worth = []route.Finding{{Kind: route.FindingManual, Text: "footer picked with /model"}}
		}, TabNow, []string{"WORTH A LOOK", "footer picked with /model · space resumes"}, nil},
		{"no last reply reading", func(s *route.Snapshot) { s.Stats.LastInputTokens = 0 }, TabNow, []string{"97% reused this session"}, nil},
		{"no classifier answer", func(s *route.Snapshot) { s.Last.Reading = nil }, TabNow, nil, []string{"high 18%", "applies at", "code 15%"}},
		{"nothing worth a look", func(s *route.Snapshot) { s.Worth = nil }, TabNow, nil, []string{"WORTH A LOOK"}},
		{"no timed answers", func(s *route.Snapshot) { s.ClassifierMedian = 0 }, TabClassifier, nil, []string{"median"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := fixture()
			tt.mutate(&s)
			got := renderPlain(s, tt.tab, 100)
			for _, w := range tt.want {
				if !strings.Contains(got, w) {
					t.Errorf("missing %q in\n%s", w, got)
				}
			}
			for _, w := range tt.without {
				if strings.Contains(got, w) {
					t.Errorf("unexpected %q in\n%s", w, got)
				}
			}
		})
	}
}

func TestRenderIsWidthBound(t *testing.T) {
	s := fixture()
	for _, tab := range Tabs {
		for _, width := range []int{40, 80, 120} {
			for _, line := range strings.Split(renderPlain(s, tab, width), "\n") {
				if n := utf8.RuneCountInString(line); n > width {
					t.Errorf("%s/%d: line of %d runes: %q", tab, width, n, line)
				}
			}
		}
	}
}

func TestPlainHasNoEscapesAndStyledDoes(t *testing.T) {
	s := fixture()
	if strings.Contains(renderPlain(s, TabNow, 100), "\x1b") {
		t.Error("plain render contains ANSI")
	}
	if !strings.Contains(render(view{snap: s, tab: TabNow, width: 100, styled: true}), "\x1b[") {
		t.Error("styled render has no ANSI")
	}
}

var testHome = home.Home{ID: "h1", Path: "/u/.codex", Name: "default"}

const testSession = "h1/th"

func connected(t *testing.T, snap route.Snapshot) (*model, *fakeConn) {
	t.Helper()
	tf := newTestFleet(t)
	f := tf.add("e1", registry.KindSession, testHome)
	tf.poll()
	f.sessions <- []route.SessionInfo{{ID: testSession, Home: testHome.ID, ThreadID: "th", Mode: snap.Mode}}
	tf.settle(func(fleetState) bool { return slices.Equal(f.subs(), []string{testSession}) })
	f.updates <- snap
	tf.settle(func(st fleetState) bool { return st.snap != nil })
	m := newModel(tf.fleet)
	m.Update(fleetMsg{})
	return m, f
}

func refresh(t *testing.T, m *model, ok func(fleetState) bool) {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for m.Update(fleetMsg{}); !ok(m.st); m.Update(fleetMsg{}) {
		select {
		case <-m.f.changed:
		case <-deadline:
			t.Fatalf("state did not settle: %+v", m.st)
		}
	}
}

func run(m *model, cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	if msg, ok := cmd().(sentMsg); ok {
		m.Update(msg)
	}
}

var namedKeys = map[string]rune{
	"tab": tea.KeyTab, "up": tea.KeyUp, "down": tea.KeyDown, "enter": tea.KeyEnter, " ": tea.KeySpace,
}

func press(m *model, s string) tea.Cmd {
	k := tea.KeyPressMsg{Text: s, Code: []rune(s)[0]}
	if s == "shift+tab" {
		k = tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift}
	} else if c, ok := namedKeys[s]; ok {
		k = tea.KeyPressMsg{Code: c}
		if c == tea.KeySpace {
			k.Text = " "
		}
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
		{"space pauses the session", func(*route.Snapshot) {}, TabNow, " ", route.Command{Op: route.OpPause, Session: testSession}},
		{"space resumes paused", func(s *route.Snapshot) { s.Mode = route.ModePaused }, TabNow, " ", route.Command{Op: route.OpResume, Session: testSession}},
		{"space resumes manual", func(s *route.Snapshot) { s.Mode = route.ModeManual }, TabNow, " ", route.Command{Op: route.OpResume, Session: testSession}},
		{"P pauses the home", func(*route.Snapshot) {}, TabNow, "P", route.Command{Op: route.OpPause, Scope: route.ScopeHome}},
		{"P resumes a paused home", func(s *route.Snapshot) { s.Mode = route.ModePaused }, TabNow, "P", route.Command{Op: route.OpResume, Scope: route.ScopeHome}},
		{"preset cycles on policy", func(*route.Snapshot) {}, TabPolicy, "c", route.Command{Op: route.OpPreset, Scope: route.ScopeHome, Preset: config.PresetQuality}},
		{"preset wraps", func(s *route.Snapshot) { s.Preset = config.PresetQuality }, TabPolicy, "c", route.Command{Op: route.OpPreset, Scope: route.ScopeHome, Preset: config.PresetThrifty}},
		{"preset key outside policy sends nothing", func(*route.Snapshot) {}, TabNow, "c", route.Command{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := fixture()
			tt.setup(&s)
			m, f := connected(t, s)
			m.tab = tt.tab
			run(m, press(m, tt.key))
			var want []route.Command
			if tt.want != (route.Command{}) {
				want = []route.Command{tt.want}
			}
			if got := f.sentCmds(); !slices.Equal(got, want) {
				t.Errorf("sent %+v, want %+v", got, want)
			}
		})
	}
}

func TestTabKeys(t *testing.T) {
	tests := []struct {
		name   string
		locked bool
		keys   []string
		want   Tab
	}{
		{"digit", false, []string{"4"}, TabClassifier},
		{"tab moves on", false, []string{"4", "tab"}, TabUsage},
		{"tab wraps to sessions", false, []string{"5", "tab"}, TabSessions},
		{"zero opens sessions", false, []string{"0"}, TabSessions},
		{"shift+tab from sessions", false, []string{"0", "shift+tab"}, TabUsage},
		{"locked tab wraps to now", true, []string{"5", "tab"}, TabNow},
		{"locked has no sessions tab", true, []string{"2", "0"}, TabRoutes},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, _ := connected(t, fixture())
			if tt.locked {
				m.f.locked = testSession
			}
			for _, k := range tt.keys {
				press(m, k)
			}
			if m.tab != tt.want {
				t.Errorf("tab = %s, want %s", m.tab, tt.want)
			}
		})
	}
}

func TestQuitKey(t *testing.T) {
	m, _ := connected(t, fixture())
	cmd := press(m, "q")
	if cmd == nil {
		t.Fatal("no quit command")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Error("q does not quit")
	}
}

func TestDisconnectedHeaderBlocksCommands(t *testing.T) {
	m, f := connected(t, fixture())
	if strings.Contains(m.View().Content, "disconnected") {
		t.Fatal("connected view says disconnected")
	}
	f.lose()
	refresh(t, m, func(st fleetState) bool { return st.disconnected })
	view := m.View().Content
	for _, w := range []string{"✕ disconnected", "retrying", "last snapshot 18:42:00"} {
		if !strings.Contains(view, w) {
			t.Errorf("view missing %q", w)
		}
	}
	if k := press(m, "p"); k != nil || !strings.Contains(m.View().Content, "command not sent") || len(f.sentCmds()) != 0 {
		t.Error("command went out while disconnected")
	}
}

func TestPanelWithoutSessionsWaits(t *testing.T) {
	m := newModel(newTestFleet(t).fleet)
	m.Update(fleetMsg{})
	if v := m.View().Content; !strings.Contains(v, "no sessions") || strings.Contains(v, "disconnected") {
		t.Errorf("view:\n%s", v)
	}
	if k := press(m, "P"); k != nil || !strings.Contains(m.View().Content, "command not sent") {
		t.Error("home command without a focus")
	}
}

func TestRejectedCommandShowsNotice(t *testing.T) {
	m, f := connected(t, fixture())
	f.setErr(errors.New("unknown tier"))
	run(m, press(m, "p"))
	if m.st.disconnected || !strings.Contains(m.View().Content, "command failed: unknown tier") {
		t.Errorf("view:\n%s", m.View().Content)
	}
}

func TestWindowSizeSetsWidth(t *testing.T) {
	m, _ := connected(t, fixture())
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	if m.width != 120 {
		t.Errorf("width = %d", m.width)
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

func usd(v float64) *float64 { return &v }

func info(h home.Home, thread, label string, mod func(*route.SessionInfo)) fleetSession {
	s := route.SessionInfo{
		ID: control.SessionID(h.ID, thread), Home: h.ID, ThreadID: thread, Label: h.Name + " · " + label,
		Cwd: "/src/" + thread, Mode: route.ModeOn, FooterSynced: true,
		Route: config.Route{Model: "gpt-6-sol", Effort: config.EffortMedium}, LastActive: at(18, 0),
	}
	if mod != nil {
		mod(&s)
	}
	return fleetSession{SessionInfo: s, endpoint: "ep-" + h.Name}
}

func fleetFixture() fleetState {
	sw := &route.Switch{At: at(18, 41), From: config.TierMedium, To: config.TierHigh, Activity: config.ActivityDebug}
	return fleetState{
		focus: control.SessionID(homeWork.ID, "t3"),
		homes: []fleetHome{
			{
				home:      homeDefault,
				endpoints: []fleetEndpointInfo{{id: "d1", kind: registry.KindSession}},
				sessions: []fleetSession{
					info(homeDefault, "t1", "codex-router · main", func(s *route.SessionInfo) {
						s.Replies, s.Switches, s.Strip, s.LastSwitch = 48, 9, strip("lllllmmmmmlllmmmmmmmmhhh"), sw
						s.Tier, s.SavedUSD, s.SpentUSD = config.TierHigh, usd(0.84), usd(1.87)
					}),
					info(homeDefault, "t2", "notes", func(s *route.SessionInfo) {
						s.Replies, s.Strip, s.Mode, s.Attention = 3, strip("lll"), route.ModePaused, route.AttentionPaused
						s.SavedUSD, s.SpentUSD = usd(-0.02), usd(0.10)
					}),
				},
			},
			{
				home:      homeWork,
				endpoints: []fleetEndpointInfo{{id: "w1", kind: registry.KindSession}, {id: "w2", kind: registry.KindBrain, up: 2*time.Hour + 12*time.Minute}},
				sessions: []fleetSession{
					info(homeWork, "t3", "api · feat/auth", func(s *route.SessionInfo) {
						s.Replies, s.Switches, s.Strip, s.Mode, s.Attention = 12, 2, strip("mmmmllllmmmm"), route.ModeManual, route.AttentionManual
					}),
					info(homeWork, "t4", "api · fix/retry", func(s *route.SessionInfo) {
						s.Replies, s.Switches, s.Strip, s.LastSwitch, s.Attention = 7, 1, strip("mmmmhhh"), sw, route.AttentionEscalation
						s.Route = config.Route{Model: "gpt-6.1-sol", Effort: config.EffortHigh}
					}),
					info(homeWork, "t5", "infra · main", nil),
				},
			},
		},
	}
}

func lockedFixture() fleetState {
	st := fleetFixture()
	h := st.homes[1]
	h.endpoints, h.sessions = h.endpoints[:1], h.sessions[:1]
	st.homes = []fleetHome{h}
	return st
}

func manyFixture() fleetState {
	st := fleetState{}
	for i, h := range []home.Home{homeDefault, homeWork} {
		g := fleetHome{home: h, endpoints: []fleetEndpointInfo{{id: h.Name, kind: registry.KindSession}}}
		for j := range 10 {
			n := i*10 + j + 1
			g.sessions = append(g.sessions, info(h, fmt.Sprintf("t%02d", n), fmt.Sprintf("repo-%02d · main", n), func(s *route.SessionInfo) {
				s.Replies, s.Strip = n, strip(strings.Repeat("m", n))
			}))
		}
		st.homes = append(st.homes, g)
	}
	st.focus = st.homes[1].sessions[4].ID
	return st
}

func disconnectedFixture() fleetState {
	st := fleetFixture()
	st.disconnected = true
	work := &st.homes[1]
	work.endpoints[0].lost = true
	for i := range work.sessions[:2] {
		work.sessions[i].disconnected = true
	}
	return st
}

func brainFixture() fleetState {
	st := fleetFixture()
	work := &st.homes[1]
	work.endpoints = work.endpoints[1:]
	work.sessions[1].Attention = route.AttentionUnknown
	return st
}

func brainDownFixture() fleetState {
	st := brainFixture()
	st.disconnected = true
	work := &st.homes[1]
	work.endpoints[0].lost, work.endpoints[0].up = true, 0
	for i := range work.sessions {
		work.sessions[i].disconnected = true
	}
	return st
}

func stateLostFixture() fleetState {
	st := brainFixture()
	work := &st.homes[1]
	for i := range work.sessions {
		work.sessions[i].Mode, work.sessions[i].Attention = route.ModePaused, route.AttentionStateLost
	}
	return st
}

func stateLostSnapshot() route.Snapshot {
	s := fixture()
	s.Mode, s.StateLost, s.Pin = route.ModePaused, true, nil
	s.Worth = append([]route.Finding{{Kind: route.FindingState, Text: "state lost: saved pause, pin and preset unreadable; routing paused until resume"}}, s.Worth...)
	return s
}

type fleetCase struct {
	name   string
	st     fleetState
	tab    Tab
	locked bool
	height int
	snap   *route.Snapshot
	wide   bool
}

func fleetCases() []fleetCase {
	ended := lockedFixture()
	ended.ended = true
	lost := stateLostSnapshot()
	return []fleetCase{
		{name: "all-now", st: fleetFixture(), tab: TabNow},
		{name: "all-sessions", st: fleetFixture(), tab: TabSessions, wide: true},
		{name: "locked", st: lockedFixture(), tab: TabNow, locked: true},
		{name: "ended", st: ended, tab: TabNow, locked: true},
		{name: "disconnected", st: disconnectedFixture(), tab: TabSessions},
		{name: "empty", st: fleetState{disconnected: true}, tab: TabNow},
		{name: "scroll", st: manyFixture(), tab: TabSessions, height: 24},
		{name: "brain-up", st: brainFixture(), tab: TabSessions},
		{name: "brain-down", st: brainDownFixture(), tab: TabSessions},
		{name: "state-lost", st: stateLostFixture(), tab: TabNow, snap: &lost},
		{name: "state-lost-sessions", st: stateLostFixture(), tab: TabSessions, snap: &lost},
	}
}

func renderCase(c fleetCase, width int) string {
	st, snap := c.st, fixture()
	if c.snap != nil {
		snap = *c.snap
	}
	return render(view{snap: snap, tab: c.tab, width: width, height: c.height, disconnected: st.disconnected, fleet: &st, locked: c.locked})
}

func checkGolden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name)
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
}

func TestRenderFleetGolden(t *testing.T) {
	for _, c := range fleetCases() {
		widths := []int{80}
		if c.wide {
			widths = append(widths, 140)
		}
		for _, width := range widths {
			t.Run(fmt.Sprintf("%s-%d", c.name, width), func(t *testing.T) {
				checkGolden(t, fmt.Sprintf("fleet-%s-%d.golden", c.name, width), renderCase(c, width))
			})
		}
	}
}

func TestRenderMixedFleetGolden(t *testing.T) {
	tf := newTestFleet(t)
	launcher := tf.add("w-launcher", registry.KindSession, homeWork)
	brain := tf.add("w-brain", registry.KindBrain, homeWork)
	tf.started("w-brain", at(18, 0).Add(-3*time.Hour-20*time.Minute))
	other := tf.add("d-launcher", registry.KindSession, homeDefault)
	tf.poll()
	launcher.sessions <- []route.SessionInfo{session(homeWork, "t1", "/src/api", at(17, 50))}
	brain.sessions <- []route.SessionInfo{
		session(homeWork, "t2", "/src/infra", at(17, 58)),
		session(homeWork, "t3", "/src/web", at(17, 40)),
	}
	other.sessions <- []route.SessionInfo{session(homeDefault, "t4", "/src/notes", at(17, 30))}
	st := tf.settleCount(4)
	if len(st.homes) != 2 || len(st.homes[1].sessions) != 3 || len(st.homes[1].endpoints) != 2 {
		t.Fatalf("work home not one group of a launcher and a brain: %+v", st.homes)
	}
	for _, width := range []int{80, 140} {
		t.Run(strconv.Itoa(width), func(t *testing.T) {
			got := render(view{snap: fixture(), tab: TabSessions, width: width, fleet: &st})
			checkGolden(t, fmt.Sprintf("fleet-mixed-%d.golden", width), got)
		})
	}
}

func TestUptime(t *testing.T) {
	tests := []struct {
		d    time.Duration
		want string
	}{
		{0, "0s"},
		{59 * time.Second, "59s"},
		{time.Minute, "1m"},
		{59*time.Minute + 59*time.Second, "59m"},
		{2*time.Hour + 12*time.Minute, "2h"},
		{23*time.Hour + 59*time.Minute, "23h"},
		{50 * time.Hour, "2d"},
	}
	for _, tt := range tests {
		if got := uptime(tt.d); got != tt.want {
			t.Errorf("uptime(%v) = %q, want %q", tt.d, got, tt.want)
		}
	}
}

func TestSessionsTabBrainState(t *testing.T) {
	tests := []struct {
		name string
		st   fleetState
		want string
	}{
		{"up", brainFixture(), "● work · 1 endpoint · brain up 2h · 19 replies"},
		{"up, start unknown", func() fleetState {
			st := brainFixture()
			st.homes[1].endpoints[0].up = 0
			return st
		}(), "● work · 1 endpoint · brain up · 19 replies"},
		{"down", brainDownFixture(), "● work · 1 endpoint · brain down: default model · 19 replies"},
		{"down, no sessions left", func() fleetState {
			st := brainDownFixture()
			st.homes[1].sessions = nil
			return st
		}(), "● work · 1 endpoint · brain down: default model · 0 replies"},
		{"down with a lost launcher", func() fleetState {
			st := brainDownFixture()
			st.homes[1].endpoints = append(st.homes[1].endpoints, fleetEndpointInfo{id: "w1", kind: registry.KindSession, lost: true})
			return st
		}(), "● work · 2 endpoints · brain down: default model · 1 lost · 19 replies"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := render(view{tab: TabSessions, width: 140, fleet: &tt.st}); !strings.Contains(got, tt.want) {
				t.Errorf("missing %q in\n%s", tt.want, got)
			}
		})
	}
}

func TestSessionsTabListsAHomeWithoutSessions(t *testing.T) {
	st := brainDownFixture()
	st.homes = st.homes[1:]
	st.homes[0].sessions, st.focus = nil, ""
	got := render(view{tab: TabSessions, width: 80, fleet: &st})
	for _, want := range []string{"0 sessions in 1 home", "brain down: default model"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in\n%s", want, got)
		}
	}
}

func TestRenderFleetIsWidthAndHeightBound(t *testing.T) {
	for _, c := range fleetCases() {
		for _, width := range []int{40, 80, 99, 100, 140} {
			got := renderCase(c, width)
			lines := strings.Split(strings.TrimSuffix(got, "\n"), "\n")
			for _, line := range lines {
				if n := utf8.RuneCountInString(line); n > width {
					t.Errorf("%s/%d: line of %d runes: %q", c.name, width, n, line)
				}
			}
			if c.height > 0 && len(lines) > c.height {
				t.Errorf("%s/%d: %d lines, height %d", c.name, width, len(lines), c.height)
			}
		}
	}
}

var sgr = regexp.MustCompile("\x1b\\[[0-9;]*m")

func fleetView(st fleetState) FleetView {
	v := FleetView{Focus: st.focus, Snap: fixture(), Width: 140}
	for _, h := range st.homes {
		v.Homes = append(v.Homes, h.home)
		for _, s := range h.sessions {
			v.Sessions = append(v.Sessions, s.SessionInfo)
		}
	}
	return v
}

func TestRenderFleetViewMatchesThePanel(t *testing.T) {
	st := fleetFixture()
	for i := range st.homes {
		h := &st.homes[i]
		h.endpoints = nil
		for _, s := range h.sessions {
			h.endpoints = append(h.endpoints, fleetEndpointInfo{id: s.ID, kind: registry.KindSession})
		}
	}
	for _, tab := range []Tab{TabSessions, TabNow} {
		v := fleetView(st)
		v.Tab = tab
		got := sgr.ReplaceAllString(RenderFleetView(v), "")
		want := render(view{snap: fixture(), tab: tab, width: 140, fleet: &st})
		if got != want {
			t.Errorf("%s: RenderFleetView differs from the panel\n--- got\n%s--- want\n%s", tab, got, want)
		}
	}
}

func TestRenderFleetViewShowsABrainHomeAndALockedPanel(t *testing.T) {
	v := fleetView(fleetFixture())
	v.Brains, v.Tab = map[string]time.Duration{v.Homes[0].ID: 2 * time.Hour}, TabSessions
	got := sgr.ReplaceAllString(RenderFleetView(v), "")
	if !strings.Contains(got, "1 endpoint · brain up 2h") {
		t.Errorf("brain home line missing:\n%s", got)
	}
	v.Tab, v.Locked = TabNow, true
	got = sgr.ReplaceAllString(RenderFleetView(v), "")
	if strings.Contains(got, "Sessions 0") || strings.Contains(got, "▸ 1/") {
		t.Errorf("locked panel shows the top bar or the Sessions tab:\n%s", got)
	}
}

func topBar(st fleetState, width int) string {
	c := &canvas{width: width}
	c.topBar(st)
	return c.out[0]
}

func TestTopBarMarksAttention(t *testing.T) {
	tests := []struct {
		attention route.Attention
		mark      string
	}{
		{route.AttentionManual, " ◐"},
		{route.AttentionPaused, " ‖"},
		{route.AttentionEscalation, " ↑"},
		{route.AttentionUnknown, " ?"},
		{route.AttentionStateLost, " !"},
		{"from-a-newer-router", " ?"},
		{"", ""},
	}
	for _, tt := range tests {
		t.Run(string(tt.attention), func(t *testing.T) {
			st := lockedFixture()
			st.homes[0].sessions[0].Attention = tt.attention
			want := "◂ ● work · api · feat/auth" + tt.mark + " "
			if got := topBar(st, 80); !strings.HasPrefix(got, want) || !strings.HasSuffix(got, "▸ 1/1") {
				t.Errorf("bar %q, want prefix %q", got, want)
			}
		})
	}
}

func TestTopBarStartsAtTheFocusAndFitsTheWidth(t *testing.T) {
	st := fleetFixture()
	wide := topBar(st, 200)
	if want := "◂ ● work · api · feat/auth ◐  ● work · api · fix/retry ↑  ● work · infra · main  ● default · codex-router · main  ● default · notes ‖"; !strings.HasPrefix(wide, want) || strings.Contains(wide, "…") {
		t.Errorf("wide bar %q", wide)
	}
	for _, width := range []int{40, 60, 80} {
		got := topBar(st, width)
		if utf8.RuneCountInString(got) != width || !strings.HasSuffix(got, "▸ 3/5") || !strings.Contains(got, "…") {
			t.Errorf("width %d: %q", width, got)
		}
	}
	st.homes[1].sessions[0].Label = "work · " + strings.Repeat("x", 100)
	if got := topBar(st, 40); utf8.RuneCountInString(got) != 40 || !strings.Contains(got, "x…") {
		t.Errorf("long focus label: %q", got)
	}
}

func TestSessionsTabColumnsByWidth(t *testing.T) {
	st := fleetFixture()
	for _, tt := range []struct {
		width int
		wide  bool
	}{{80, false}, {99, false}, {100, true}, {140, true}} {
		got := render(view{tab: TabSessions, width: tt.width, fleet: &st})
		if wide := strings.Contains(got, "REPLIES") && strings.Contains(got, "LAST SWITCH"); wide != tt.wide {
			t.Errorf("width %d: replies and last switch shown %v\n%s", tt.width, wide, got)
		}
	}
}

func TestSessionsTabHomeTotals(t *testing.T) {
	st := fleetFixture()
	got := render(view{tab: TabSessions, width: 140, fleet: &st})
	for _, want := range []string{
		"● default · 1 endpoint · 51 replies · 9 switches · saved ≈ $0.82",
		"● work · 2 endpoints · brain up 2h · 19 replies · 3 switches\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in\n%s", want, got)
		}
	}
	st.homes[0].sessions[0].SavedUSD = usd(-1)
	if got := render(view{tab: TabSessions, width: 140, fleet: &st}); !strings.Contains(got, "cost ≈ $1.02") {
		t.Errorf("negative savings not shown as cost:\n%s", got)
	}
}

func TestSessionsTabScrollKeepsTheCursorInView(t *testing.T) {
	st := manyFixture()
	for _, s := range st.all() {
		got := render(view{tab: TabSessions, width: 80, height: 24, fleet: &st, cursor: s.ID})
		label := strings.TrimPrefix(s.Label, homeDefault.Name+" · ")
		label = strings.TrimPrefix(label, homeWork.Name+" · ")
		if !strings.Contains(got, "▸ "+label) {
			t.Errorf("cursor %s not in view:\n%s", s.ID, got)
		}
		if n := strings.Count(got, "\n"); n > 24 {
			t.Errorf("cursor %s: %d lines", s.ID, n)
		}
	}
}

func fleetPanel(t *testing.T) (*model, *fakeConn, []route.SessionInfo) {
	t.Helper()
	tf := newTestFleet(t)
	e := tf.add("e1", registry.KindSession, homeWork)
	tf.poll()
	ss := []route.SessionInfo{
		session(homeWork, "t1", "/a", at(18, 30)),
		session(homeWork, "t2", "/b", at(18, 20)),
		session(homeWork, "t3", "/c", at(18, 10)),
	}
	e.sessions <- ss
	tf.settle(func(st fleetState) bool { return len(flat(st)) == 3 && st.focus == ss[0].ID })
	m := newModel(tf.fleet)
	m.Update(fleetMsg{})
	return m, e, ss
}

func TestBracketKeysMoveTheFocus(t *testing.T) {
	tests := []struct {
		keys []string
		want int
	}{
		{[]string{"]"}, 1},
		{[]string{"]", "]"}, 2},
		{[]string{"]", "]", "]"}, 0},
		{[]string{"["}, 2},
		{[]string{"[", "]"}, 0},
	}
	for _, tt := range tests {
		t.Run(strings.Join(tt.keys, ""), func(t *testing.T) {
			m, e, ss := fleetPanel(t)
			for _, k := range tt.keys {
				run(m, press(m, k))
			}
			want := ss[tt.want].ID
			refresh(t, m, func(st fleetState) bool { return st.focus == want })
			if subs := e.subs(); subs[len(subs)-1] != want {
				t.Errorf("subscribed %v, want last %s", subs, want)
			}
		})
	}
}

func TestSessionsTabSelectsTheFocus(t *testing.T) {
	m, _, ss := fleetPanel(t)
	press(m, "0")
	press(m, "up")
	press(m, "down")
	press(m, "down")
	press(m, "down")
	if m.cursor != ss[2].ID {
		t.Fatalf("cursor %s, want %s", m.cursor, ss[2].ID)
	}
	v := m.view()
	v.styled = false
	if got := render(v); !strings.Contains(got, "▸ c ") {
		t.Errorf("cursor row not marked:\n%s", got)
	}
	run(m, press(m, "enter"))
	if m.tab != TabNow {
		t.Errorf("tab = %s", m.tab)
	}
	refresh(t, m, func(st fleetState) bool { return st.focus == ss[2].ID })
}

func TestLockedPanelIgnoresSessionKeys(t *testing.T) {
	m, e, ss := fleetPanel(t)
	m.f.locked = ss[0].ID
	for _, k := range []string{"]", "[", "0", "down", "enter"} {
		run(m, press(m, k))
	}
	if m.tab != TabNow || m.st.focus != ss[0].ID || len(e.subs()) != 1 {
		t.Errorf("tab %s, focus %s, subscribed %v", m.tab, m.st.focus, e.subs())
	}
	if v := m.View().Content; strings.Contains(v, "◂") || strings.Contains(v, "Sessions 0") {
		t.Errorf("locked view shows the session bar:\n%s", v)
	}
}

func TestEndedPanelQuits(t *testing.T) {
	tf := newTestFleet(t)
	locked := session(homeWork, "t1", "/r", at(18, 1))
	tf.locked = locked.ID
	e := tf.add("e1", registry.KindSession, homeWork)
	tf.poll()
	e.sessions <- []route.SessionInfo{locked}
	tf.settleCount(1)
	m := newModel(tf.fleet)
	m.Update(fleetMsg{})
	if m.ending {
		t.Fatal("live session scheduled to end")
	}

	e.sessions <- nil
	refresh(t, m, func(st fleetState) bool { return st.ended })
	if !m.ending || !strings.Contains(m.View().Content, "■ ended") {
		t.Fatalf("ending %v, view:\n%s", m.ending, m.View().Content)
	}
	_, cmd := m.Update(endedMsg{})
	if cmd == nil {
		t.Fatal("no quit after ended")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Error("ended panel does not quit")
	}
}

func TestEndedExitCancelsWhenTheSessionReturns(t *testing.T) {
	m, _ := connected(t, fixture())
	m.st.ended, m.ending = false, true
	if _, cmd := m.Update(endedMsg{}); cmd != nil {
		t.Error("quit although the session is back")
	}
}

func renderPlain(s route.Snapshot, tab Tab, width int) string {
	return render(view{snap: s, tab: tab, width: width})
}

func TestKeyHintsFollowTheState(t *testing.T) {
	one := lockedFixture()
	one.focus = one.homes[0].sessions[0].ID
	paused := lockedFixture()
	for i := range paused.homes[0].sessions {
		paused.homes[0].sessions[i].Mode = route.ModePaused
	}
	paused.focus = paused.homes[0].sessions[0].ID
	many := fleetFixture()
	one.homes[0].sessions = one.homes[0].sessions[:1]
	manual := fixture()
	manual.Mode = route.ModeManual
	off := fixture()
	off.ClassifierOff = true
	tests := []struct {
		name string
		v    view
		want string
	}{
		{"several sessions", view{snap: fixture(), tab: TabNow, fleet: &many}, "[ ] session · 0 list · 1-5 tabs · p pin next · space pause · P pause home · q quit"},
		{"one session hides the session keys", view{snap: fixture(), tab: TabNow, fleet: &one}, "0 list · 1-5 tabs · p pin next · space pause · P pause home · q quit"},
		{"manual resumes", view{snap: manual, tab: TabNow, fleet: &many}, "[ ] session · 0 list · 1-5 tabs · p pin next · space resume · P pause home · q quit"},
		{"a paused home resumes", view{snap: fixture(), tab: TabNow, fleet: &paused}, "0 list · 1-5 tabs · p pin next · space pause · P resume home · q quit"},
		{"policy offers presets before quit", view{snap: fixture(), tab: TabPolicy, fleet: &many}, "[ ] session · 0 list · 1-5 tabs · p pin next · space pause · P pause home · c preset · q quit"},
		{"policy without a classifier", view{snap: off, tab: TabPolicy, fleet: &many}, "[ ] session · 0 list · 1-5 tabs · p pin next · space pause · P pause home · q quit"},
		{"sessions tab acts on the focused session", view{snap: fixture(), tab: TabSessions, fleet: &many}, "↑↓ select · enter focus · [ ] session · 1-5 tabs · space pause focused · P pause focused home · q quit"},
		{"locked panel", view{snap: fixture(), tab: TabNow, fleet: &one, locked: true}, "1-5 tabs · p pin next · space pause · P pause home · q quit"},
		{"disconnected keeps navigation only", view{snap: fixture(), tab: TabNow, fleet: &many, disconnected: true}, "[ ] session · 0 list · 1-5 tabs · q quit"},
		{"no sessions", view{tab: TabNow, fleet: &fleetState{}}, "q quit"},
		{"bare snapshot", view{snap: manual, tab: TabNow}, "1-5 tabs · tab next · p pin next · space resume · q quit"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := strings.Join(keyHints(tt.v), " · "); got != tt.want {
				t.Errorf("hints\n got %s\nwant %s", got, tt.want)
			}
		})
	}
}
