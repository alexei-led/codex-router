package tui

import "charm.land/bubbles/v2/key"

// keyMap is the panel's keys. Update matches against it and the hint footer
// reads its help, so the two cannot drift.
type keyMap struct {
	Quit    key.Binding
	TabNum  key.Binding
	List    key.Binding
	Next    key.Binding
	Prev    key.Binding
	Session key.Binding
	Up      key.Binding
	Down    key.Binding
	Enter   key.Binding
	Pin     key.Binding
	Toggle  key.Binding
	Home    key.Binding
	Preset  key.Binding
}

var keys = keyMap{
	Quit:    key.NewBinding(key.WithKeys("q", "ctrl+c"), key.WithHelp("q", "quit")),
	TabNum:  key.NewBinding(key.WithKeys("1", "2", "3", "4", "5"), key.WithHelp("1-5", "tabs")),
	List:    key.NewBinding(key.WithKeys("0"), key.WithHelp("0", "list")),
	Next:    key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", "next")),
	Prev:    key.NewBinding(key.WithKeys("shift+tab")),
	Session: key.NewBinding(key.WithKeys("[", "]"), key.WithHelp("[ ]", "session")),
	Up:      key.NewBinding(key.WithKeys("up"), key.WithHelp("↑↓", "select")),
	Down:    key.NewBinding(key.WithKeys("down")),
	Enter:   key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "focus")),
	Pin:     key.NewBinding(key.WithKeys("p"), key.WithHelp("p", "pin")),
	Toggle:  key.NewBinding(key.WithKeys("space"), key.WithHelp("space", "pause")),
	Home:    key.NewBinding(key.WithKeys("P"), key.WithHelp("P", "pause home")),
	Preset:  key.NewBinding(key.WithKeys("c"), key.WithHelp("c", "preset")),
}

// hint is b's help as one footer entry; desc replaces the help text when set.
func hint(b key.Binding, desc string) string {
	h := b.Help()
	if desc == "" {
		desc = h.Desc
	}
	return h.Key + " " + desc
}
