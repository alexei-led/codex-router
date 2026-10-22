package tui

import (
	"strconv"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/alexei-led/codex-router/internal/route"
)

const (
	helpKeyWidth = 11
	// helpColumns is the width from which the help overlay has two columns.
	helpColumns = 80
	helpTop     = 2
)

// compose draws the overlays over the laid-out panel: the help overlay or an
// open popover, which take the clicks, then the toast. The panel's own zones
// stay while only a toast shows.
func (c *canvas) compose(v view, footerRow int) (string, []zone) {
	base := strings.Join(c.out, "\n")
	var overlays []*lipgloss.Layer
	zones := c.zones
	overlayOpen := false
	switch {
	case v.help:
		all := strings.Split(strings.Join(c.helpLines(), "\n"), "\n")
		if v.height > 0 {
			var wrapped []string
			width := max(c.width-4, 1)
			for _, line := range all {
				if ansi.StringWidth(line) > width {
					wrapped = append(wrapped, strings.Split(ansi.Hardwrap(line, width, false), "\n")...)
				} else {
					wrapped = append(wrapped, line)
				}
			}
			maxLines := max(footerRow-helpTop-3, 1)
			if len(wrapped) > maxLines {
				wrapped = wrapped[:maxLines]
				wrapped[maxLines-1] = c.paint(dim("… more help hidden at this height"))
			}
			all = wrapped
		}
		box := c.box([]string{strings.Join(all, "\n")}, roleAccent, c.width)
		y := helpTop
		if v.height > 0 {
			y = min(y, max(footerRow-lipgloss.Height(box)-1, 0))
		}
		overlays = append(overlays, lipgloss.NewLayer(box).Y(y).Z(1))
		zones, overlayOpen = nil, true
	case v.popover != popNone:
		if a, ok := c.popoverAnchor(v); ok {
			layer, zs := c.popover(v, a, footerRow)
			overlays, zones = append(overlays, layer), zs
			overlayOpen = true
		}
	}
	if overlayOpen {
		rows := len(c.out)
		if v.height > 0 {
			rows = min(rows, v.height)
		} else {
			rows += 8
		}
		for row := range rows {
			zones = append(zones, zone{action: action{kind: actClose}, row: row, x1: c.width})
		}
	}
	if v.toast != "" && !v.help && v.popover == popNone {
		overlays = append(overlays, c.toast(v.toast, footerRow))
	}
	if len(overlays) == 0 {
		return base + "\n", zones
	}
	layers := append([]*lipgloss.Layer{lipgloss.NewLayer(base)}, overlays...)
	return lipgloss.NewCompositor(layers...).Render() + "\n", zones
}

// box frames lines in a rounded border; width 0 fits the lines.
func (c *canvas) box(lines []string, border role, width int) string {
	st := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).Padding(0, 1)
	if width > 0 {
		st = st.Width(width)
	}
	if c.styled {
		st = st.BorderForeground(c.th[border])
	}
	return st.Render(strings.Join(lines, "\n"))
}

// helpLines is every binding, grouped, in one or two columns.
func (c *canvas) helpLines() []string {
	var blocks []string
	for _, g := range keys.groups() {
		lines := []string{c.paint(seg{text: g.title, role: roleMuted, bold: true})}
		for _, b := range g.bindings {
			h := b.Help()
			lines = append(lines, c.paint(seg{text: pad(h.Key, helpKeyWidth), role: roleAccent, bold: true})+h.Desc)
		}
		blocks = append(blocks, strings.Join(lines, "\n"))
	}
	note := c.paint(dim("click a row to select it, again to open it · wheel scrolls · click a view name, a button or a footer key to use it"))
	note = lipgloss.NewStyle().Width(c.width - 4).Render(note)
	if c.width < helpColumns {
		return []string{strings.Join(append(blocks, note), "\n\n")}
	}
	right := strings.Join(blocks[1:], "\n\n")
	cols := lipgloss.JoinHorizontal(lipgloss.Top, blocks[0], "      ", right)
	return []string{cols + "\n\n" + note}
}

func (c *canvas) popoverAnchor(v view) (anchor, bool) {
	name := "pin"
	if v.popover == popMode {
		name = "mode"
	}
	a, ok := c.anchors[name]
	return a, ok
}

// popover is the open choice box hung under the row it belongs to, with a
// zone for each choice. A narrow panel wraps the choices onto more lines.
func (c *canvas) popover(v view, a anchor, footerRow int) (*lipgloss.Layer, []zone) {
	var items [][]seg
	choice := func(k, name string, r role, act action) {
		items = append(items, []seg{
			clickable(seg{text: k, role: roleAccent, bold: true}, act),
			clickable(seg{text: " " + name, role: r}, act),
		})
	}
	if v.popover == popPin {
		for i, t := range tierOrder {
			choice(strconv.Itoa(i+1), string(t), tierRole[t], action{kind: actPin, tier: t})
		}
		choice("0", "none", roleMuted, action{kind: actUnpin})
	} else {
		for i, mode := range Modes {
			r := roleAccent
			if mode == route.ModeDefault {
				r = roleMuted
			}
			choice(strconv.Itoa(i+1), string(mode), r, action{kind: actMode, mode: mode})
		}
	}
	pc := c.child(c.width)
	var line []seg
	flush := func() {
		if line != nil {
			pc.add(line...)
			line = nil
		}
	}
	for _, it := range items {
		if line != nil && segsWidth(line)+lipgloss.Width(gap)+segsWidth(it) > c.width-4 {
			flush()
		}
		if line != nil {
			line = append(line, plain(gap))
		}
		line = append(line, it...)
	}
	flush()
	box := c.box(pc.out, roleAccent, 0)
	x := min(a.x, max(c.width-lipgloss.Width(box), 0))
	y := a.row + 1
	if v.height > 0 {
		boxHeight := lipgloss.Height(box)
		bottom := min(v.height, max(footerRow-1, 0))
		if y+boxHeight > bottom {
			y = max(a.row-boxHeight, 0)
		}
		y = min(y, max(bottom-boxHeight, 0))
	}
	zs := make([]zone, 0, len(pc.zones))
	for _, z := range pc.zones {
		zs = append(zs, zone{action: z.action, row: y + 1 + z.row, x0: x + 2 + z.x0, x1: x + 2 + z.x1})
	}
	return lipgloss.NewLayer(c.padded(box, x)).Y(y).Z(1), zs
}

// padded is box in the middle of full-width blank lines, so the rows it covers
// do not show through around it.
func (c *canvas) padded(box string, x int) string {
	lines := strings.Split(box, "\n")
	for i, l := range lines {
		lines[i] = strings.Repeat(" ", x) + l + strings.Repeat(" ", max(c.width-x-lipgloss.Width(l), 0))
	}
	return strings.Join(lines, "\n")
}

// toast is a one-line confirmation against the right edge, just above the
// footer.
func (c *canvas) toast(text string, footerRow int) *lipgloss.Layer {
	t := " ✓ " + text + " "
	if c.styled {
		t = lipgloss.NewStyle().Foreground(c.th[roleOK]).Background(c.th[roleSurface]).Bold(true).Render(t)
	}
	x := max(c.width-lipgloss.Width(t)-1, 0)
	return lipgloss.NewLayer(t).X(x).Y(max(footerRow-1, 0)).Z(1)
}
