package tui

import (
	"errors"
	"slices"
	"time"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"github.com/alexei-led/codex-router/internal/config"
	"github.com/alexei-led/codex-router/internal/control"
	"github.com/alexei-led/codex-router/internal/route"
)

const (
	heartbeat    = 2 * time.Second
	defaultWidth = 80
	// endedExit is how long a locked panel shows `ended` before it quits.
	endedExit = 3 * time.Second
	// focusClick is how long after the terminal gains focus a click is the
	// click that focused it, not a command.
	focusClick = 150 * time.Millisecond
)

// conn is the part of control.Client the panel uses.
type conn interface {
	Hello() control.Hello
	Snapshots() <-chan route.Snapshot
	Sessions() <-chan []route.SessionInfo
	Subscribe(session string) error
	Send(route.Command) error
	Close() error
}

type (
	fleetMsg struct{}
	beatMsg  struct{}
	endedMsg struct{}
	sentMsg  struct{ err error }
)

type model struct {
	f      *fleet
	st     fleetState
	tab    Tab
	width  int
	height int
	cursor string // Sessions tab selection
	ending bool   // the ended exit is scheduled
	notice string
	mouse  bool // the panel takes the mouse
	focus  time.Time
	now    func() time.Time
}

func newModel(f *fleet) *model {
	return &model{f: f, st: fleetState{disconnected: true}, tab: TabNow, width: defaultWidth, now: time.Now}
}

func (m *model) Init() tea.Cmd { return tea.Batch(waitFleetCmd(m.f), beatCmd()) }

func waitFleetCmd(f *fleet) tea.Cmd {
	return func() tea.Msg {
		<-f.changed
		return fleetMsg{}
	}
}

func beatCmd() tea.Cmd {
	return tea.Tick(heartbeat, func(time.Time) tea.Msg { return beatMsg{} })
}

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case fleetMsg:
		m.st = m.f.state()
		if _, ok := m.listed(m.cursor); !ok {
			m.cursor = m.st.focus
		}
		if m.st.ended && !m.ending {
			m.ending = true
			return m, tea.Batch(waitFleetCmd(m.f), tea.Tick(endedExit, func(time.Time) tea.Msg { return endedMsg{} }))
		}
		return m, waitFleetCmd(m.f)
	case endedMsg:
		m.ending = false
		if m.st.ended {
			return m, tea.Quit
		}
	case beatMsg:
		f := m.f
		return m, tea.Batch(func() tea.Msg { f.heartbeat(); return nil }, beatCmd())
	case sentMsg:
		m.notice = ""
		switch {
		case errors.Is(msg.err, errNotConnected):
			m.notice = msg.err.Error()
		case msg.err != nil:
			m.notice = "command failed: " + msg.err.Error()
		}
	case tea.KeyPressMsg:
		return m, m.key(msg)
	case tea.FocusMsg:
		m.focus = m.now()
	case tea.MouseClickMsg:
		return m, m.click(msg.Mouse())
	case tea.MouseWheelMsg:
		if m.tab == TabSessions {
			switch msg.Mouse().Button {
			case tea.MouseWheelUp:
				m.moveCursor(-1)
			case tea.MouseWheelDown:
				m.moveCursor(1)
			}
		}
	}
	return m, nil
}

// click runs the action under a left click. It lays the view out again
// rather than keeping the last drawn zones, so it cannot act on a stale one.
func (m *model) click(ms tea.Mouse) tea.Cmd {
	if ms.Button != tea.MouseLeft || m.now().Sub(m.focus) < focusClick {
		return nil
	}
	_, zones := layout(m.view())
	a, ok := hit(zones, ms.X, ms.Y)
	if !ok {
		return nil
	}
	switch a.kind {
	case actTab:
		m.tab = a.tab
		if a.tab == TabSessions {
			m.cursor = m.st.focus
		}
	case actFocus:
		return m.focusOn(a.session)
	case actSelect:
		if a.session != m.selected() {
			m.cursor = a.session
			return nil
		}
		m.tab = TabNow
		return m.focusOn(a.session)
	case actPin:
		if p := m.snap().Pin; p != nil && *p == a.tier {
			return m.command(route.Command{Op: route.OpUnpin, Session: m.st.focus})
		}
		return m.command(route.Command{Op: route.OpPin, Session: m.st.focus, Tier: a.tier})
	case actPreset:
		return m.homeCommand(route.Command{Op: route.OpPreset, Preset: a.preset})
	}
	return nil
}

// selected is the Sessions tab row the cursor is on; the focus until it moves.
func (m *model) selected() string {
	if m.cursor == "" {
		return m.st.focus
	}
	return m.cursor
}

