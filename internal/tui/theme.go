package tui

import (
	"image/color"

	"charm.land/lipgloss/v2"

	"github.com/alexei-led/codex-router/internal/config"
)

// role is a color token. Segments carry roles, never colors, so one render
// serves a light and a dark terminal.
type role int

const (
	roleNone role = iota
	roleMuted
	roleAccent
	roleSurface // a background: the selected row, popovers, the toast
	roleOK
	roleWarn
	roleErr
	roleInfo
	roleMicro
	roleLow
	roleMedium
	roleHigh
)

var tierRole = map[config.Tier]role{
	config.TierMicro:  roleMicro,
	config.TierLow:    roleLow,
	config.TierMedium: roleMedium,
	config.TierHigh:   roleHigh,
}

// roleActivity colors activity names.
const roleActivity = roleInfo

// pair is one token as a light and a dark color.
type pair struct{ light, dark string }

var palette = map[role]pair{
	roleMuted:   {"#6b7280", "#7d8597"},
	roleAccent:  {"#3b5bdb", "#7c9cff"},
	roleSurface: {"#e3e7f0", "#2a2f3d"},
	roleOK:      {"#1a7f5a", "#5fd7af"},
	roleWarn:    {"#9a6700", "#f0b429"},
	roleErr:     {"#cf222e", "#ff6b6b"},
	roleInfo:    {"#8250df", "#c792ea"},
	roleMicro:   {"#1a7f5a", "#5fd7af"},
	roleLow:     {"#1f6feb", "#5f9fe0"},
	roleMedium:  {"#9a6700", "#d7af5f"},
	roleHigh:    {"#c2255c", "#e0708a"},
}

// theme resolves roles to colors for one terminal background.
type theme map[role]color.Color

func newTheme(dark bool) theme {
	pick := lipgloss.LightDark(dark)
	t := theme{}
	for r, p := range palette {
		t[r] = pick(lipgloss.Color(p.light), lipgloss.Color(p.dark))
	}
	return t
}
