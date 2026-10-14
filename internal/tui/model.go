package tui

import (
	"errors"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/alexei-led/codex-router/internal/config"
	"github.com/alexei-led/codex-router/internal/control"
	"github.com/alexei-led/codex-router/internal/route"
)

const (
	heartbeat    = 2 * time.Second
	defaultWidth = 80
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
	sentMsg  struct{ err error }
)

type model struct {
	f      *fleet
	st     fleetState
	tab    Tab
	width  int
	notice string
}

func newModel(f *fleet) *model {
	return &model{f: f, st: fleetState{disconnected: true}, tab: TabNow, width: defaultWidth}
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
		m.width = msg.Width
	case fleetMsg:
		m.st = m.f.state()
		return m, waitFleetCmd(m.f)
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
	case tea.KeyMsg:
		return m, m.key(msg)
	}
	return m, nil
}

func (m *model) key(k tea.KeyMsg) tea.Cmd {
	switch s := k.String(); s {
	case "q", "ctrl+c":
		return tea.Quit
	case "1", "2", "3", "4", "5":
		m.tab = Tabs[s[0]-'1']
	case "tab":
		m.tab = Tabs[(m.tabIndex()+1)%len(Tabs)]
	case "shift+tab":
		m.tab = Tabs[(m.tabIndex()+len(Tabs)-1)%len(Tabs)]
	case "p":
		return m.command(pinCycle(m.st.focus, m.snap().Pin))
	case " ":
		return m.command(toggleCommand(m.st.focus, m.snap().Mode))
	case "P":
		return m.homeCommand(m.homeToggle())
	case "c":
		if m.tab == TabPolicy {
			return m.homeCommand(route.Command{Op: route.OpPreset, Preset: nextPreset(m.snap().Preset)})
		}
	}
	return nil
}

func (m *model) tabIndex() int {
	for i, t := range Tabs {
		if t == m.tab {
			return i
		}
	}
	return 0
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

func (m *model) View() string {
	return render(view{snap: m.snap(), tab: m.tab, width: m.width, disconnected: m.st.disconnected, notice: m.notice, styled: true})
}
