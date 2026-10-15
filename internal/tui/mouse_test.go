package tui

import (
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/alexei-led/codex-router/internal/config"
	"github.com/alexei-led/codex-router/internal/route"
)

func TestHit(t *testing.T) {
	a, b := action{kind: actTab, tab: TabNow}, action{kind: actTab, tab: TabRoutes}
	zones := []zone{{a, 2, 0, 5}, {b, 2, 5, 9}, {b, 4, 0, 80}}
	tests := []struct {
		name string
		x, y int
		want action
		ok   bool
	}{
		{"first cell", 0, 2, a, true},
		{"last cell of a zone", 4, 2, a, true},
		{"x1 is exclusive", 5, 2, b, true},
		{"past the last zone", 9, 2, action{}, false},
		{"other row", 0, 3, action{}, false},
		{"whole row zone", 79, 4, b, true},
		{"negative", -1, 2, action{}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := hit(zones, tt.x, tt.y)
			if got != tt.want || ok != tt.ok {
				t.Errorf("hit(%d,%d) = %+v %v, want %+v %v", tt.x, tt.y, got, ok, tt.want, tt.ok)
			}
		})
	}
	if _, ok := hit(nil, 0, 0); ok {
		t.Error("hit in no zones")
	}
}

func TestZonesCoverTheirLabels(t *testing.T) {
	m, _, _ := fleetPanel(t)
	m.st.snap = &route.Snapshot{Mode: route.ModeOn, Preset: config.PresetBalanced, FooterSynced: true}
	tests := []struct {
		tab  Tab
		want map[action]string
	}{
		{TabNow, map[action]string{
			{kind: actTab, tab: TabSessions}:       "Sessions 0",
			{kind: actTab, tab: TabUsage}:          "Usage 5",
			{kind: actPin, tier: config.TierHigh}:  "high",
			{kind: actPin, tier: config.TierMicro}: "micro",
		}},
		{TabPolicy, map[action]string{
			{kind: actPreset, preset: config.PresetThrifty}: "thrifty",
			{kind: actPreset, preset: config.PresetQuality}: "quality",
		}},
	}
	for _, tt := range tests {
		t.Run(string(tt.tab), func(t *testing.T) {
			m.tab = tt.tab
			v := m.view()
			v.styled = false
			out, zones := layout(v)
			lines := strings.Split(out, "\n")
			for a, text := range tt.want {
				i := slices.IndexFunc(zones, func(z zone) bool { return z.action == a })
				if i < 0 {
					t.Errorf("no zone for %+v", a)
					continue
				}
				z := zones[i]
				if got := strings.TrimSpace(string([]rune(lines[z.row])[z.x0:z.x1])); got != text {
					t.Errorf("zone %+v covers %q, want %q", a, got, text)
				}
			}
		})
	}
}

func clickAt(m *model, x, y int, b tea.MouseButton) tea.Cmd {
	_, cmd := m.Update(tea.MouseClickMsg{X: x, Y: y, Button: b})
	return cmd
}

func clickOn(t *testing.T, m *model, a action) tea.Cmd {
	t.Helper()
	_, zones := layout(m.view())
	i := slices.IndexFunc(zones, func(z zone) bool { return z.action == a })
	if i < 0 {
		t.Fatalf("no zone for %+v", a)
	}
	return clickAt(m, zones[i].x0, zones[i].row, tea.MouseLeft)
}

