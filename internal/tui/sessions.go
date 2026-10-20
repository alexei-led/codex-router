package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"

	"github.com/alexei-led/codex-router/internal/config"
	"github.com/alexei-led/codex-router/internal/home"
	"github.com/alexei-led/codex-router/internal/registry"
	"github.com/alexei-led/codex-router/internal/route"
)

const (
	gap           = "  "
	sparkCells    = 10
	switchWidth   = 7 // "18:42 ↑"
	minSessionCol = 16
	maxSessionCol = 32
	// listShare is the list's share of a side-by-side panel, in percent.
	listShare = 45
	// minListRows is the fewest list lines worth drawing above the detail.
	minListRows = 4
	waitingText = "○ waiting for the first prompt"
	noSession   = "no Codex session yet · run codex-router in a project to start one"
)

// homeRoles color each home's dot, by the home's place in the list.
var homeRoles = []role{roleAccent, roleInfo, roleOK, roleMedium, roleHigh}

// spark draws a tier in the LAST TURNS column.
var spark = map[config.Tier]string{
	config.TierMicro: "▁", config.TierLow: "▃", config.TierMedium: "▅", config.TierHigh: "▇",
}

// FleetView is what RenderFleetView draws for the docs images. Each session
// is on its own endpoint, as stage 1 launchers run them, unless its home is
// in Brains: then one brain serves the home, and the value is how long it
// has run. Waiting lists, by home id, the labels of routers whose Codex has
// not sent a prompt yet.
type FleetView struct {
	Homes    []home.Home
	Sessions []route.SessionInfo
	Brains   map[string]time.Duration
	Waiting  map[string][]string
	Focus    string
	Snap     route.Snapshot // the focused session's snapshot
	Tab      Tab
	Width    int
	Height   int
	Help     bool
	Popover  bool // the pin popover is open
	Locked   bool // a panel run with --session: no list
}

// RenderFleetView draws v with ANSI colors. Output depends only on v.
func RenderFleetView(v FleetView) string {
	st := fleetState{focus: v.Focus, snap: &v.Snap}
	for _, h := range v.Homes {
		g := fleetHome{home: h}
		if up, ok := v.Brains[h.ID]; ok {
			g.endpoints = append(g.endpoints, fleetEndpointInfo{id: "brain-" + h.ID, kind: registry.KindBrain, up: up})
		}
		for _, x := range v.Sessions {
			if x.Home != h.ID {
				continue
			}
			ep := x.ID
			if _, ok := v.Brains[h.ID]; ok {
				ep = "brain-" + h.ID
			} else {
				g.endpoints = append(g.endpoints, fleetEndpointInfo{id: ep, kind: registry.KindSession})
			}
			g.sessions = append(g.sessions, fleetSession{SessionInfo: x, endpoint: ep})
		}
		for _, l := range v.Waiting[h.ID] {
			g.endpoints = append(g.endpoints, fleetEndpointInfo{id: "waiting-" + l, kind: registry.KindSession, label: l, idle: true})
		}
		st.homes = append(st.homes, g)
	}
	pop := popNone
	if v.Popover {
		pop = popPin
	}
	if v.Tab == "" {
		v.Tab = TabSessions
	}
	var b strings.Builder
	w := colorprofile.Writer{Forward: &b, Profile: colorprofile.ANSI256}
	_, _ = w.WriteString(render(view{
		snap: v.Snap, tab: v.Tab, width: v.Width, height: v.Height, styled: true, fleet: &st, locked: v.Locked,
		help: v.Help, popover: pop,
	}))
	return b.String()
}

// all lists every session, home by home, in display order.
func (s fleetState) all() []fleetSession {
	var out []fleetSession
	for _, h := range s.homes {
		out = append(out, h.sessions...)
	}
	return out
}

func (s fleetState) homeRole(id string) role {
	for i, h := range s.homes {
		if h.home.ID == id {
			return homeRoles[i%len(homeRoles)]
		}
	}
	return roleMuted
}

func cells(s string) int { return ansi.StringWidth(s) }

// clip cuts s to n terminal cells, ending with … when cut.
func clip(s string, n int) string {
	if cells(s) <= n {
		return s
	}
	if n <= 0 {
		return ""
	}
	return ansi.Truncate(s, n, "…")
}

// pad clips s to n terminal cells and fills it with spaces to n.
func pad(s string, n int) string {
	s = clip(s, n)
	return s + strings.Repeat(" ", max(n-cells(s), 0))
}

