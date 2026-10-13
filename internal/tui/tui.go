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

// Options configure Run.
type Options struct {
	Socket string
}

// Run shows the panel until the user quits or ctx ends. It redials the
// control socket when it is lost.
func Run(ctx context.Context, o Options) error {
	dial := func(path string) (conn, error) { return control.Dial(path) }
	p := tea.NewProgram(newModel(o.Socket, dial), tea.WithContext(ctx), tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		return fmt.Errorf("run panel: %w", err)
	}
	return nil
}
