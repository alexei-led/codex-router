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
	redialMin    = 250 * time.Millisecond
	redialMax    = 2 * time.Second
	defaultWidth = 80
)

// conn is the part of control.Client the panel uses.
type conn interface {
	Updates() <-chan route.Snapshot
	Send(route.Command) error
	Close() error
}

type (
	dialedMsg struct {
		c   conn
		err error
	}
	redialMsg struct{}
	snapMsg   route.Snapshot
	closedMsg struct{}
	beatMsg   struct{}
	sentMsg   struct {
		err error
	}
)

type model struct {
	socket       string
	dial         func(string) (conn, error)
	c            conn
	snap         route.Snapshot
	disconnected bool
	tab          Tab
	width        int
	notice       string
	backoff      time.Duration
}

func newModel(socket string, dial func(string) (conn, error)) *model {
	return &model{socket: socket, dial: dial, tab: TabNow, width: defaultWidth, disconnected: true, backoff: redialMin}
}

func (m *model) Init() tea.Cmd { return m.dialCmd() }

func (m *model) dialCmd() tea.Cmd {
	return func() tea.Msg {
		c, err := m.dial(m.socket)
		return dialedMsg{c, err}
	}
}

func waitCmd(c conn) tea.Cmd {
	return func() tea.Msg {
		s, ok := <-c.Updates()
		if !ok {
			return closedMsg{}
		}
		return snapMsg(s)
	}
}

func beatCmd() tea.Cmd {
	return tea.Tick(heartbeat, func(time.Time) tea.Msg { return beatMsg{} })
}

func sendCmd(c conn, cmd route.Command) tea.Cmd {
	return func() tea.Msg { return sentMsg{c.Send(cmd)} }
}

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
	case dialedMsg:
		return m, m.dialed(msg)
	case redialMsg:
		return m, m.dialCmd()
	case snapMsg:
		m.snap, m.disconnected, m.notice = route.Snapshot(msg), false, ""
		return m, waitCmd(m.c)
	case closedMsg:
		return m, m.lost()
	case beatMsg:
		if m.c == nil {
			return m, nil
		}
		return m, tea.Batch(sendCmd(m.c, route.Command{Op: route.OpSnapshot}), beatCmd())
	case sentMsg:
		return m, m.sent(msg.err)
	case tea.KeyMsg:
		return m, m.key(msg)
	}
	return m, nil
}

func (m *model) dialed(msg dialedMsg) tea.Cmd {
	if msg.err != nil {
		m.disconnected = true
		wait := m.backoff
		m.backoff = min(m.backoff*2, redialMax)
		return tea.Tick(wait, func(time.Time) tea.Msg { return redialMsg{} })
	}
	m.c, m.backoff = msg.c, redialMin
	return tea.Batch(waitCmd(m.c), beatCmd(), sendCmd(m.c, route.Command{Op: route.OpSnapshot}))
}

func (m *model) lost() tea.Cmd {
	m.disconnected = true
	m.c = nil
	return tea.Tick(m.backoff, func(time.Time) tea.Msg { return redialMsg{} })
}

func (m *model) sent(err error) tea.Cmd {
	switch {
	case errors.Is(err, control.ErrDisconnected):
		m.disconnected = true
	case err != nil:
		m.notice = "command failed: " + err.Error()
	default:
		m.disconnected = false
		m.notice = ""
	}
	return nil
}

func (m *model) key(k tea.KeyMsg) tea.Cmd {
	switch s := k.String(); s {
	case "q", "ctrl+c":
		if m.c != nil {
			_ = m.c.Close()
		}
		return tea.Quit
	case "1", "2", "3", "4", "5":
		m.tab = Tabs[s[0]-'1']
	case "tab":
		m.tab = Tabs[(m.tabIndex()+1)%len(Tabs)]
	case "shift+tab":
		m.tab = Tabs[(m.tabIndex()+len(Tabs)-1)%len(Tabs)]
	case "p":
		return m.command(pinCycle(m.snap.Pin))
	case " ":
		return m.command(toggleCommand(m.snap.Mode))
	case "c":
		if m.tab == TabPolicy {
			return m.command(route.Command{Op: route.OpPreset, Preset: nextPreset(m.snap.Preset)})
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

func (m *model) command(cmd route.Command) tea.Cmd {
	if m.c == nil || m.disconnected {
		m.notice = "disconnected: command not sent"
		return nil
	}
	return sendCmd(m.c, cmd)
}

// pinCycle walks none, high, medium, low, micro, none.
func pinCycle(cur *config.Tier) route.Command {
	if cur == nil {
		return route.Command{Op: route.OpPin, Tier: tierOrder[0]}
	}
	for i, t := range tierOrder {
		if t == *cur && i+1 < len(tierOrder) {
			return route.Command{Op: route.OpPin, Tier: tierOrder[i+1]}
		}
	}
	return route.Command{Op: route.OpUnpin}
}

func toggleCommand(mode route.Mode) route.Command {
	if mode == route.ModeOn || mode == "" {
		return route.Command{Op: route.OpPause}
	}
	return route.Command{Op: route.OpResume}
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
	return render(view{snap: m.snap, tab: m.tab, width: m.width, disconnected: m.disconnected, notice: m.notice, styled: true})
}