func plural(n int, one, many string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, one)
	}
	return fmt.Sprintf("%d %s", n, many)
}

// sessions is the Sessions view: the list and the selected session's detail,
// stacked, side by side, or the detail alone.
func (c *canvas) sessions(v view, room int, limited bool) {
	bodyStart := len(c.out)
	switch {
	case v.wide():
		c.sideBySide(v, room, limited)
	case v.listed() && v.fleet.focus == "":
		if limited && room == 0 {
			return
		}
		c.list(v, room)
	case v.listed():
		if limited && room == 0 {
			return
		}
		if !limited {
			c.list(v, 0)
			c.blank()
			d := c.child(c.width)
			d.detail(v)
			c.layout = layoutInfo{detailRow: len(c.out), hasDetail: true}
			c.append(d)
			return
		}
		if room <= 2 {
			d := c.child(c.width)
			d.detail(v)
			c.layout = layoutInfo{detailRow: len(c.out), hasDetail: true, scrollMax: max(len(d.out)-room, 0)}
			d = crop(d, room, v.scroll)
			c.append(d)
			return
		}
		d := c.child(c.width)
		d.detail(v)
		detailRoom := min(len(d.out), max(room-minListRows-1, 0))
		listRoom := room - detailRoom - 1
		c.list(v, listRoom)
		if len(c.out)-bodyStart < room {
			c.blank()
		}
		available := max(room-(len(c.out)-bodyStart), 0)
		scrollMax := 0
		if available > 0 {
			scrollMax = max(len(d.out)-available, 0)
		}
		d = crop(d, available, v.scroll)
		c.layout = layoutInfo{detailRow: len(c.out), hasDetail: true, scrollMax: scrollMax}
		c.append(d)
	case v.fleet != nil && v.fleet.focus == "":
		c.add(dim(noSession))
	default:
		d := c.child(c.width)
		d.detail(v)
		scrollMax := 0
		if limited {
			scrollMax = max(len(d.out)-room, 0)
			d = crop(d, room, v.scroll)
		}
		c.layout = layoutInfo{detailRow: len(c.out), hasDetail: true, scrollMax: scrollMax}
		c.append(d)
	}
}

// sideBySide draws the list on the left and the detail on the right, with an
// accent gutter on the focused pane.
func (c *canvas) sideBySide(v view, room int, limited bool) {
	if limited && room == 0 {
		return
	}
	lw := c.width * listShare / 100
	rw := c.width - lw - 3
	l, r := c.child(lw), c.child(rw)
	l.list(v, room)
	if v.fleet.focus == "" {
		r.add(dim(noSession))
	} else {
		r.detail(v)
	}
	scrollMax := 0
	if limited {
		rScroll := 0
		if v.pane == paneDetail {
			rScroll = v.scroll
			scrollMax = max(len(r.out)-room, 0)
		}
		r = crop(r, room, rScroll)
	}
	bodyStart := len(c.out)
	for i := range max(len(l.out), len(r.out)) {
		left, lwid := "", 0
		if i < len(l.out) {
			left, lwid = l.out[i], l.widths[i]
		}
		right, rwid := "", 0
		if i < len(r.out) {
			right, rwid = r.out[i], r.widths[i]
		}
		mark := func(p pane) string {
			if v.pane == p {
				return c.paint(seg{text: "▎", role: roleAccent})
			}
			return " "
		}
		c.out = append(c.out, mark(paneList)+left+strings.Repeat(" ", lw-lwid)+" "+mark(paneDetail)+right)
		c.widths = append(c.widths, lw+3+rwid)
	}
	base := bodyStart
	for _, z := range l.zones {
		z.row, z.x0, z.x1 = z.row+base, z.x0+1, z.x1+1
		c.zones = append(c.zones, z)
	}
	for _, z := range r.zones {
		z.row, z.x0, z.x1 = z.row+base, z.x0+lw+3, z.x1+lw+3
		c.zones = append(c.zones, z)
	}
	for k, a := range r.anchors {
		a.row, a.x = a.row+base, a.x+lw+3
		c.anchors[k] = a
	}
	c.layout = layoutInfo{detailRow: base, detailX: lw + 3, hasDetail: v.fleet.focus != "", scrollMax: scrollMax}
}

// listLine is one line of the list body; session is set on session rows.
type listLine struct {
	segs    []seg
	session string
}

