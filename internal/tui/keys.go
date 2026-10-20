package tui

import (
	"slices"

	"charm.land/bubbles/v2/key"
)

// keyMap is the panel's keys, the single definition of each action. Update
// dispatches on it, the footer and the help overlay read its help, and a
// click on a footer entry or a button runs the same binding.
type keyMap struct {
	Up          key.Binding
	Down        key.Binding
	Open        key.Binding
	Back        key.Binding
	PrevSession key.Binding
	NextSession key.Binding
	Next        key.Binding
	Prev        key.Binding
	Pin         key.Binding
	Toggle      key.Binding
	Home        key.Binding
	Preset      key.Binding
	Help        key.Binding
	Quit        key.Binding

	// PinPick and PresetPick are the choices of the open popover.
	PinPick    key.Binding
	PresetPick key.Binding
}

var keys = keyMap{
	Up:          key.NewBinding(key.WithKeys("up", "k"), key.WithHelp("↑↓", "select or scroll")),
	Down:        key.NewBinding(key.WithKeys("down", "j"), key.WithHelp("j k", "same as ↑↓")),
	Open:        key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "open")),
	Back:        key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "back")),
	PrevSession: key.NewBinding(key.WithKeys("["), key.WithHelp("[", "previous session")),
	NextSession: key.NewBinding(key.WithKeys("]"), key.WithHelp("]", "next session")),
	Next:        key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", "views")),
	Prev:        key.NewBinding(key.WithKeys("shift+tab"), key.WithHelp("shift+tab", "previous view")),
	Pin:         key.NewBinding(key.WithKeys("p"), key.WithHelp("p", "pin")),
	Toggle:      key.NewBinding(key.WithKeys("space"), key.WithHelp("space", "pause")),
	Home:        key.NewBinding(key.WithKeys("P"), key.WithHelp("P", "pause home")),
	Preset:      key.NewBinding(key.WithKeys("s"), key.WithHelp("s", "preset")),
	Help:        key.NewBinding(key.WithKeys("?"), key.WithHelp("?", "help")),
	Quit:        key.NewBinding(key.WithKeys("q", "ctrl+c"), key.WithHelp("q", "quit")),

	PinPick:    key.NewBinding(key.WithKeys("1", "2", "3", "4", "0"), key.WithHelp("1-4", "pick")),
	PresetPick: key.NewBinding(key.WithKeys("1", "2", "3"), key.WithHelp("1-3", "pick")),
}

// helpGroup is one titled block of the help overlay.
type helpGroup struct {
	title    string
	bindings []key.Binding
}

func (k keyMap) groups() []helpGroup {
	return []helpGroup{
		{"Navigate", []key.Binding{k.Up, k.Down, k.Open, k.Back, k.PrevSession, k.NextSession, k.Help, k.Quit}},
		{"Session", []key.Binding{k.Pin, k.Toggle}},
		{"Home", []key.Binding{k.Home, k.Preset}},
		{"Views", []key.Binding{k.Next, k.Prev}},
	}
}

// bindings lists every key the panel dispatches outside a popover.
func (k keyMap) bindings() []key.Binding {
	var out []key.Binding
	for _, g := range k.groups() {
		out = append(out, g.bindings...)
	}
	return out
}

// named finds the binding whose first key is name: the handle a click on a
// footer entry carries.
func (k keyMap) named(name string) (key.Binding, bool) {
	i := slices.IndexFunc(k.bindings(), func(b key.Binding) bool { return b.Keys()[0] == name })
	if i < 0 {
		return key.Binding{}, false
	}
	return k.bindings()[i], true
}

// handle is the name a click on b carries.
func handle(b key.Binding) string { return b.Keys()[0] }

// described is b with its footer text replaced.
func described(b key.Binding, desc string) key.Binding {
	b.SetHelp(b.Help().Key, desc)
	return b
}
