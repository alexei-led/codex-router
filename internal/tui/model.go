package tui

import (
	"errors"
	"slices"
	"strings"
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
	// toastFor is how long a confirmation stays.
	toastFor = 2500 * time.Millisecond
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
	// sentMsg is a command's ack. Done is the toast that confirms it.
	sentMsg struct {
		err  error
		done string
	}
	toastMsg struct{ seq int }
)

type model struct {
	f      *fleet
	st     fleetState
	tab    Tab
	width  int
	height int
	ending bool // the ended exit is scheduled
	notice string
	mouse  bool // the panel takes the mouse
	focus  time.Time
	now    func() time.Time

	light    bool // the terminal background is light; dark until it says so
	zoom     bool // stacked: the selected session's detail without the list
	pane     pane // side by side: which half has the focus
	help     bool
	popover  popover
	toast    string
	toastSeq int
}

func newModel(f *fleet) *model {
	return &model{f: f, st: fleetState{disconnected: true}, tab: TabSessions, width: defaultWidth, now: time.Now}
}

func (m *model) Init() tea.Cmd {
	return tea.Batch(waitFleetCmd(m.f), beatCmd(), tea.RequestBackgroundColor)
}

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
	case tea.BackgroundColorMsg:
		m.light = !msg.IsDark()
	case fleetMsg:
		m.st = m.f.state()
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
		case msg.done != "":
			m.toastSeq++
			m.toast = msg.done
			seq := m.toastSeq
			return m, tea.Tick(toastFor, func(time.Time) tea.Msg { return toastMsg{seq} })
		}
	case toastMsg:
		if msg.seq == m.toastSeq {
			m.toast = ""
		}
	case tea.KeyPressMsg:
		return m, m.key(msg)
	case tea.FocusMsg:
		m.focus = m.now()
	case tea.MouseClickMsg:
		return m, m.click(msg.Mouse())
	case tea.MouseWheelMsg:
		if m.view().listed() {
			switch msg.Mouse().Button {
			case tea.MouseWheelUp:
				return m, m.selectStep(-1)
			case tea.MouseWheelDown:
				return m, m.selectStep(1)
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
	m.popover = popNone
	switch a.kind {
	case actClose:
		m.help = false
	case actTab:
		m.show(a.tab, 1)
	case actSelect:
		if a.session != m.st.focus {
			return m.focusOn(a.session)
		}
		return m.run(keys.Open)
	case actPin:
		if p := m.snap().Pin; p != nil && *p == a.tier {
			return m.unpin()
		}
		return m.pin(a.tier)
	case actUnpin:
		return m.unpin()
	case actPreset:
		return m.setPreset(a.preset)
	case actKey:
		if b, ok := keys.named(a.key); ok {
			return m.run(b)
		}
	}
	return nil
}

// key dispatches a key press. An open overlay takes the keys first.
func (m *model) key(k tea.KeyPressMsg) tea.Cmd {
	switch {
	case key.Matches(k, keys.Quit):
		return tea.Quit
	case m.help:
		if key.Matches(k, keys.Back, keys.Help) {
			m.help = false
		}
		return nil
	case m.popover != popNone:
		return m.choose(k)
	}
	for _, b := range keys.bindings() {
		if key.Matches(k, b) {
			return m.run(b)
		}
	}
	return nil
}

// choose handles a key while a popover is open: esc closes it, a number picks.
func (m *model) choose(k tea.KeyPressMsg) tea.Cmd {
	if key.Matches(k, keys.Back) {
		m.popover = popNone
		return nil
	}
	n := strings.Index("0123456789", k.String())
	pop := m.popover
	switch {
	case pop == popPin && key.Matches(k, keys.PinPick):
		m.popover = popNone
		if n == 0 {
			return m.unpin()
		}
		return m.pin(tierOrder[n-1])
	case pop == popPreset && key.Matches(k, keys.PresetPick):
		m.popover = popNone
		return m.setPreset(Presets[n-1])
	}
	return nil
}

func is(a, b key.Binding) bool { return handle(a) == handle(b) }

// run does what binding b stands for; a key press and a click on a footer
// entry both come here.
func (m *model) run(b key.Binding) tea.Cmd {
	v := m.view()
	switch {
	case is(b, keys.Quit):
		return tea.Quit
	case is(b, keys.Help):
		m.help = true
	case is(b, keys.Back):
		m.zoom, m.pane = false, paneList
	case is(b, keys.Next):
		m.cycle(1)
	case is(b, keys.Prev):
		m.cycle(-1)
	case is(b, keys.Up):
		if v.listed() {
			return m.selectStep(-1)
		}
	case is(b, keys.Down):
		if v.listed() {
			return m.selectStep(1)
		}
	case is(b, keys.Open):
		if v.listed() && m.st.focus != "" {
			if v.wide() {
				m.pane = paneDetail
			} else {
				m.zoom = true
			}
		}
	case is(b, keys.PrevSession):
		if v.multi() {
			return m.step(-1)
		}
	case is(b, keys.NextSession):
		if v.multi() {
			return m.step(1)
		}
	case is(b, keys.Pin):
		if m.tab == TabSessions && m.st.focus != "" {
			m.popover = popPin
		}
	case is(b, keys.Preset):
		if m.tab == TabPolicy && m.st.focus != "" {
			m.popover = popPreset
		}
	case is(b, keys.Toggle):
		cmd := toggleCommand(m.st.focus, m.snap().Mode)
		return m.command(cmd, map[route.Op]string{route.OpPause: "session paused", route.OpResume: "session resumed"}[cmd.Op])
	case is(b, keys.Home):
		cmd := m.homeToggle()
		return m.homeCommand(cmd, map[route.Op]string{route.OpPause: "home paused", route.OpResume: "home resumed"}[cmd.Op])
	}
	return nil
}

func (m *model) pin(t config.Tier) tea.Cmd {
	return m.command(route.Command{Op: route.OpPin, Session: m.st.focus, Tier: t}, "pinned "+string(t)+" for the next turn")
}

func (m *model) unpin() tea.Cmd {
	return m.command(route.Command{Op: route.OpUnpin, Session: m.st.focus}, "pin cleared")
}

func (m *model) setPreset(p config.Preset) tea.Cmd {
	return m.homeCommand(route.Command{Op: route.OpPreset, Preset: p}, "preset "+string(p)+" sent to the home")
}

// show opens view t. Going on into Sessions lands on the list, going back
// on the detail, which is the last stop of the tab ring when it is side by side.
func (m *model) show(t Tab, dir int) {
	m.tab, m.zoom, m.pane = t, false, paneList
	if dir < 0 && t == TabSessions {
		m.pane = paneDetail
	}
}

// cycle moves d stops along the ring of panes and views: side by side, the
// Sessions view has two stops, the list and the detail.
func (m *model) cycle(d int) {
	if v := m.view(); v.wide() && m.tab == TabSessions {
		if d > 0 && m.pane == paneList {
			m.pane = paneDetail
			return
		}
		if d < 0 && m.pane == paneDetail {
			m.pane = paneList
			return
		}
	}
	i := slices.Index(Tabs, m.tab)
	m.show(Tabs[(i+d+len(Tabs))%len(Tabs)], d)
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

// selectStep moves the selection d rows, stopping at the ends. The selected
// session is the focused one: the detail shows it.
func (m *model) selectStep(d int) tea.Cmd {
	all := m.st.all()
	if len(all) == 0 {
		return nil
	}
	i, _ := m.listed(m.st.focus)
	return m.focusOn(all[min(max(i+d, 0), len(all)-1)].ID)
}

// focusOn focuses session id. The panel shows it at once; the fleet
// resubscribes in the background and confirms with its next state.
func (m *model) focusOn(id string) tea.Cmd {
	if id == m.st.focus || !m.f.pick(id) {
		return nil
	}
	m.st.focus, m.st.snap = id, nil
	f := m.f
	return func() tea.Msg { f.sync(); return nil }
}

// snap is the focused session's snapshot, empty until one arrives.
func (m *model) snap() route.Snapshot {
	if m.st.snap == nil {
		return route.Snapshot{}
	}
	return *m.st.snap
}

// command sends a command for the focused session; done is the toast the
// ack earns.
func (m *model) command(cmd route.Command, done string) tea.Cmd {
	if m.st.focus == "" || m.st.disconnected {
		m.notice = errNotConnected.Error()
		return nil
	}
	f := m.f
	return func() tea.Msg { return sentMsg{err: f.send(cmd), done: done} }
}

// homeCommand sends cmd to every endpoint of the focused session's home.
func (m *model) homeCommand(cmd route.Command, done string) tea.Cmd {
	s, ok := m.st.focused()
	if !ok {
		m.notice = errNotConnected.Error()
		return nil
	}
	f, h := m.f, s.Home
	return func() tea.Msg { return sentMsg{err: f.sendHome(h, cmd), done: done} }
}

// homeToggle pauses the focused home unless every session of it is already
// paused, then it resumes. The sessions list carries no home pause flag, so
// a home whose sessions are all paused one by one gets a resume that keeps
// those session pauses; space resumes each.
func (m *model) homeToggle() route.Command {
	if m.st.homeResumes() {
		return route.Command{Op: route.OpResume}
	}
	return route.Command{Op: route.OpPause}
}

// homeResumes reports whether P resumes the focused home: every session of
// it is paused.
func (s fleetState) homeResumes() bool {
	f, _ := s.focused()
	h, _ := s.homeOf(f.Home)
	for _, x := range h.sessions {
		if x.Mode != route.ModePaused {
			return false
		}
	}
	return true
}

// toggleCommand pauses session, or resumes it when paused or manual.
func toggleCommand(session string, mode route.Mode) route.Command {
	if mode == route.ModeOn || mode == "" {
		return route.Command{Op: route.OpPause, Session: session}
	}
	return route.Command{Op: route.OpResume, Session: session}
}

func (m *model) View() tea.View {
	out, _ := layout(m.view())
	v := tea.NewView(out)
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
		notice: m.notice, styled: true, light: m.light, fleet: &m.st, locked: m.f.locked != "",
		loading: m.st.snap == nil && m.st.focus != "" && !m.st.disconnected,
		zoom:    m.zoom, pane: m.pane, help: m.help, popover: m.popover, toast: m.toast,
	}
}