// listCols are the list's column widths; model, turns and sw are dropped,
// in that order of importance, when the width runs out.
type listCols struct {
	session, state, model int
	turns, sw             bool
}

// listColumns sizes the columns to the sessions listed and the width.
func listColumns(width int, st fleetState) listCols {
	cols := listCols{session: len("SESSION"), state: len("STATE"), model: len("MODEL")}
	for _, h := range st.homes {
		for _, e := range h.endpoints {
			if e.idle {
				cols.session = max(cols.session, cells(waitingName(e, h)))
			}
		}
	}
	for _, s := range st.all() {
		text := stateText(s)
		cols.session = max(cols.session, cells(rowLabel(s)))
		cols.state = max(cols.state, cells(text))
		cols.model = max(cols.model, cells(routeText(s.Route)))
	}
	cols.session = min(cols.session, maxSessionCol)
	used := cells("▸ ") + minSessionCol + cells(gap) + cols.state
	fits := func(w int) bool {
		if used+cells(gap)+w > width {
			return false
		}
		used += cells(gap) + w
		return true
	}
	switch {
	case !fits(cols.model):
		cols.model = 0
	case !fits(sparkCells):
	case !fits(switchWidth):
		cols.turns = true
	default:
		cols.turns, cols.sw = true, true
	}
	cols.session = min(cols.session, max(minSessionCol+width-used, 6))
	return cols
}

// rowLabel is a session's label without its home, which the home line shows.
func rowLabel(s fleetSession) string {
	if i := strings.Index(s.Label, home.LabelSep); i >= 0 {
		return s.Label[i+len(home.LabelSep):]
	}
	return s.Label
}

// list draws the sessions grouped by home. room is how many lines fit; 0
// means no limit. Past it, the list scrolls to keep the selection in view.
func (c *canvas) list(v view, room int) {
	st := *v.fleet
	if len(st.homes) == 0 {
		c.add(dim(noSession))
		return
	}
	cols := listColumns(c.width, st)
	titles := []seg{plain("  "), dim(pad("SESSION", cols.session)), plain(gap), dim(pad("STATE", cols.state))}
	if cols.model > 0 {
		titles = append(titles, plain(gap), dim(pad("MODEL", cols.model)))
	}
	if cols.turns {
		titles = append(titles, plain(gap), dim(pad("LAST TURNS", sparkCells)))
	}
	if cols.sw {
		titles = append(titles, plain(gap), dim("SWITCH"))
	}
	c.add(titles...)
	if room > 0 {
		room = max(room-1, 1)
	}

	var lines []listLine
	cur := 0
	for i, h := range st.homes {
		if i > 0 {
			lines = append(lines, listLine{})
		}
		lines = append(lines, listLine{segs: homeHeader(st, h)})
		for _, s := range h.sessions {
			if s.ID == st.focus {
				cur = len(lines)
			}
			lines = append(lines, listLine{segs: c.fill(sessionRow(s, h, cols, s.ID == st.focus), s.ID == st.focus), session: s.ID})
		}
		for _, e := range h.endpoints {
			if e.idle {
				lines = append(lines, listLine{segs: waitingRow(e, h, cols)})
			}
		}
	}
	start, end := 0, len(lines)
	if room > 0 && len(lines) > room {
		room = max(room-2, 1)
		start = min(max(cur-room/2, 0), len(lines)-room)
		end = start + room
	}
	if n := countSessions(lines[:start]); n > 0 {
		c.add(dim(fmt.Sprintf("  ↑ %d more", n)))
	}
	for _, l := range lines[start:end] {
		c.add(l.segs...)
		if l.session != "" {
			c.zones = append(c.zones, zone{action: action{kind: actSelect, session: l.session}, row: len(c.out) - 1, x1: c.width})
		}
	}
	if n := countSessions(lines[end:]); n > 0 {
		c.add(dim(fmt.Sprintf("  ↓ %d more", n)))
	}
}

// fill gives the selected row its background across the whole width.
func (c *canvas) fill(segs []seg, selected bool) []seg {
	if !selected {
		return segs
	}
	for i := range segs {
		segs[i].bg = roleSurface
	}
	if rest := c.width - segsWidth(segs); rest > 0 && c.styled {
		segs = append(segs, seg{text: strings.Repeat(" ", rest), bg: roleSurface})
	}
	return segs
}

