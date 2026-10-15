package tui

import (
	"errors"
	"image/color"
	"slices"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/alexei-led/codex-router/internal/config"
	"github.com/alexei-led/codex-router/internal/route"
)

func TestKeysSendCommands(t *testing.T) {
	low := config.TierLow
	micro := config.TierMicro
	tests := []struct {
		name  string
		setup func(*route.Snapshot)
		tab   Tab
		keys  []string
		want  route.Command
	}{
		{"p then a number pins that tier", func(*route.Snapshot) {}, TabSessions, []string{"p", "3"}, route.Command{Op: route.OpPin, Session: testSession, Tier: config.TierMedium}},
		{"1 is micro", func(*route.Snapshot) {}, TabSessions, []string{"p", "1"}, route.Command{Op: route.OpPin, Session: testSession, Tier: config.TierMicro}},
		{"4 is high", func(*route.Snapshot) {}, TabSessions, []string{"p", "4"}, route.Command{Op: route.OpPin, Session: testSession, Tier: config.TierHigh}},
		{"0 clears the pin", func(s *route.Snapshot) { s.Pin = &low }, TabSessions, []string{"p", "0"}, route.Command{Op: route.OpUnpin, Session: testSession}},
		{"picking the pinned tier again keeps it", func(s *route.Snapshot) { s.Pin = &micro }, TabSessions, []string{"p", "1"}, route.Command{Op: route.OpPin, Session: testSession, Tier: config.TierMicro}},
		{"p alone sends nothing", func(*route.Snapshot) {}, TabSessions, []string{"p"}, route.Command{}},
		{"esc cancels the popover", func(*route.Snapshot) {}, TabSessions, []string{"p", "esc", "3"}, route.Command{}},
		{"a number without a popover sends nothing", func(*route.Snapshot) {}, TabSessions, []string{"3"}, route.Command{}},
		{"5 is no choice", func(*route.Snapshot) {}, TabSessions, []string{"p", "5"}, route.Command{}},
		{"p on Policy has no pin row", func(*route.Snapshot) {}, TabPolicy, []string{"p", "3"}, route.Command{}},
		{"space pauses the session", func(*route.Snapshot) {}, TabSessions, []string{" "}, route.Command{Op: route.OpPause, Session: testSession}},
		{"space resumes paused", func(s *route.Snapshot) { s.Mode = route.ModePaused }, TabSessions, []string{" "}, route.Command{Op: route.OpResume, Session: testSession}},
		{"space resumes manual", func(s *route.Snapshot) { s.Mode = route.ModeManual }, TabUsage, []string{" "}, route.Command{Op: route.OpResume, Session: testSession}},
		{"P pauses the home", func(*route.Snapshot) {}, TabSessions, []string{"P"}, route.Command{Op: route.OpPause, Scope: route.ScopeHome}},
		{"P resumes a paused home", func(s *route.Snapshot) { s.Mode = route.ModePaused }, TabSessions, []string{"P"}, route.Command{Op: route.OpResume, Scope: route.ScopeHome}},
		{"s then a number sets the preset", func(*route.Snapshot) {}, TabPolicy, []string{"s", "3"}, route.Command{Op: route.OpPreset, Scope: route.ScopeHome, Preset: config.PresetQuality}},
		{"1 is thrifty", func(*route.Snapshot) {}, TabPolicy, []string{"s", "1"}, route.Command{Op: route.OpPreset, Scope: route.ScopeHome, Preset: config.PresetThrifty}},
		{"4 is no preset", func(*route.Snapshot) {}, TabPolicy, []string{"s", "4"}, route.Command{}},
		{"s outside Policy sends nothing", func(*route.Snapshot) {}, TabSessions, []string{"s", "3"}, route.Command{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := fixture()
			tt.setup(&s)
			m, f := connected(t, s)
			m.tab = tt.tab
			pressAll(m, tt.keys...)
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

func TestViewRing(t *testing.T) {
	type stop struct {
		tab  Tab
		pane pane
	}
	tests := []struct {
		name   string
		width  int
		locked bool
		keys   []string
		want   stop
	}{
		{"tab goes on", 97, false, []string{"tab"}, stop{TabPolicy, paneList}},
		{"tab twice", 97, false, []string{"tab", "tab"}, stop{TabUsage, paneList}},
		{"tab wraps to Sessions", 97, false, []string{"tab", "tab", "tab"}, stop{TabSessions, paneList}},
		{"shift+tab wraps to Usage", 97, false, []string{"shift+tab"}, stop{TabUsage, paneList}},
		{"locked has the same ring", 97, true, []string{"tab", "tab", "tab"}, stop{TabSessions, paneList}},
		{"side by side: tab moves to the detail first", 130, false, []string{"tab"}, stop{TabSessions, paneDetail}},
		{"then on to Policy", 130, false, []string{"tab", "tab"}, stop{TabPolicy, paneList}},
		{"shift+tab back from Policy lands on the detail", 130, false, []string{"tab", "tab", "shift+tab"}, stop{TabSessions, paneDetail}},
		{"shift+tab from the detail lands on the list", 130, false, []string{"tab", "shift+tab"}, stop{TabSessions, paneList}},
		{"shift+tab from the list goes to Usage", 130, false, []string{"shift+tab"}, stop{TabUsage, paneList}},
		{"locked is never side by side", 130, true, []string{"tab"}, stop{TabPolicy, paneList}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, _ := connected(t, fixture())
			m.Update(tea.WindowSizeMsg{Width: tt.width, Height: 50})
			if tt.locked {
				m.f.locked = testSession
			}
			pressAll(m, tt.keys...)
			if got := (stop{m.tab, m.pane}); got != tt.want {
				t.Errorf("at %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestQuitKeyWorksEverywhere(t *testing.T) {
	for _, setup := range []string{"", "?", "p"} {
		t.Run("after "+setup, func(t *testing.T) {
			m, _ := connected(t, fixture())
			if setup != "" {
				press(m, setup)
			}
			cmd := press(m, "q")
			if cmd == nil {
				t.Fatal("no quit command")
			}
			if _, ok := cmd().(tea.QuitMsg); !ok {
				t.Error("q does not quit")
			}
		})
	}
}

func footerText(v view) string {
	var parts []string
	for _, b := range footerBindings(v) {
		h := b.Help()
		parts = append(parts, h.Key+" "+h.Desc)
	}
	return strings.Join(parts, " · ")
}

func TestFooterFollowsTheState(t *testing.T) {
	one := lockedFixture()
	paused := lockedFixture()
	for i := range paused.homes[0].sessions {
		paused.homes[0].sessions[i].Mode = route.ModePaused
	}
	many := fleetFixture()
	manual := fixture()
	manual.Mode = route.ModeManual
	off := fixture()
	off.ClassifierOff = true
	tests := []struct {
		name string
		v    view
		want string
	}{
		{"several sessions", view{snap: fixture(), tab: TabSessions, fleet: &many}, "↑↓ select · p pin · space pause · P pause home · tab views · q quit · ? help"},
		{"one session hides select", view{snap: fixture(), tab: TabSessions, fleet: &one}, "p pin · space pause · P pause home · tab views · q quit · ? help"},
		{"manual resumes", view{snap: manual, tab: TabSessions, fleet: &many}, "↑↓ select · p pin · space resume · P pause home · tab views · q quit · ? help"},
		{"a paused home resumes", view{snap: fixture(), tab: TabSessions, fleet: &paused}, "p pin · space pause · P resume home · tab views · q quit · ? help"},
		{"policy offers the preset", view{snap: fixture(), tab: TabPolicy, fleet: &many}, "s preset · space pause · P pause home · tab views · q quit · ? help"},
		{"policy without a classifier", view{snap: off, tab: TabPolicy, fleet: &many}, "space pause · P pause home · tab views · q quit · ? help"},
		{"usage", view{snap: fixture(), tab: TabUsage, fleet: &many}, "space pause · P pause home · tab views · q quit · ? help"},
		{"locked panel", view{snap: fixture(), tab: TabSessions, fleet: &many, locked: true}, "p pin · space pause · P pause home · tab views · q quit · ? help"},
		{"zoomed detail has no list keys", view{snap: fixture(), tab: TabSessions, fleet: &many, zoom: true}, "p pin · space pause · P pause home · tab views · q quit · ? help"},
		{"disconnected keeps navigation only", view{snap: fixture(), tab: TabSessions, fleet: &many, disconnected: true}, "↑↓ select · tab views · q quit · ? help"},
		{"no sessions", view{tab: TabSessions, fleet: &fleetState{}}, "q quit · ? help"},
		{"ended", view{snap: fixture(), tab: TabSessions, fleet: &fleetState{ended: true}, locked: true}, "q quit · ? help"},
		{"bare snapshot", view{snap: manual, tab: TabSessions}, "p pin · space resume · P pause home · tab views · q quit · ? help"},
		{"help overlay", view{snap: fixture(), tab: TabSessions, fleet: &many, help: true}, "esc close"},
		{"pin popover", view{snap: fixture(), tab: TabSessions, fleet: &many, popover: popPin}, "1-4 pick · esc close"},
		{"preset popover", view{snap: fixture(), tab: TabPolicy, fleet: &many, popover: popPreset}, "1-3 pick · esc close"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := footerText(tt.v); got != tt.want {
				t.Errorf("footer\n got %s\nwant %s", got, tt.want)
			}
		})
	}
}

func TestFooterWrapsAtTheWidth(t *testing.T) {
	st := fleetFixture()
	got := render(view{snap: fixture(), tab: TabSessions, width: 50, fleet: &st})
	lines := strings.Split(strings.TrimSuffix(got, "\n"), "\n")
	foot := lines[len(lines)-2:]
	if foot[0] != "↑↓ select · p pin · space pause · P pause home" || foot[1] != "tab views · q quit · ? help" {
		t.Errorf("footer lines %q", foot)
	}
}

func TestHelpOverlay(t *testing.T) {
	m, f := connected(t, fixture())
	press(m, "?")
	content := screen(m)
	for _, w := range []string{"Navigate", "Session", "Home", "Views", "pause home", "previous view", "esc close"} {
		if !strings.Contains(content, w) {
			t.Errorf("help overlay missing %q:\n%s", w, content)
		}
	}
	pressAll(m, "p", " ", "P", "tab", "3")
	if m.popover != popNone || m.tab != TabSessions || len(f.sentCmds()) != 0 {
		t.Errorf("keys acted under the overlay: popover %v, tab %s, sent %v", m.popover, m.tab, f.sentCmds())
	}
	press(m, "esc")
	if m.help || strings.Contains(screen(m), "Navigate") {
		t.Error("esc did not close the overlay")
	}
	pressAll(m, "?", "?")
	if m.help {
		t.Error("? did not close the overlay")
	}
}

func TestHelpListsEveryBindingOnce(t *testing.T) {
	canvas := &canvas{width: 97}
	text := strings.Join(canvas.helpLines(), "\n")
	for _, b := range keys.bindings() {
		if h := b.Help(); !strings.Contains(text, h.Key) || !strings.Contains(text, h.Desc) {
			t.Errorf("help overlay lacks %q %q", h.Key, h.Desc)
		}
	}
	seen := map[string]bool{}
	for _, b := range keys.bindings() {
		if seen[handle(b)] {
			t.Errorf("binding %q listed twice", handle(b))
		}
		seen[handle(b)] = true
	}
}

func TestPopoverShowsAndEscCloses(t *testing.T) {
	m, _ := connected(t, fixture())
	press(m, "p")
	if got := screen(m); !strings.Contains(got, "1 micro  2 low  3 medium  4 high  0 none") {
		t.Errorf("no pin popover:\n%s", got)
	}
	press(m, "esc")
	if m.popover != popNone || strings.Contains(screen(m), "0 none") {
		t.Error("esc did not close the popover")
	}
	m.tab = TabPolicy
	press(m, "s")
	if got := screen(m); !strings.Contains(got, "1 thrifty  2 balanced  3 quality") {
		t.Errorf("no preset popover:\n%s", got)
	}
}

func TestSelectionMovesTheFocus(t *testing.T) {
	tests := []struct {
		keys []string
		want int
	}{
		{[]string{"down"}, 1},
		{[]string{"j", "j"}, 2},
		{[]string{"down", "down", "down", "down"}, 2},
		{[]string{"up"}, 0},
		{[]string{"down", "down", "up"}, 1},
		{[]string{"down", "k"}, 0},
	}
	for _, tt := range tests {
		t.Run(strings.Join(tt.keys, ""), func(t *testing.T) {
			m, e, ss := fleetPanel(t)
			pressAll(m, tt.keys...)
			if m.st.focus != ss[tt.want].ID {
				t.Fatalf("focus %s, want %s", m.st.focus, ss[tt.want].ID)
			}
			refresh(t, m, func(st fleetState) bool { return st.focus == ss[tt.want].ID })
			if subs := e.subs(); subs[len(subs)-1] != ss[tt.want].ID {
				t.Errorf("subscribed %v, want last %s", subs, ss[tt.want].ID)
			}
		})
	}
}

func TestSelectionIgnoresTheListWhereThereIsNone(t *testing.T) {
	m, _, ss := fleetPanel(t)
	m.tab = TabPolicy
	pressAll(m, "down", "j")
	m.tab, m.zoom = TabSessions, true
	pressAll(m, "down")
	if m.st.focus != ss[0].ID {
		t.Errorf("focus moved to %s", m.st.focus)
	}
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

func TestEnterOpensTheDetailAndEscGoesBack(t *testing.T) {
	m, _, _ := fleetPanel(t)
	m.Update(tea.WindowSizeMsg{Width: 97, Height: 50})
	press(m, "enter")
	if !m.zoom || strings.Contains(screen(m), "SESSION") {
		t.Errorf("enter did not zoom into the detail:\n%s", screen(m))
	}
	press(m, "esc")
	if m.zoom || !strings.Contains(screen(m), "SESSION") {
		t.Error("esc did not bring the list back")
	}

	m.Update(tea.WindowSizeMsg{Width: 130, Height: 50})
	press(m, "enter")
	if m.zoom || m.pane != paneDetail {
		t.Errorf("side by side: zoom %v, pane %v", m.zoom, m.pane)
	}
	press(m, "esc")
	if m.pane != paneList {
		t.Errorf("pane after esc = %v", m.pane)
	}
}

func TestEscClosesOverlaysBeforeGoingBack(t *testing.T) {
	m, _, _ := fleetPanel(t)
	pressAll(m, "enter", "p", "esc")
	if m.popover != popNone || !m.zoom {
		t.Errorf("first esc: popover %v, zoom %v", m.popover, m.zoom)
	}
	press(m, "esc")
	if m.zoom {
		t.Error("second esc did not leave the detail")
	}
}

func TestLockedPanelIgnoresSessionKeys(t *testing.T) {
	m, e, ss := fleetPanel(t)
	m.f.locked = ss[0].ID
	for _, k := range []string{"]", "[", "down", "j", "enter"} {
		run(m, press(m, k))
	}
	if m.tab != TabSessions || m.zoom || m.st.focus != ss[0].ID || len(e.subs()) != 1 {
		t.Errorf("tab %s, zoom %v, focus %s, subscribed %v", m.tab, m.zoom, m.st.focus, e.subs())
	}
	if v := screen(m); strings.Contains(v, "SESSION") || strings.Contains(v, "3 sessions") {
		t.Errorf("locked view shows the list:\n%s", v)
	}
}

func TestToastConfirmsWhatTheAckAccepted(t *testing.T) {
	tests := []struct {
		name string
		keys []string
		want string
	}{
		{"pin", []string{"p", "4"}, "pinned high for the next turn"},
		{"unpin", []string{"p", "0"}, "pin cleared"},
		{"pause", []string{" "}, "session paused"},
		{"home pause", []string{"P"}, "home paused"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, _ := connected(t, fixture())
			pressAll(m, tt.keys...)
			if m.toast != tt.want || !strings.Contains(screen(m), "✓ "+tt.want) {
				t.Errorf("toast %q, view:\n%s", m.toast, screen(m))
			}
		})
	}
}

func TestToastResumeAndPreset(t *testing.T) {
	s := fixture()
	s.Mode = route.ModePaused
	m, _ := connected(t, s)
	pressAll(m, " ")
	if m.toast != "session resumed" {
		t.Errorf("toast %q", m.toast)
	}
	pressAll(m, "P")
	if m.toast != "home resumed" {
		t.Errorf("toast %q", m.toast)
	}
	m.tab = TabPolicy
	pressAll(m, "s", "2")
	if m.toast != "preset balanced sent to the home" {
		t.Errorf("toast %q", m.toast)
	}
}

func TestToastGoesAfterItsTime(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m := newModel(newTestFleet(t).fleet)
		_, tick := m.Update(sentMsg{done: "first"})
		if m.toast != "first" || tick == nil {
			t.Fatalf("toast %q, tick %v", m.toast, tick)
		}
		start := time.Now()
		first := tick()
		if got := time.Since(start); got != toastFor {
			t.Errorf("toast lasts %v, want %v", got, toastFor)
		}
		m.Update(sentMsg{done: "second"})
		m.Update(first)
		if m.toast != "second" {
			t.Errorf("an old tick cleared the newer toast: %q", m.toast)
		}
		m.Update(toastMsg{seq: m.toastSeq})
		if m.toast != "" {
			t.Errorf("toast %q after its time", m.toast)
		}
	})
}

func TestFailedCommandsShowNoToast(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{"rejected", errors.New("unknown tier"), "command failed: unknown tier"},
		{"not connected", errNotConnected, "disconnected: command not sent"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, _ := connected(t, fixture())
			_, cmd := m.Update(sentMsg{err: tt.err, done: "pinned"})
			if cmd != nil || m.toast != "" || m.notice != tt.want {
				t.Errorf("cmd %v, toast %q, notice %q", cmd, m.toast, m.notice)
			}
		})
	}
}

func TestBackgroundColorPicksTheTheme(t *testing.T) {
	m, _ := connected(t, fixture())
	if m.view().light {
		t.Fatal("light before the terminal answered")
	}
	m.Update(tea.BackgroundColorMsg{Color: color.White})
	if !m.view().light {
		t.Error("a light background did not select the light theme")
	}
	m.Update(tea.BackgroundColorMsg{Color: color.Black})
	if m.view().light {
		t.Error("a dark background did not select the dark theme")
	}
}

func TestInitAsksForTheBackgroundColor(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newTestFleet(t).fleet
		f.notify()
		batch, ok := newModel(f).Init()().(tea.BatchMsg)
		if !ok {
			t.Fatal("Init is not a batch")
		}
		asked := false
		for _, cmd := range batch {
			asked = asked || cmd() == tea.RequestBackgroundColor()
		}
		if !asked {
			t.Error("Init does not request the background color")
		}
	})
}