func TestClicksSendCommands(t *testing.T) {
	low := config.TierLow
	tests := []struct {
		name  string
		setup func(*route.Snapshot)
		tab   Tab
		on    action
		want  route.Command
	}{
		{"pin a tier", func(*route.Snapshot) {}, TabNow, action{kind: actPin, tier: config.TierMicro}, route.Command{Op: route.OpPin, Session: testSession, Tier: config.TierMicro}},
		{"pin jumps over the cycle", func(s *route.Snapshot) { s.Pin = &low }, TabNow, action{kind: actPin, tier: config.TierHigh}, route.Command{Op: route.OpPin, Session: testSession, Tier: config.TierHigh}},
		{"the pinned tier unpins", func(s *route.Snapshot) { s.Pin = &low }, TabNow, action{kind: actPin, tier: config.TierLow}, route.Command{Op: route.OpUnpin, Session: testSession}},
		{"preset", func(*route.Snapshot) {}, TabPolicy, action{kind: actPreset, preset: config.PresetThrifty}, route.Command{Op: route.OpPreset, Scope: route.ScopeHome, Preset: config.PresetThrifty}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := fixture()
			tt.setup(&s)
			m, f := connected(t, s)
			m.tab = tt.tab
			run(m, clickOn(t, m, tt.on))
			if got := f.sentCmds(); !slices.Equal(got, []route.Command{tt.want}) {
				t.Errorf("sent %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestClickOnATabShowsIt(t *testing.T) {
	m, _, ss := fleetPanel(t)
	for _, tab := range []Tab{TabUsage, TabSessions, TabNow} {
		clickOn(t, m, action{kind: actTab, tab: tab})
		if m.tab != tab {
			t.Errorf("tab = %s, want %s", m.tab, tab)
		}
	}
	m.cursor = ss[2].ID
	clickOn(t, m, action{kind: actTab, tab: TabSessions})
	if m.cursor != ss[0].ID {
		t.Errorf("cursor = %s, want the focus %s", m.cursor, ss[0].ID)
	}
}

func TestClickOnTheTopBarFocusesThatSession(t *testing.T) {
	m, e, ss := fleetPanel(t)
	run(m, clickOn(t, m, action{kind: actFocus, session: ss[1].ID}))
	refresh(t, m, func(st fleetState) bool { return st.focus == ss[1].ID })
	if subs := e.subs(); subs[len(subs)-1] != ss[1].ID {
		t.Errorf("subscribed %v, want last %s", subs, ss[1].ID)
	}
}

func TestSessionsRowClicksSelectThenOpen(t *testing.T) {
	m, _, ss := fleetPanel(t)
	clickOn(t, m, action{kind: actTab, tab: TabSessions})
	row := action{kind: actSelect, session: ss[2].ID}

	run(m, clickOn(t, m, row))
	if m.cursor != ss[2].ID || m.tab != TabSessions || m.st.focus != ss[0].ID {
		t.Fatalf("first click: cursor %s, tab %s, focus %s", m.cursor, m.tab, m.st.focus)
	}
	run(m, clickOn(t, m, row))
	if m.tab != TabNow {
		t.Errorf("second click: tab = %s", m.tab)
	}
	refresh(t, m, func(st fleetState) bool { return st.focus == ss[2].ID })
}

func TestWheelMovesTheSessionsCursor(t *testing.T) {
	tests := []struct {
		name   string
		tab    Tab
		button tea.MouseButton
		want   int
	}{
		{"down", TabSessions, tea.MouseWheelDown, 1},
		{"up stops at the first", TabSessions, tea.MouseWheelUp, 0},
		{"other tabs ignore it", TabNow, tea.MouseWheelDown, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, _, ss := fleetPanel(t)
			m.tab = tt.tab
			m.Update(tea.MouseWheelMsg{Button: tt.button})
			if m.selected() != ss[tt.want].ID {
				t.Errorf("selected %s, want %s", m.selected(), ss[tt.want].ID)
			}
		})
	}
}

func TestClicksTheMouseShouldNotAct(t *testing.T) {
	now := at(18, 0)
	tests := []struct {
		name   string
		focus  time.Duration
		button tea.MouseButton
		acts   bool
	}{
		{"left click", time.Hour, tea.MouseLeft, true},
		{"right click", time.Hour, tea.MouseRight, false},
		{"middle click", time.Hour, tea.MouseMiddle, false},
		{"click that focused the terminal", 100 * time.Millisecond, tea.MouseLeft, false},
		{"click after the focus window", 200 * time.Millisecond, tea.MouseLeft, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, f := connected(t, fixture())
			m.now = func() time.Time { return now }
			m.Update(tea.FocusMsg{})
			now = now.Add(tt.focus)
			_, zones := layout(m.view())
			i := slices.IndexFunc(zones, func(z zone) bool { return z.action.kind == actPin })
			run(m, clickAt(m, zones[i].x0, zones[i].row, tt.button))
			if got := len(f.sentCmds()) == 1; got != tt.acts {
				t.Errorf("acted = %v, want %v", got, tt.acts)
			}
		})
	}
}

func TestClickOutsideEveryZoneDoesNothing(t *testing.T) {
	m, f := connected(t, fixture())
	if cmd := clickAt(m, 79, 0, tea.MouseLeft); cmd != nil {
		t.Error("click on empty space made a command")
	}
	if len(f.sentCmds()) != 0 {
		t.Errorf("sent %+v", f.sentCmds())
	}
}

func TestViewAsksForTheMouseUnlessDisabled(t *testing.T) {
	m, _ := connected(t, fixture())
	if v := m.View(); v.MouseMode != tea.MouseModeNone || v.ReportFocus {
		t.Errorf("mouse %v focus %v by default for a bare model", v.MouseMode, v.ReportFocus)
	}
	m.mouse = true
	if v := m.View(); v.MouseMode != tea.MouseModeCellMotion || !v.ReportFocus || !v.AltScreen {
		t.Errorf("view = %+v", v)
	}
}