func countSessions(lines []listLine) int {
	n := 0
	for _, l := range lines {
		if l.session != "" {
			n++
		}
	}
	return n
}

// homeHeader is a home's line: its dot and name, its brain's state, and how
// many of its routers are lost. A lost brain is not counted as lost: it has
// its own words.
func homeHeader(st fleetState, h fleetHome) []seg {
	segs := []seg{col(st.homeRole(h.home.ID), "● "), bold(h.home.Name)}
	lost := 0
	for _, e := range h.endpoints {
		switch {
		case e.kind == registry.KindBrain && e.lost:
			segs = append(segs, dim(sep), col(roleWarn, "brain down: default model"))
		case e.kind == registry.KindBrain && e.up > 0:
			segs = append(segs, dim(sep), col(roleOK, "brain up "+uptime(e.up)))
		case e.kind == registry.KindBrain:
			segs = append(segs, dim(sep), col(roleOK, "brain up"))
		case e.lost:
			lost++
		}
	}
	if lost > 0 {
		segs = append(segs, col(roleWarn, fmt.Sprintf(" · %d lost", lost)))
	}
	return segs
}

// uptime is d in its largest whole unit: 45s, 12m, 2h, 3d.
func uptime(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d/time.Second))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d/time.Minute))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d/time.Hour))
	}
	return fmt.Sprintf("%dd", int(d/(24*time.Hour)))
}

// waitingRow is a router that has answered but has no session yet. A brain
// has no working directory, so it shows as `brain`.
func waitingRow(e fleetEndpointInfo, h fleetHome, cols listCols) []seg {
	return []seg{plain("  "), plain(pad(waitingName(e, h), cols.session)), plain(gap), dim(waitingText)}
}

func waitingName(e fleetEndpointInfo, h fleetHome) string {
	if name := strings.TrimPrefix(e.label, h.home.Name+home.LabelSep); name != "" {
		return name
	}
	return e.kind
}

// sessionRow is one session's columns.
func sessionRow(s fleetSession, h fleetHome, cols listCols, selected bool) []seg {
	marker := plain("  ")
	if selected {
		marker = seg{text: "▸ ", role: roleAccent, bold: true}
	}
	name := plain(pad(rowLabel(s), cols.session))
	name.bold = selected
	text, r := stateOf(s)
	if s.PendingPin {
		text += " · ◇ pin"
	}
	segs := []seg{marker, name, plain(gap), col(r, pad(text, cols.state))}
	if cols.model > 0 {
		segs = append(segs, plain(gap), plain(pad(routeText(s.Route), cols.model)))
	}
	if cols.turns {
		segs = append(segs, plain(gap))
		tail := s.Strip[max(len(s.Strip)-sparkCells, 0):]
		for _, t := range tail {
			segs = append(segs, col(tierRole[t], spark[t]))
		}
		segs = append(segs, plain(strings.Repeat(" ", sparkCells-len(tail))))
	}
	if cols.sw {
		segs = append(segs, plain(gap))
		if sw := s.LastSwitch; sw != nil {
			arrow := "↓"
			if tierRank[sw.To] > tierRank[sw.From] {
				arrow = "↑"
			}
			segs = append(segs, plain(sw.At.Format(timeLayout)+" "), col(tierRole[sw.To], arrow))
		}
	}
	return segs
}

// stateOf is the STATE column: a lost connection first, then the session's
// attention flag, else its mode. Every state has a glyph.
func stateText(s fleetSession) string {
	text, _ := stateOf(s)
	if s.PendingPin {
		text += " · ◇ pin"
	}
	return text
}

func stateOf(s fleetSession) (string, role) {
	switch {
	case s.disconnected:
		return "✕ disconnected", roleWarn
	case s.Attention == route.AttentionPaused, s.Attention == "" && s.Mode == route.ModePaused:
		return "‖ paused", roleMuted
	case s.Attention == route.AttentionManual:
		return "◐ manual", roleWarn
	case s.Attention == route.AttentionEscalation:
		if s.Tier == "" {
			return "↑ escalated", roleHigh
		}
		return "↑ " + string(s.Tier), tierRole[s.Tier]
	case s.Attention == route.AttentionStateLost:
		return "! state lost", roleWarn
	case s.Attention == route.AttentionUnknown:
		return "? unknown", roleMuted
	case s.Attention != "":
		return "? " + string(s.Attention), roleWarn
	}
	return "● on", roleOK
}
