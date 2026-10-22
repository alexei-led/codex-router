package tui

import (
	"context"
	"fmt"
	"os"

	tea "charm.land/bubbletea/v2"

	"github.com/alexei-led/codex-router/internal/config"
	"github.com/alexei-led/codex-router/internal/control"
	"github.com/alexei-led/codex-router/internal/registry"
	"github.com/alexei-led/codex-router/internal/route"
)

// Tab is one panel view. Tab and shift+tab cycle them.
type Tab string

const (
	TabSessions Tab = "sessions"
	TabPolicy   Tab = "policy"
	TabUsage    Tab = "usage"
)

// Tabs lists the views in display order.
var Tabs = []Tab{TabSessions, TabPolicy, TabUsage}

// Modes lists the mode popover's choices in key order: the routing modes,
// then default, which clears the session's override.
var Modes = append(append([]config.RoutingMode(nil), config.RoutingModes...), route.ModeDefault)

// Options configure Run. Socket, when set, shows that one endpoint and
// skips the registry; else the panel watches the registry dir Registry.
// Session, when set, locks the panel to that session id; else it shows all.
// NoMouse leaves the mouse to the terminal, so text selection works.
type Options struct {
	Socket   string
	Registry string
	Session  string
	NoMouse  bool
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
	m := newModel(f)
	m.mouse = !o.NoMouse
	p := tea.NewProgram(m, tea.WithContext(ctx))
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
