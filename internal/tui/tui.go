package tui

import (
	"context"
	"fmt"
	"os"

	tea "charm.land/bubbletea/v2"

	"github.com/alexei-led/codex-router/internal/config"
	"github.com/alexei-led/codex-router/internal/control"
	"github.com/alexei-led/codex-router/internal/registry"
)

// Tab is one panel tab, in key order 0-5.
type Tab string

const (
	TabSessions   Tab = "sessions" // tab 0, only when the panel shows all sessions
	TabNow        Tab = "now"
	TabRoutes     Tab = "routes"
	TabPolicy     Tab = "policy"
	TabClassifier Tab = "classifier"
	TabUsage      Tab = "usage"
)

// Tabs lists the tabs of one session, keys 1-5, in display order.
var Tabs = []Tab{TabNow, TabRoutes, TabPolicy, TabClassifier, TabUsage}

// tabsFor lists the panel's tabs in key order; withSessions puts the
// Sessions tab 0 first.
func tabsFor(withSessions bool) []Tab {
	if withSessions {
		return append([]Tab{TabSessions}, Tabs...)
	}
	return Tabs
}

// Presets lists the policy presets in cycling order.
var Presets = []config.Preset{config.PresetThrifty, config.PresetBalanced, config.PresetQuality}

// Options configure Run. Socket, when set, shows that one endpoint and
// skips the registry; else the panel watches the registry dir Registry.
// Session, when set, locks the panel to that session id; else it shows all.
type Options struct {
	Socket   string
	Registry string
	Session  string
}

// Run shows the panel until the user quits or ctx ends. It redials lost
// endpoints while their registry entries remain.
func Run(ctx context.Context, o Options) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var f *fleet
	if o.Socket != "" {
		f = newFleet(socketSource(o.Socket), dialControl)
	} else {
		f = newFleet(registrySource(o.Registry), dialControl)
		f.prune = registryPrune(o.Registry)
	}
	f.locked = o.Session
	f.cwd, _ = os.Getwd()
	go f.run(ctx)
	p := tea.NewProgram(newModel(f), tea.WithContext(ctx))
	if _, err := p.Run(); err != nil {
		return fmt.Errorf("run panel: %w", err)
	}
	return nil
}

func dialControl(ep registry.Endpoint) (conn, error) {
	c, err := control.Dial(ep.Socket)
	if err != nil {
		return nil, err
	}
	return c, nil
}
