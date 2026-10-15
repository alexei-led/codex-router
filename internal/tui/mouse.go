package tui

import "github.com/alexei-led/codex-router/internal/config"

type actionKind int

const (
	actNone   actionKind = iota
	actTab               // show tab
	actFocus             // focus session, from the top bar
	actSelect            // Sessions tab row: select, then open
	actPin               // pin tier, or unpin it when pinned
	actPreset            // set the focused home's preset
)

// action is what a click does.
type action struct {
	kind    actionKind
	tab     Tab
	session string
	tier    config.Tier
	preset  config.Preset
}

// zone is a clickable span of one screen row, x0 inclusive, x1 exclusive, in
// the cell widths the canvas cuts lines by.
type zone struct {
	action action
	row    int
	x0, x1 int
}

// hit finds the action under the cell (x, y); the first zone wins.
func hit(zones []zone, x, y int) (action, bool) {
	for _, z := range zones {
		if z.row == y && x >= z.x0 && x < z.x1 {
			return z.action, true
		}
	}
	return action{}, false
}
