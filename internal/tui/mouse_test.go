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
	a, b := action{kind: actTab, tab: TabSessions}, action{kind: actTab, tab: TabPolicy}
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

func plainView(m *model) view {
	v := m.view()
	v.styled = false
	return v
}

func TestZonesCoverTheirLabels(t *testing.T) {
	m, _, _ := fleetPanel(t)
	m.st.snap = &route.Snapshot{Mode: route.ModeOn, Preset: config.PresetBalanced, FooterSynced: true}
	tests := []struct {
		name    string
		tab     Tab
		popover popover
		want    map[action]string
	}{
		{"sessions", TabSessions, popNone, map[action]string{
			{kind: actTab, tab: TabSessions}:         "Sessions",
			{kind: actTab, tab: TabPolicy}:           "Policy",
			{kind: actTab, tab: TabUsage}:            "Usage",
			{kind: actKey, key: handle(keys.Help)}:   "? help",
			{kind: actPin, tier: config.TierHigh}:    "high",
			{kind: actPin, tier: config.TierMicro}:   "micro",
			{kind: actKey, key: handle(keys.Pin)}:    "p pin",
			{kind: actKey, key: handle(keys.Toggle)}: "space pause",
			{kind: actKey, key: handle(keys.Home)}:   "P pause home",
			{kind: actKey, key: handle(keys.Quit)}:   "q quit",
			{kind: actKey, key: handle(keys.Next)}:   "tab views",
		}},
		{"policy", TabPolicy, popNone, map[action]string{
			{kind: actPreset, preset: config.PresetThrifty}: "thrifty",
			{kind: actPreset, preset: config.PresetQuality}: "quality",
			{kind: actKey, key: handle(keys.Preset)}:        "s preset",
		}},
		{"pin popover", TabSessions, popPin, map[action]string{
			{kind: actPin, tier: config.TierMicro}:  "1 micro",
			{kind: actPin, tier: config.TierMedium}: "3 medium",
			{kind: actUnpin}:                        "0 none",
		}},
		{"preset popover", TabPolicy, popPreset, map[action]string{
			{kind: actPreset, preset: config.PresetThrifty}:  "1 thrifty",
			{kind: actPreset, preset: config.PresetBalanced}: "2 balanced",
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m.tab, m.popover = tt.tab, tt.popover
			out, zones := layout(plainView(m))
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

func TestZonesStayOnTheirRowsWhenSideBySide(t *testing.T) {
	m, _, ss := fleetPanel(t)
	m.Update(tea.WindowSizeMsg{Width: 130, Height: 50})
	m.st.snap = &route.Snapshot{Mode: route.ModeOn, FooterSynced: true}
	out, zones := layout(plainView(m))
	lines := strings.Split(out, "\n")
	for a, text := range map[action]string{
		{kind: actPin, tier: config.TierLow}:   "low",
		{kind: actPin, tier: config.TierMicro}: "micro",
	} {
		i := slices.IndexFunc(zones, func(z zone) bool { return z.action == a })
		if i < 0 {
			t.Fatalf("no zone for %+v", a)
		}
		z := zones[i]
		if got := strings.TrimSpace(string([]rune(lines[z.row])[z.x0:z.x1])); got != text {
			t.Errorf("zone %+v covers %q, want %q", a, got, text)
		}
	}
	i := slices.IndexFunc(zones, func(z zone) bool { return z.action == action{kind: actSelect, session: ss[1].ID} })
	if i < 0 || !strings.Contains(lines[zones[i].row], "  b  ") {
		t.Errorf("row zone of %s: %v", ss[1].ID, i)
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
		name    string
		setup   func(*route.Snapshot)
		tab     Tab
		popover popover
		on      action
		want    route.Command
	}{
		{"pin a tier", func(*route.Snapshot) {}, TabSessions, popNone, action{kind: actPin, tier: config.TierMicro}, route.Command{Op: route.OpPin, Session: testSession, Tier: config.TierMicro}},
		{"pin jumps straight to the tier", func(s *route.Snapshot) { s.Pin = &low }, TabSessions, popNone, action{kind: actPin, tier: config.TierHigh}, route.Command{Op: route.OpPin, Session: testSession, Tier: config.TierHigh}},
		{"the pinned tier unpins", func(s *route.Snapshot) { s.Pin = &low }, TabSessions, popNone, action{kind: actPin, tier: config.TierLow}, route.Command{Op: route.OpUnpin, Session: testSession}},
		{"a popover choice pins", func(*route.Snapshot) {}, TabSessions, popPin, action{kind: actPin, tier: config.TierMedium}, route.Command{Op: route.OpPin, Session: testSession, Tier: config.TierMedium}},
		{"the popover's none unpins", func(s *route.Snapshot) { s.Pin = &low }, TabSessions, popPin, action{kind: actUnpin}, route.Command{Op: route.OpUnpin, Session: testSession}},
		{"preset", func(*route.Snapshot) {}, TabPolicy, popNone, action{kind: actPreset, preset: config.PresetThrifty}, route.Command{Op: route.OpPreset, Scope: route.ScopeHome, Preset: config.PresetThrifty}},
		{"a preset popover choice", func(*route.Snapshot) {}, TabPolicy, popPreset, action{kind: actPreset, preset: config.PresetQuality}, route.Command{Op: route.OpPreset, Scope: route.ScopeHome, Preset: config.PresetQuality}},
		{"the footer's pause", func(*route.Snapshot) {}, TabSessions, popNone, action{kind: actKey, key: handle(keys.Toggle)}, route.Command{Op: route.OpPause, Session: testSession}},
		{"the footer's home pause", func(*route.Snapshot) {}, TabUsage, popNone, action{kind: actKey, key: handle(keys.Home)}, route.Command{Op: route.OpPause, Scope: route.ScopeHome}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := fixture()
			tt.setup(&s)
			m, f := connected(t, s)
			m.tab, m.popover = tt.tab, tt.popover
			run(m, clickOn(t, m, tt.on))
			if got := f.sentCmds(); !slices.Equal(got, []route.Command{tt.want}) {
				t.Errorf("sent %+v, want %+v", got, tt.want)
			}
			if m.popover != popNone {
				t.Error("popover still open")
			}
			if m.toast == "" {
				t.Error("no toast for the ack")
			}
		})
	}
}

func TestClickOnAViewNameShowsIt(t *testing.T) {
	m, _, _ := fleetPanel(t)
	for _, tab := range []Tab{TabUsage, TabPolicy, TabSessions} {
		clickOn(t, m, action{kind: actTab, tab: tab})
		if m.tab != tab {
			t.Errorf("tab = %s, want %s", m.tab, tab)
		}
	}
}

func TestClickOnTheFooterRunsItsBinding(t *testing.T) {
	m, _, _ := fleetPanel(t)
	clickOn(t, m, action{kind: actKey, key: handle(keys.Next)})
	if m.tab != TabPolicy {
		t.Errorf("tab = %s", m.tab)
	}
	clickOn(t, m, action{kind: actKey, key: handle(keys.Help)})
	if !m.help {
		t.Error("help did not open")
	}
	clickAt(m, 3, 3, tea.MouseLeft)
	if m.help {
		t.Error("a click outside did not close the help overlay")
	}
	clickOn(t, m, action{kind: actKey, key: handle(keys.Quit)})
}

func TestClickOutsideAPopoverClosesItWithoutActing(t *testing.T) {
	m, f := connected(t, fixture())
	press(m, "p")
	if cmd := clickAt(m, 2, 12, tea.MouseLeft); cmd != nil {
		t.Error("click outside made a command")
	}
	if m.popover != popNone || len(f.sentCmds()) != 0 {
		t.Errorf("popover %v, sent %v", m.popover, f.sentCmds())
	}
}

func TestListRowClicksSelectThenOpen(t *testing.T) {
	m, _, ss := fleetPanel(t)
	m.Update(tea.WindowSizeMsg{Width: 97, Height: 50})
	row := action{kind: actSelect, session: ss[2].ID}

	run(m, clickOn(t, m, row))
	if m.st.focus != ss[2].ID || m.zoom {
		t.Fatalf("first click: focus %s, zoom %v", m.st.focus, m.zoom)
	}
	refresh(t, m, func(st fleetState) bool { return st.focus == ss[2].ID })
	run(m, clickOn(t, m, row))
	if !m.zoom {
		t.Error("second click did not open the detail")
	}

	m.zoom = false
	m.Update(tea.WindowSizeMsg{Width: 130, Height: 50})
	run(m, clickOn(t, m, row))
	if m.pane != paneDetail {
		t.Errorf("side by side, second click: pane = %v", m.pane)
	}
}

func TestWheelMovesTheSelection(t *testing.T) {
	tests := []struct {
		name   string
		tab    Tab
		zoom   bool
		button tea.MouseButton
		want   int
	}{
		{"down", TabSessions, false, tea.MouseWheelDown, 1},
		{"up stops at the first", TabSessions, false, tea.MouseWheelUp, 0},
		{"other views ignore it", TabPolicy, false, tea.MouseWheelDown, 0},
		{"the zoomed detail has no list", TabSessions, true, tea.MouseWheelDown, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, _, ss := fleetPanel(t)
			m.tab, m.zoom = tt.tab, tt.zoom
			m.Update(tea.MouseWheelMsg{Button: tt.button})
			if m.st.focus != ss[tt.want].ID {
				t.Errorf("focus %s, want %s", m.st.focus, ss[tt.want].ID)
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
	if cmd := clickAt(m, 79, 1, tea.MouseLeft); cmd != nil {
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