func (m *model) key(k tea.KeyPressMsg) tea.Cmd {
	all := m.f.locked == ""
	tabs := tabsFor(all)
	switch {
	case key.Matches(k, keys.Quit):
		return tea.Quit
	case key.Matches(k, keys.TabNum):
		m.tab = Tabs[k.String()[0]-'1']
	case key.Matches(k, keys.List):
		if all {
			m.tab, m.cursor = TabSessions, m.st.focus
		}
	case key.Matches(k, keys.Next):
		m.tab = tabs[(m.tabIndex(tabs)+1)%len(tabs)]
	case key.Matches(k, keys.Prev):
		m.tab = tabs[(m.tabIndex(tabs)+len(tabs)-1)%len(tabs)]
	case key.Matches(k, keys.Session):
		if all {
			if k.String() == "[" {
				return m.step(-1)
			}
			return m.step(1)
		}
	case key.Matches(k, keys.Up):
		if m.tab == TabSessions {
			m.moveCursor(-1)
		}
	case key.Matches(k, keys.Down):
		if m.tab == TabSessions {
			m.moveCursor(1)
		}
	case key.Matches(k, keys.Enter):
		if m.tab == TabSessions && m.cursor != "" {
			m.tab = TabNow
			return m.focusOn(m.cursor)
		}
	case key.Matches(k, keys.Pin):
		return m.command(pinCycle(m.st.focus, m.snap().Pin))
	case key.Matches(k, keys.Toggle):
		return m.command(toggleCommand(m.st.focus, m.snap().Mode))
	case key.Matches(k, keys.Home):
		return m.homeCommand(m.homeToggle())
	case key.Matches(k, keys.Preset):
		if m.tab == TabPolicy {
			return m.homeCommand(route.Command{Op: route.OpPreset, Preset: nextPreset(m.snap().Preset)})
		}
	}
	return nil
}

func (m *model) tabIndex(tabs []Tab) int {
	for i, t := range tabs {
		if t == m.tab {
			return i
		}
	}
	return slices.Index(tabs, TabNow)
}

// listed returns the index of session id in the sessions list.
func (m *model) listed(id string) (int, bool) {
	i := slices.IndexFunc(m.st.all(), func(s fleetSession) bool { return s.ID == id })
	return i, i >= 0
}

// step moves the focus d sessions along the list, wrapping around.
func (m *model) step(d int) tea.Cmd {
	all := m.st.all()
	if len(all) == 0 {
		return nil
	}
	i, _ := m.listed(m.st.focus)
	return m.focusOn(all[((i+d)%len(all)+len(all))%len(all)].ID)
}

// moveCursor moves the Sessions tab selection d rows, stopping at the ends.
func (m *model) moveCursor(d int) {
	all := m.st.all()
	if len(all) == 0 {
		return
	}
	i, ok := m.listed(m.cursor)
	if !ok {
		i, _ = m.listed(m.st.focus)
	}
	m.cursor = all[min(max(i+d, 0), len(all)-1)].ID
}

// focusOn focuses session id. The panel shows it at once; the fleet
// resubscribes in the background and confirms with its next state.
func (m *model) focusOn(id string) tea.Cmd {
	if id == m.st.focus {
		return nil
	}
	m.st.focus, m.st.snap, m.cursor = id, nil, id
	f := m.f
	return func() tea.Msg { f.focusOn(id); return nil }
}

// snap is the focused session's snapshot, empty until one arrives.
func (m *model) snap() route.Snapshot {
	if m.st.snap == nil {
		return route.Snapshot{}
	}
	return *m.st.snap
}

// command sends a command for the focused session.
func (m *model) command(cmd route.Command) tea.Cmd {
	if m.st.focus == "" || m.st.disconnected {
		m.notice = errNotConnected.Error()
		return nil
	}
	f := m.f
	return func() tea.Msg { return sentMsg{f.send(cmd)} }
}

// homeCommand sends cmd to every endpoint of the focused session's home.
func (m *model) homeCommand(cmd route.Command) tea.Cmd {
	s, ok := m.st.focused()
	if !ok {
		m.notice = errNotConnected.Error()
		return nil
	}
	f, h := m.f, s.Home
	return func() tea.Msg { return sentMsg{f.sendHome(h, cmd)} }
}

// homeToggle pauses the focused home unless every session of it is already
// paused, then it resumes. The sessions list carries no home pause flag, so
// a home whose sessions are all paused one by one gets a resume that keeps
// those session pauses; space resumes each.
func (m *model) homeToggle() route.Command {
	s, _ := m.st.focused()
	h, _ := m.st.homeOf(s.Home)
	for _, x := range h.sessions {
		if x.Mode != route.ModePaused {
			return route.Command{Op: route.OpPause}
		}
	}
	return route.Command{Op: route.OpResume}
}

// pinCycle walks session's pin through none, high, medium, low, micro, none.
func pinCycle(session string, cur *config.Tier) route.Command {
	if cur == nil {
		return route.Command{Op: route.OpPin, Session: session, Tier: tierOrder[0]}
	}
	for i, t := range tierOrder {
		if t == *cur && i+1 < len(tierOrder) {
			return route.Command{Op: route.OpPin, Session: session, Tier: tierOrder[i+1]}
		}
	}
	return route.Command{Op: route.OpUnpin, Session: session}
}

// toggleCommand pauses session, or resumes it when paused or manual.
func toggleCommand(session string, mode route.Mode) route.Command {
	if mode == route.ModeOn || mode == "" {
		return route.Command{Op: route.OpPause, Session: session}
	}
	return route.Command{Op: route.OpResume, Session: session}
}

func nextPreset(cur config.Preset) config.Preset {
	for i, p := range Presets {
		if p == cur {
			return Presets[(i+1)%len(Presets)]
		}
	}
	return Presets[0]
}

func (m *model) View() tea.View {
	v := tea.NewView(render(m.view()))
	v.AltScreen = true
	if m.mouse {
		v.MouseMode = tea.MouseModeCellMotion
		v.ReportFocus = true
	}
	return v
}

func (m *model) view() view {
	return view{
		snap: m.snap(), tab: m.tab, width: m.width, height: m.height, disconnected: m.st.disconnected,
		notice: m.notice, styled: true, fleet: &m.st, locked: m.f.locked != "", cursor: m.cursor,
	}
}
