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
		Why:     "same test failure twice, edit in between",
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
	tiers := map[rune]config.Tier{'u': config.TierMicro, 'l': config.TierLow, 'm': config.TierMedium, 'h': config.TierHigh}
	var out []config.Tier
	for _, r := range s {
		out = append(out, tiers[r])
	}
	return out
}

func renderPlain(s route.Snapshot, tab Tab, width int) string {
	return render(view{snap: s, tab: tab, width: width})
}

func TestRenderReflectsTheSnapshot(t *testing.T) {
	low := config.TierLow
	tests := []struct {
		name    string
		mutate  func(*route.Snapshot)
		tab     Tab
		want    []string
		without []string
	}{
		{"header names the classifier", func(*route.Snapshot) {}, TabSessions, []string{"codex-router  Jev ready"}, nil},
		{"header, classifier off", func(s *route.Snapshot) { s.ClassifierOff = true }, TabSessions, []string{"codex-router  ○ Jev off"}, []string{"Jev ready"}},
		{"header, classifier unreachable", func(s *route.Snapshot) {
			s.Classifier = classifier.Health{Failures: 3, LastError: "http 503", PausedUntil: s.At.Add(42 * time.Second)}
		}, TabSessions, []string{"✕ Jev unreachable · resumes in 42s"}, []string{"Jev ready"}},
		{"header, unnamed classifier", func(s *route.Snapshot) { s.ClassifierLabel = "" }, TabSessions, []string{"classifier ready"}, nil},
		{"band of a decision", func(*route.Snapshot) {}, TabSessions,
			[]string{" HIGH  debug → gpt-6.1-sol · high", "↑ same test failure twice, edit in between", "       Jev high 18% · no classifier needed"}, nil},
		{"evidence against the bar", func(s *route.Snapshot) { s.Last.Reason = policy.ReasonUpgrade }, TabSessions, []string{"Jev high 18% < 65%"}, nil},
		{"evidence above the bar", func(s *route.Snapshot) {
			s.Last.Reason, s.Last.Reading.Tiers[config.TierHigh] = policy.ReasonUpgrade, 0.99
		}, TabSessions, []string{"Jev high 99% ≥ 65%"}, nil},
		{"manual holds", func(s *route.Snapshot) { s.Mode = route.ModeManual }, TabSessions, []string{"MANUAL", "picked with /model", "space resumes"}, []string{"HIGH"}},
		{"paused", func(s *route.Snapshot) { s.Mode = route.ModePaused }, TabSessions, []string{"PAUSED", "unchanged"}, []string{"HIGH"}},
		{"state lost", func(s *route.Snapshot) { s.Mode, s.StateLost = route.ModePaused, true }, TabSessions, []string{"STATE LOST", "P resumes the home"}, []string{"PAUSED"}},
		{"pinned", func(s *route.Snapshot) { s.Pin = &low }, TabSessions, []string{" LOW ", "pinned → gpt-6-luna · medium", "next turn only"}, []string{"▶"}},
		{"classifier off", func(s *route.Snapshot) { s.ClassifierOff, s.ClassifierOffReason = true, "TYPESAFE_API_KEY is not set" }, TabSessions,
			[]string{"PINS ONLY", "requests keep gpt-6.1-sol · high", "○ Jev off · TYPESAFE_API_KEY is not set · pin a tier with p"}, []string{"HIGH", "▶"}},
		{"classifier off, no decision", func(s *route.Snapshot) { s.ClassifierOff, s.Last = true, nil }, TabSessions, []string{"PINS ONLY", "○ Jev off · pin a tier with p"}, []string{"WAITING"}},
		{"running tier is marked", func(*route.Snapshot) {}, TabSessions, []string{"micro   low   medium  ▶high"}, nil},
		{"unknown reason code", func(s *route.Snapshot) { s.Last.Why, s.Last.Reason = "", "mystery" }, TabSessions,
			[]string{"reason not shown by this panel version"}, []string{"mystery"}},
		{"known reason code", func(s *route.Snapshot) { s.Last.Why, s.Last.Reason = "", policy.ReasonDowngradeHeld }, TabSessions,
			[]string{"waiting to go down"}, []string{"downgrade-held"}},
		{"kept decision", func(s *route.Snapshot) {
			s.Last = &route.Decision{Tier: config.TierMicro, Route: s.Baseline, Original: s.Baseline, Reason: policy.ReasonNoClassifier, At: at(18, 40)}
		}, TabSessions, []string{" KEPT ", "gpt-6.1-sol · high · classifier off; request keeps Codex's model"}, []string{"MICRO", "▶"}},
		{"no decision yet", func(s *route.Snapshot) { s.Stats = route.Stats{}; s.Last = nil }, TabSessions,
			[]string{"WAITING", "no routed request yet · baseline gpt-6.1-sol · high", "no routed replies yet"}, []string{"Cache", "HIGH"}},
		{"session summary", func(*route.Snapshot) {}, TabSessions,
			[]string{"48 replies · 9 switches · saved ≈ $0.84 (31%) vs gpt-6.1-sol · high", "per cell"}, nil},
		{"strip glyphs tell the tiers apart", func(*route.Snapshot) {}, TabSessions, []string{"▃▃▃▃▃▅▅▅▅▅▃▃▃▅▅▅▅▅▅▅▅▇▇▇▇▅▅▅▃▃▃▃▃▅▅▅▅▅▅▃▃▃▇▇▇▇"}, nil},
		{"costs more", func(s *route.Snapshot) { s.Stats.RoutedUSD = 3 }, TabSessions, []string{"cost ≈ $0.29 (11%)"}, []string{"saved"}},
		{"prices missing", func(s *route.Snapshot) { s.Stats.RoutedUSD, s.Stats.BaselineUSD = 0, 0 }, TabSessions, []string{`savings need "prices" in the config`}, nil},
		{"context and cache", func(*route.Snapshot) {}, TabSessions, []string{"Context  █░░░░░░░░░ 16%  44.6K / 272K", "Cache  98% last reply · 97% session"}, nil},
		{"no last reply reading", func(s *route.Snapshot) { s.Stats.LastInputTokens = 0 }, TabSessions, []string{"Cache  97% this session"}, []string{"last reply"}},
		{"window unknown", func(s *route.Snapshot) { s.Window = 0 }, TabSessions, []string{"window unknown"}, nil},
		{"every finding on a line of its own", func(*route.Snapshot) {}, TabSessions,
			[]string{"Look     context guard kept 2 steps", "see Policy", "         routes to gpt-5.5 never run"}, nil},
		{"manual finding", func(s *route.Snapshot) {
			s.Worth = []route.Finding{{Kind: route.FindingManual, Text: "footer picked with /model"}}
		}, TabSessions, []string{"Look     footer picked with /model · space resumes"}, nil},
		{"nothing worth a look", func(s *route.Snapshot) { s.Worth = nil }, TabSessions, nil, []string{"Look"}},

		{"preset buttons", func(*route.Snapshot) {}, TabPolicy, []string{"Preset      thrifty  ▶balanced   quality", "for every session of this home"}, []string{"no effect"}},
		{"preset has no effect without a classifier", func(s *route.Snapshot) { s.ClassifierOff = true }, TabPolicy, []string{"no effect while the classifier is off"}, []string{"for every session"}},
		{"classifier healthy", func(*route.Snapshot) {}, TabPolicy, []string{"Classifier ● Jev · jev-1.13.0 · 388 ms median · 0 failures"}, nil},
		{"classifier unreachable", func(s *route.Snapshot) {
			s.Classifier = classifier.Health{Failures: 3, LastError: "http 503", PausedUntil: s.At.Add(42 * time.Second)}
		}, TabPolicy, []string{"✕ Jev unreachable · resumes in 42s · last error: http 503", "requests are forwarded unchanged"}, []string{"median"}},
		{"classifier failing", func(s *route.Snapshot) { s.Classifier = classifier.Health{Failures: 1, LastError: "timeout"} },
			TabPolicy, []string{"● Jev · jev-1.13.0 · 388 ms median · 1 failure · last error: timeout"}, []string{"resumes in"}},
		{"classifier never called", func(s *route.Snapshot) { s.Classifier, s.ClassifierMedian = classifier.Health{}, 0 },
			TabPolicy, []string{"● Jev · jev-1.13.0 · ready · no calls yet"}, []string{"failures"}},
		{"classifier unnamed", func(s *route.Snapshot) { s.ClassifierLabel, s.ClassifierModel = "", "" }, TabPolicy, []string{"● classifier · 388 ms median"}, []string{"Jev"}},
		{"classifier off", func(s *route.Snapshot) { s.ClassifierOff, s.ClassifierOffReason = true, "TYPESAFE_API_KEY is not set" }, TabPolicy,
			[]string{"○ off · TYPESAFE_API_KEY is not set · requests keep Codex's model; only pins change it"}, []string{"median"}},
		{"classifier off without a reason", func(s *route.Snapshot) { s.ClassifierOff = true }, TabPolicy, []string{"○ off · requests keep Codex's model"}, nil},
		{"last latency when nothing is timed", func(s *route.Snapshot) { s.ClassifierMedian = 0 }, TabPolicy, []string{"412 ms last"}, []string{"median"}},
		{"routes in two columns", func(*route.Snapshot) {}, TabPolicy,
			[]string{"Routes     micro  gpt-6-luna · low", "low    gpt-6-luna · medium", "medium gpt-6-sol · medium", "high   gpt-6.1-sol · high", "group of gpt-6.1-sol"},
			[]string{"comp_hash", "3000"}},
		{"blocked models", func(*route.Snapshot) {}, TabPolicy, []string{"Blocked    gpt-5.5 · outside gpt-6.1-sol's group"}, nil},
		{"group unknown", func(s *route.Snapshot) { s.Group = catalog.Group{} }, TabPolicy, []string{"compatibility group unknown"}, nil},
		{"thresholds", func(*route.Snapshot) {}, TabPolicy,
			[]string{"up after 2 votes, bar 0.65 · down after 2 votes, bar 0.70 · min dwell 8 requests", "escalation hold 2 turns · review every 6 requests · jump bar 0.85"}, nil},

		{"savings sentence", func(*route.Snapshot) {}, TabUsage,
			[]string{"Routing saved ≈ $0.84 (31%) vs gpt-6.1-sol · high", "list-price estimate $1.87 vs $2.71 all-gpt-6.1-sol · high", "plan billing is limits, not $"}, nil},
		{"ledger", func(*route.Snapshot) {}, TabUsage,
			[]string{"48 replies since 18:12 · 9 switches · cache reuse 97%", "switches wrote 212K uncached tokens", "last switch 18:41  medium → high · debug (inside a turn)"}, nil},
		{"counts as bars", func(*route.Snapshot) {}, TabUsage,
			[]string{"Tiers      micro", "0   0%", "high      ███████", "6  12%", "Activity   code      ████████████████████████   16  33%", "ops"}, nil},
		{"empty session", func(s *route.Snapshot) { s.Stats = route.Stats{} }, TabUsage, []string{"no replies yet"}, []string{"Ledger", "cache reuse", "savings need"}},
		{"usage, prices missing", func(s *route.Snapshot) { s.Stats.RoutedUSD, s.Stats.BaselineUSD = 0, 0 }, TabUsage,
			[]string{`savings need "prices" in the config`}, []string{"list-price"}},
		{"usage, routing costs more", func(s *route.Snapshot) { s.Stats.RoutedUSD = 3 }, TabUsage, []string{"Routing cost"}, nil},
		{"no timed switch", func(s *route.Snapshot) { s.Stats.LastSwitch, s.Stats.UncachedSwitchTokens = nil, 0 }, TabUsage, nil, []string{"last switch", "uncached"}},
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

func TestPlainHasNoEscapesAndStyledDoes(t *testing.T) {
	s := fixture()
	if strings.Contains(renderPlain(s, TabSessions, 100), "\x1b") {
		t.Error("plain render contains ANSI")
	}
	if !strings.Contains(render(view{snap: s, tab: TabSessions, width: 100, styled: true}), "\x1b[") {
		t.Error("styled render has no ANSI")
	}
}

func TestLightAndDarkThemesColorTheSameText(t *testing.T) {
	dark := render(view{snap: fixture(), tab: TabSessions, width: 100, styled: true})
	light := render(view{snap: fixture(), tab: TabSessions, width: 100, styled: true, light: true})
	if dark == light {
		t.Fatal("light and dark renders are the same")
	}
	if sgr.ReplaceAllString(dark, "") != sgr.ReplaceAllString(light, "") {
		t.Error("the themes differ in more than color")
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
	"tab": tea.KeyTab, "up": tea.KeyUp, "down": tea.KeyDown, "enter": tea.KeyEnter, "esc": tea.KeyEscape, " ": tea.KeySpace,
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

func screen(m *model) string { return sgr.ReplaceAllString(m.View().Content, "") }

func pressAll(m *model, ks ...string) {
	for _, k := range ks {
		run(m, press(m, k))
	}
}

func TestDisconnectedHeaderBlocksCommands(t *testing.T) {
	m, f := connected(t, fixture())
	if strings.Contains(screen(m), "disconnected") {
		t.Fatal("connected view says disconnected")
	}
	f.lose()
	refresh(t, m, func(st fleetState) bool { return st.disconnected })
	view := screen(m)
	for _, w := range []string{"✕ disconnected", "retrying", "last snapshot 18:42:00"} {
		if !strings.Contains(view, w) {
			t.Errorf("view missing %q", w)
		}
	}
	if k := press(m, " "); k != nil || !strings.Contains(screen(m), "command not sent") || len(f.sentCmds()) != 0 {
		t.Error("command went out while disconnected")
	}
}

func TestPanelWithoutSessionsWaits(t *testing.T) {
	m := newModel(newTestFleet(t).fleet)
	m.Update(fleetMsg{})
	if v := screen(m); !strings.Contains(v, "no sessions") || !strings.Contains(v, "run codex-router in a project") || strings.Contains(v, "✕ disconnected") {
		t.Errorf("view:\n%s", v)
	}
	if k := press(m, "P"); k != nil || !strings.Contains(screen(m), "command not sent") {
		t.Error("home command without a focus")
	}
	for _, tab := range []Tab{TabPolicy, TabUsage} {
		m.tab = tab
		if v := screen(m); !strings.Contains(v, "no Codex session yet") {
			t.Errorf("%s:\n%s", tab, v)
		}
	}
}

func TestRejectedCommandShowsNotice(t *testing.T) {
	m, f := connected(t, fixture())
	f.setErr(errors.New("unknown tier"))
	pressAll(m, "p", "3")
	if m.st.disconnected || !strings.Contains(screen(m), "command failed: unknown tier") || m.toast != "" {
		t.Errorf("toast %q, view:\n%s", m.toast, screen(m))
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
	down := &route.Switch{At: at(18, 29), From: config.TierMedium, To: config.TierLow, Activity: config.ActivityExplore}
	return fleetState{
		focus: control.SessionID(homeDefault.ID, "t1"),
		homes: []fleetHome{
			{
				home:      homeDefault,
				endpoints: []fleetEndpointInfo{{id: "d1", kind: registry.KindSession}},
				sessions: []fleetSession{
					info(homeDefault, "t1", "codex-router · main", func(s *route.SessionInfo) {
						s.Replies, s.Switches, s.Strip, s.LastSwitch = 48, 9, strip("lllllmmmmmlllmmmmmmmmhhh"), sw
						s.Tier, s.Attention, s.SavedUSD, s.SpentUSD = config.TierHigh, route.AttentionEscalation, usd(0.84), usd(1.87)
						s.Route = config.Route{Model: "gpt-6.1-sol", Effort: config.EffortHigh}
					}),
					info(homeDefault, "t2", "codex-router · fix/hook", func(s *route.SessionInfo) {
						s.Replies, s.Switches, s.Strip, s.LastSwitch = 31, 4, strip("lllmmmlllllmmmuuu"), down
						s.Tier, s.Route = config.TierLow, config.Route{Model: "gpt-6-luna", Effort: config.EffortMedium}
					}),
					info(homeDefault, "t3", "notes", func(s *route.SessionInfo) {
						s.Replies, s.Strip, s.Mode, s.Attention = 3, strip("lll"), route.ModePaused, route.AttentionPaused
						s.Route = config.Route{Model: "gpt-6-luna", Effort: config.EffortMedium}
						s.SavedUSD, s.SpentUSD = usd(-0.02), usd(0.10)
					}),
				},
			},
			{
				home:      homeWork,
				endpoints: []fleetEndpointInfo{{id: "w1", kind: registry.KindSession}, {id: "w2", kind: registry.KindBrain, up: 2*time.Hour + 12*time.Minute}, {id: "w3", kind: registry.KindSession, label: "work · reflex · main", idle: true}},
				sessions: []fleetSession{
					info(homeWork, "t4", "api · feat/auth", func(s *route.SessionInfo) {
						s.Replies, s.Switches, s.Strip, s.Mode, s.Attention = 12, 2, strip("mmmmllllmmmm"), route.ModeManual, route.AttentionManual
					}),
					info(homeWork, "t5", "api · fix/retry", func(s *route.SessionInfo) {
						s.Replies, s.Switches, s.Strip, s.LastSwitch, s.Attention, s.Tier = 7, 1, strip("mmmmhhh"), sw, route.AttentionEscalation, config.TierHigh
						s.Route = config.Route{Model: "gpt-6.1-sol", Effort: config.EffortHigh}
					}),
					info(homeWork, "t6", "infra · main", nil),
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
	st.focus = h.sessions[0].ID
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
	st.focus = work.sessions[0].ID
	return st
}

func brainFixture() fleetState {
	st := fleetFixture()
	work := &st.homes[1]
	work.endpoints = work.endpoints[1:2]
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
	st.focus = work.sessions[0].ID
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
	widths []int
	mutate func(*view)
}

func fleetCases() []fleetCase {
	ended := lockedFixture()
	ended.ended = true
	lost := stateLostSnapshot()
	return []fleetCase{
		{name: "sessions", st: fleetFixture(), tab: TabSessions, widths: []int{80, 97, 130}},
		{name: "sessions-zoom", st: fleetFixture(), tab: TabSessions, widths: []int{97}, mutate: func(v *view) { v.zoom = true }},
		{name: "sessions-detail-focus", st: fleetFixture(), tab: TabSessions, widths: []int{130}, mutate: func(v *view) { v.pane = paneDetail }},
		{name: "policy", st: fleetFixture(), tab: TabPolicy, widths: []int{80, 97}},
		{name: "usage", st: fleetFixture(), tab: TabUsage, widths: []int{80, 97}},
		{name: "help", st: fleetFixture(), tab: TabSessions, widths: []int{80, 97}, mutate: func(v *view) { v.help = true }},
		{name: "popover-pin", st: fleetFixture(), tab: TabSessions, widths: []int{97}, mutate: func(v *view) { v.popover = popPin }},
		{name: "popover-preset", st: fleetFixture(), tab: TabPolicy, widths: []int{97}, mutate: func(v *view) { v.popover = popPreset }},
		{name: "toast", st: fleetFixture(), tab: TabSessions, widths: []int{97}, mutate: func(v *view) { v.toast = "pinned high for the next turn" }},
		{name: "locked", st: lockedFixture(), tab: TabSessions, locked: true, widths: []int{80, 97}},
		{name: "locked-policy", st: lockedFixture(), tab: TabPolicy, locked: true, widths: []int{80}},
		{name: "ended", st: ended, tab: TabSessions, locked: true, widths: []int{80}},
		{name: "disconnected", st: disconnectedFixture(), tab: TabSessions, widths: []int{80, 97}},
		{name: "empty", st: fleetState{disconnected: true}, tab: TabSessions, widths: []int{80}},
		{name: "scroll", st: manyFixture(), tab: TabSessions, height: 40, widths: []int{80, 130}},
		{name: "brain-up", st: brainFixture(), tab: TabSessions, widths: []int{80}},
		{name: "brain-down", st: brainDownFixture(), tab: TabSessions, widths: []int{80}},
		{name: "state-lost", st: stateLostFixture(), tab: TabSessions, snap: &lost, widths: []int{97}},
		{name: "loading", st: fleetFixture(), tab: TabSessions, widths: []int{80}, mutate: func(v *view) { v.loading = true }},
	}
}

func renderCase(c fleetCase, width int) string {
	st, snap := c.st, fixture()
	if c.snap != nil {
		snap = *c.snap
	}
	v := view{snap: snap, tab: c.tab, width: width, height: c.height, disconnected: st.disconnected, fleet: &st, locked: c.locked}
	if c.mutate != nil {
		c.mutate(&v)
	}
	return render(v)
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
		for _, width := range c.widths {
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
	waiting := tf.add("d-waiting", registry.KindSession, homeDefault)
	tf.label("d-waiting", "default · reflex · main")
	tf.poll()
	launcher.sessions <- []route.SessionInfo{session(homeWork, "t1", "/src/api", at(17, 50))}
	brain.sessions <- []route.SessionInfo{
		session(homeWork, "t2", "/src/infra", at(17, 58)),
		session(homeWork, "t3", "/src/web", at(17, 40)),
	}
	other.sessions <- []route.SessionInfo{session(homeDefault, "t4", "/src/notes", at(17, 30))}
	waiting.sessions <- nil
	st := tf.settle(func(st fleetState) bool {
		return len(flat(st)) == 4 && slices.ContainsFunc(st.homes[0].endpoints, func(e fleetEndpointInfo) bool { return e.idle })
	})
	if len(st.homes) != 2 || len(st.homes[1].sessions) != 3 || len(st.homes[1].endpoints) != 2 {
		t.Fatalf("work home not one group of a launcher and a brain: %+v", st.homes)
	}
	for _, width := range []int{80, 130} {
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

func TestListBrainState(t *testing.T) {
	tests := []struct {
		name string
		st   fleetState
		want string
	}{
		{"up", brainFixture(), "● work · brain up 2h\n"},
		{"up, start unknown", func() fleetState {
			st := brainFixture()
			st.homes[1].endpoints[0].up = 0
			return st
		}(), "● work · brain up\n"},
		{"down", brainDownFixture(), "● work · brain down: default model\n"},
		{"down, no sessions left", func() fleetState {
			st := brainDownFixture()
			st.homes[1].sessions = nil
			return st
		}(), "● work · brain down: default model\n"},
		{"down with a lost launcher", func() fleetState {
			st := brainDownFixture()
			st.homes[1].endpoints = append(st.homes[1].endpoints, fleetEndpointInfo{id: "w1", kind: registry.KindSession, lost: true})
			return st
		}(), "● work · brain down: default model · 1 lost\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := render(view{tab: TabSessions, width: 100, fleet: &tt.st}); !strings.Contains(got, tt.want) {
				t.Errorf("missing %q in\n%s", tt.want, got)
			}
		})
	}
}

func TestListShowsAHomeWithoutSessions(t *testing.T) {
	st := brainDownFixture()
	st.homes = st.homes[1:]
	st.homes[0].sessions, st.focus = nil, ""
	got := render(view{tab: TabSessions, width: 80, fleet: &st})
	for _, want := range []string{"○ no sessions", "brain down: default model"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in\n%s", want, got)
		}
	}
}

func TestListWaitingRows(t *testing.T) {
	tests := []struct {
		name string
		mod  func(*fleetEndpointInfo)
		want string
	}{
		{"launcher", func(*fleetEndpointInfo) {}, "reflex · main"},
		{"label without a home prefix", func(e *fleetEndpointInfo) { e.label = "reflex · main" }, "reflex · main"},
		{"brain", func(e *fleetEndpointInfo) { e.kind, e.label = registry.KindBrain, "" }, "brain"},
		{"no label", func(e *fleetEndpointInfo) { e.label = "" }, "session"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := fleetFixture()
			tt.mod(&st.homes[1].endpoints[2])
			got := render(view{tab: TabSessions, width: 97, fleet: &st})
			i := slices.IndexFunc(strings.Split(got, "\n"), func(l string) bool { return strings.Contains(l, waitingText) })
			line := ""
			if i >= 0 {
				line = strings.Split(got, "\n")[i]
			}
			if !strings.HasPrefix(line, "  "+tt.want+" ") || strings.Contains(line, "▸") {
				t.Errorf("waiting row %q, want it to start with %q", line, tt.want)
			}
		})
	}
	st := fleetFixture()
	st.homes[1].endpoints[2].idle = false
	if got := render(view{tab: TabSessions, width: 97, fleet: &st}); strings.Contains(got, "waiting") {
		t.Errorf("an endpoint with sessions shows as waiting:\n%s", got)
	}
}

func TestListStatesHaveGlyphs(t *testing.T) {
	tests := []struct {
		name string
		mod  func(*fleetSession)
		want string
	}{
		{"on", func(*fleetSession) {}, "● on"},
		{"paused attention", func(s *fleetSession) { s.Attention = route.AttentionPaused }, "‖ paused"},
		{"paused mode", func(s *fleetSession) { s.Mode = route.ModePaused }, "‖ paused"},
		{"manual", func(s *fleetSession) { s.Attention = route.AttentionManual }, "◐ manual"},
		{"escalation shows the tier", func(s *fleetSession) { s.Attention, s.Tier = route.AttentionEscalation, config.TierMedium }, "↑ medium"},
		{"escalation without a tier", func(s *fleetSession) { s.Attention, s.Tier = route.AttentionEscalation, "" }, "↑ escalated"},
		{"state lost", func(s *fleetSession) { s.Attention = route.AttentionStateLost }, "! state lost"},
		{"unknown", func(s *fleetSession) { s.Attention = route.AttentionUnknown }, "? unknown"},
		{"from a newer router", func(s *fleetSession) { s.Attention = "from-a-newer-router" }, "? from-a-newer-router"},
		{"disconnected beats the rest", func(s *fleetSession) { s.disconnected, s.Attention = true, route.AttentionManual }, "✕ disconnected"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := info(homeWork, "t", "api · main", nil)
			tt.mod(&s)
			if got, _ := stateOf(s); got != tt.want {
				t.Errorf("state = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestListRowCells(t *testing.T) {
	st := fleetFixture()
	got := render(view{tab: TabSessions, width: 97, fleet: &st})
	for _, want := range []string{
		"▸ codex-router · main      ↑ high    gpt-6.1-sol · high   ▅▅▅▅▅▅▅▇▇▇  18:41 ↑",
		"codex-router · fix/hook  ● on      gpt-6-luna · medium  ▃▃▃▃▅▅▅▁▁▁  18:29 ↓",
		"notes                    ‖ paused  gpt-6-luna · medium  ▃▃▃\n",
		"SESSION                  STATE     MODEL                LAST TURNS  SWITCH",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in\n%s", want, got)
		}
	}
}

func TestListColumnsByWidth(t *testing.T) {
	st := fleetFixture()
	tests := []struct {
		width                  int
		model, turns, switches bool
	}{
		{40, false, false, false},
		{56, true, false, false},
		{64, true, true, false},
		{80, true, true, true},
		{97, true, true, true},
	}
	for _, tt := range tests {
		got := render(view{tab: TabSessions, width: tt.width, fleet: &st})
		title := strings.Split(got, "\n")[2]
		for col, want := range map[string]bool{"MODEL": tt.model, "LAST TURNS": tt.turns, "SWITCH": tt.switches} {
			if has := strings.Contains(title, col); has != want {
				t.Errorf("width %d: %s shown %v in %q", tt.width, col, has, title)
			}
		}
	}
}

func TestListScrollKeepsTheSelectionInView(t *testing.T) {
	st := manyFixture()
	for _, s := range st.all() {
		st.focus = s.ID
		got := render(view{tab: TabSessions, width: 80, height: 40, fleet: &st})
		if !strings.Contains(got, "▸ "+rowLabel(s)) {
			t.Errorf("selection %s not in view:\n%s", s.ID, got)
		}
		if n := strings.Count(got, "\n"); n > 40 {
			t.Errorf("selection %s: %d lines", s.ID, n)
		}
	}
}

func TestRenderFleetIsWidthAndHeightBound(t *testing.T) {
	for _, c := range fleetCases() {
		for _, width := range []int{40, 64, 80, 97, 119, 120, 130} {
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

func trimLines(s string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight(l, " ")
	}
	return strings.Join(lines, "\n")
}

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
	for _, tab := range []Tab{TabSessions, TabPolicy, TabUsage} {
		v := fleetView(st)
		v.Tab = tab
		got := trimLines(sgr.ReplaceAllString(RenderFleetView(v), ""))
		want := render(view{snap: fixture(), tab: tab, width: 140, fleet: &st})
		if got != want {
			t.Errorf("%s: RenderFleetView differs from the panel\n--- got\n%s--- want\n%s", tab, got, want)
		}
	}
}

func TestRenderFleetViewShowsABrainHomeWaitingRoutersAndALockedPanel(t *testing.T) {
	v := fleetView(fleetFixture())
	v.Brains, v.Waiting = map[string]time.Duration{v.Homes[0].ID: 2 * time.Hour}, map[string][]string{v.Homes[1].ID: {"work · reflex · main"}}
	got := sgr.ReplaceAllString(RenderFleetView(v), "")
	for _, want := range []string{"● default · brain up 2h", "reflex · main", "○ waiting for the first prompt"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q:\n%s", want, got)
		}
	}
	v.Locked = true
	got = sgr.ReplaceAllString(RenderFleetView(v), "")
	if strings.Contains(got, "SESSION") || strings.Contains(got, "3 sessions") {
		t.Errorf("locked panel shows the list:\n%s", got)
	}
}

func TestRenderFleetViewDrawsOverlays(t *testing.T) {
	v := fleetView(fleetFixture())
	v.Help = true
	if got := sgr.ReplaceAllString(RenderFleetView(v), ""); !strings.Contains(got, "Navigate") {
		t.Errorf("no help overlay:\n%s", got)
	}
	v.Help, v.Popover = false, true
	if got := sgr.ReplaceAllString(RenderFleetView(v), ""); !strings.Contains(got, "1 micro  2 low  3 medium  4 high  0 none") {
		t.Errorf("no pin popover:\n%s", got)
	}
}

func TestDetailTitleNamesTheSessionAndWarnsAboutTheFooter(t *testing.T) {
	st := fleetFixture()
	snap := fixture()
	got := render(view{snap: snap, tab: TabSessions, width: 97, fleet: &st, locked: true})
	if !strings.Contains(got, "codex-router · main") || !strings.Contains(got, "default · bbbbbbbb\n") {
		t.Errorf("title:\n%s", got)
	}
	snap.FooterSynced = false
	got = render(view{snap: snap, tab: TabSessions, width: 97, fleet: &st, locked: true})
	if !strings.Contains(got, "default · bbbbbbbb · footer not synced") {
		t.Errorf("no sync warning:\n%s", got)
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
	if !m.ending || !strings.Contains(screen(m), "■ ended") {
		t.Fatalf("ending %v, view:\n%s", m.ending, screen(m))
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
