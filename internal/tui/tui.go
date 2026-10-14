package tui

import (
	"context"
	"fmt"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/alexei-led/codex-router/internal/config"
	"github.com/alexei-led/codex-router/internal/control"
)

// Tab is one panel tab, in key order 1-5.
type Tab string

const (
	TabNow        Tab = "now"
	TabRoutes     Tab = "routes"
	TabPolicy     Tab = "policy"
	TabClassifier Tab = "classifier"
	TabUsage      Tab = "usage"
)

// Tabs lists the tabs in display order.
var Tabs = []Tab{TabNow, TabRoutes, TabPolicy, TabClassifier, TabUsage}

// Presets lists the policy presets in cycling order.
var Presets = []config.Preset{config.PresetThrifty, config.PresetBalanced, config.PresetQuality}

// Options configure Run. Locate, when set, finds the socket again before
// every dial, so a panel follows a restarted session instead of redialing
// the dead one's socket.
type Options struct {
	Socket string
	Locate func() (string, error)
}

// Run shows the panel until the user quits or ctx ends. It redials the
// control socket when it is lost.
func Run(ctx context.Context, o Options) error {
	dial := dialer(o.Locate, func(path string) (conn, error) { return control.Dial(path) })
	p := tea.NewProgram(newModel(o.Socket, dial), tea.WithContext(ctx), tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		return fmt.Errorf("run panel: %w", err)
	}
	return nil
}

func dialer(locate func() (string, error), dial func(string) (conn, error)) func(string) (conn, error) {
	if locate == nil {
		return dial
	}
	return func(string) (conn, error) {
		path, err := locate()
		if err != nil {
			return nil, err
		}
		return dial(path)
	}
}
