package tui

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/alexei-led/codex-router/internal/config"
	"github.com/alexei-led/codex-router/internal/home"
	"github.com/alexei-led/codex-router/internal/registry"
	"github.com/alexei-led/codex-router/internal/route"
)

const (
	// sessionsWide is the width from which the Sessions tab shows the
	// REPLIES and LAST SWITCH columns.
	sessionsWide  = 100
	gap           = "  "
	stateWidth    = 14 // "✕ disconnected"
	routeWidth    = 20
	switchWidth   = 17 // "18:41 medium→high"
	repliesWidth  = 4  // " 148"
	minSessionCol = 20
	maxSessionCol = 40
)

// homeColors color each home's dot, by the home's place in the list.
var homeColors = []string{colorAccent, "#c792ea", colorOK, "#d7af5f", "#e0708a"}

var attentionMarks = map[route.Attention]string{
	route.AttentionManual:     "◐",
	route.AttentionPaused:     "‖",
	route.AttentionEscalation: "↑",
	route.AttentionUnknown:    "?",
	route.AttentionStateLost:  "!",
}

// attentionMark is the top bar mark for a; an attention this panel does not
// know shows as unknown.
func attentionMark(a route.Attention) string {
	if a == "" {
		return ""
	}
	if m, ok := attentionMarks[a]; ok {
		return m
	}
	return "?"
}

// FleetView is what RenderFleetView draws for the docs images. Each session
// is on its own endpoint, as stage 1 launchers run them, unless its home is
// in Brains: then one brain serves the home, and the value is how long it
// has run.
type FleetView struct {
	Homes    []home.Home
	Sessions []route.SessionInfo
	Brains   map[string]time.Duration
	Focus    string
	Snap     route.Snapshot // the focused session's snapshot
	Tab      Tab
	Width    int
	Locked   bool // a panel run with --session: no top bar, no Sessions tab
}

// RenderFleet draws the all-sessions panel with ANSI colors, top bar
// included, for the docs images. homes and sessions come in display order,
// each session on its own endpoint as stage 1 launchers run them; s is the
// focused session's snapshot. Output depends only on its arguments.
func RenderFleet(homes []home.Home, sessions []route.SessionInfo, focus string, s route.Snapshot, tab Tab, width int) string {
	return RenderFleetView(FleetView{Homes: homes, Sessions: sessions, Focus: focus, Snap: s, Tab: tab, Width: width})
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
		st.homes = append(st.homes, g)
	}
	return render(view{snap: v.Snap, tab: v.Tab, width: v.Width, styled: true, fleet: &st, locked: v.Locked})
}

// all lists every session, home by home, in display order.
func (s fleetState) all() []fleetSession {
	var out []fleetSession
	for _, h := range s.homes {
		out = append(out, h.sessions...)
	}
	return out
}

func (s fleetState) homeColor(id string) string {
	for i, h := range s.homes {
		if h.home.ID == id {
			return homeColors[i%len(homeColors)]
		}
	}
	return colorDim
}

func runes(s string) int { return utf8.RuneCountInString(s) }

// clip cuts s to n runes, ending with … when cut.
func clip(s string, n int) string {
	if runes(s) <= n {
		return s
	}
	if n <= 0 {
		return ""
	}
	return string([]rune(s)[:n-1]) + "…"
}

// pad clips s to n runes and fills it with spaces to n.
func pad(s string, n int) string {
	s = clip(s, n)
	return s + strings.Repeat(" ", max(n-runes(s), 0))
}

func plural(n int, one, many string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, one)
	}
	return fmt.Sprintf("%d %s", n, many)
}

// topBar is `◂ <focused> <others…> ▸ i/n`: the focus first, then the
// sessions after it in list order, cut with … where the width ends.
func (c *canvas) topBar(st fleetState) {
	all := st.all()
	if len(all) == 0 {
		c.add(dim("◂ no sessions ▸"))
		return
	}
	at := 0
	for i, s := range all {
		if s.ID == st.focus {
			at = i
		}
	}
	right := fmt.Sprintf(" ▸ %d/%d", at+1, len(all))
	room := c.width - runes("◂ ") - runes(right)
	segs := []seg{dim("◂ ")}
	for k := range all {
		s := all[(at+k)%len(all)]
		label, mark := s.Label, attentionMark(s.Attention)
		if mark != "" {
			mark = " " + mark
		}
		sep := ""
		if k > 0 {
			sep = gap
		}
		need := runes(sep) + 2 + runes(label) + runes(mark)
		if need > room {
			if k > 0 {
				if room >= 2 {
					segs = append(segs, dim(" …"))
					room -= 2
				}
				break
			}
			label = clip(label, room-2-runes(mark))
			need = 2 + runes(label) + runes(mark)
		}
		room -= need
		name := plain(label)
		switch {
		case k == 0:
			name = seg{text: label, color: colorAccent, bold: true}
		case s.disconnected:
			name = dim(label)
		}
		segs = append(segs, plain(sep), col(st.homeColor(s.Home), "● "), name, col(colorWarn, mark))
	}
	segs = append(segs, plain(strings.Repeat(" ", max(room, 0))), dim(right))
	c.add(segs...)
}

// lockedHeader is the one line a panel locked to a session shows instead of
// the top bar.
func (c *canvas) lockedHeader(st fleetState) {
	s, ok := st.focused()
	if !ok {
		c.add(dim("● "), plain(st.focus))
		return
	}
	c.add(col(st.homeColor(s.Home), "● "), bold(s.Label), dim(gap+s.ID))
}

// sessionLine is one line of the Sessions tab body; session is set on
// session rows.
type sessionLine struct {
	segs    []seg
	session string
}

// sessionCols are the Sessions tab column widths; strip is 0 when the
// REPLIES and LAST SWITCH columns are dropped.
type sessionCols struct {
	session, strip int
}

func columns(width int) sessionCols {
	fixed := runes("▸ ") + stateWidth + routeWidth + 2*runes(gap)
	if width < sessionsWide {
		return sessionCols{session: max(width-fixed, 10)}
	}
	fixed += switchWidth + repliesWidth + 2*runes(gap)
	sess := min(max(width-fixed-route.SessionStrip, minSessionCol), maxSessionCol)
	return sessionCols{session: sess, strip: min(route.SessionStrip, max(width-fixed-sess, 1))}
}

// sessionsTab lists every session grouped by home. room is how many lines
// fit; 0 means no limit. Past it, the list scrolls to keep cursor in view.
func (c *canvas) sessionsTab(st fleetState, cursor string, room int) {
	all := st.all()
	if len(st.homes) == 0 {
		c.add(dim("SESSIONS"), dim(" · none yet"))
		c.add(dim("each codex-router run lists its Codex session here"))
		return
	}
	if cursor == "" {
		cursor = st.focus
	}
	c.add(dim("SESSIONS"), dim(fmt.Sprintf(" · %s in %s", plural(len(all), "session", "sessions"), plural(len(st.homes), "home", "homes"))))
	cols := columns(c.width)
	titles := []seg{plain("  "), dim(pad("SESSION", cols.session)), plain(gap), dim(pad("STATE", stateWidth)), plain(gap)}
	if cols.strip > 0 {
		titles = append(titles, dim(pad("ROUTE", routeWidth)), plain(gap), dim(pad("REPLIES", cols.strip+repliesWidth)), plain(gap), dim("LAST SWITCH"))
	} else {
		titles = append(titles, dim("ROUTE"))
	}
	c.add(titles...)
	if room > 0 {
		room = max(room-2, 1)
	}

	var lines []sessionLine
	cur := 0
	for i, h := range st.homes {
		if i > 0 {
			lines = append(lines, sessionLine{})
		}
		lines = append(lines, sessionLine{segs: homeHeader(st, h)})
		for _, s := range h.sessions {
			if s.ID == cursor {
				cur = len(lines)
			}
			lines = append(lines, sessionLine{segs: sessionRow(s, h, cols, s.ID == cursor, s.ID == st.focus), session: s.ID})
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
	}
	if n := countSessions(lines[end:]); n > 0 {
		c.add(dim(fmt.Sprintf("  ↓ %d more", n)))
	}
}

func countSessions(lines []sessionLine) int {
	n := 0
	for _, l := range lines {
		if l.session != "" {
			n++
		}
	}
	return n
}

// homeHeader is a home's line: its endpoints' state, its brain's state, and
// the sums of its sessions' tallies. A lost brain is not counted as lost: it
// has its own words.
func homeHeader(st fleetState, h fleetHome) []seg {
	segs := []seg{col(st.homeColor(h.home.ID), "● "), bold(h.home.Name), dim(" · " + plural(len(h.endpoints), "endpoint", "endpoints"))}
	lost := 0
	for _, e := range h.endpoints {
		switch {
		case e.kind == registry.KindBrain && e.lost:
			segs = append(segs, dim(" · "), col(colorWarn, "brain down: default model"))
		case e.kind == registry.KindBrain && e.up > 0:
			segs = append(segs, dim(" · "), col(colorOK, "brain up "+uptime(e.up)))
		case e.kind == registry.KindBrain:
			segs = append(segs, dim(" · "), col(colorOK, "brain up"))
		case e.lost:
			lost++
		}
	}
	if lost > 0 {
		segs = append(segs, col(colorWarn, fmt.Sprintf(" · %d lost", lost)))
	}
	replies, switches := 0, 0
	var saved float64
	priced := false
	for _, s := range h.sessions {
		replies += s.Replies
		switches += s.Switches
		if s.SavedUSD != nil {
			saved += *s.SavedUSD
			priced = true
		}
	}
	segs = append(segs, dim(" · "+plural(replies, "reply", "replies")+" · "+plural(switches, "switch", "switches")))
	switch {
	case priced && saved < 0:
		segs = append(segs, dim(" · "), col(colorWarn, fmt.Sprintf("cost ≈ $%.2f", -saved)))
	case priced:
		segs = append(segs, dim(" · "), col(colorOK, fmt.Sprintf("saved ≈ $%.2f", saved)))
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

// sessionRow is one session's columns. The label drops its home prefix,
// which the home line above already shows.
func sessionRow(s fleetSession, h fleetHome, cols sessionCols, cursor, focus bool) []seg {
	marker := plain("  ")
	if cursor {
		marker = col(colorAccent, "▸ ")
	}
	label := plain(pad(strings.TrimPrefix(s.Label, h.home.Name+" · "), cols.session))
	if focus {
		label.bold = true
	}
	segs := []seg{marker, label, plain(gap), stateCell(s), plain(gap)}
	if cols.strip == 0 {
		return append(segs, plain(clip(routeText(s.Route), routeWidth)))
	}
	segs = append(segs, plain(pad(routeText(s.Route), routeWidth)), plain(gap))
	strip := s.Strip[max(len(s.Strip)-cols.strip, 0):]
	for _, t := range strip {
		segs = append(segs, col(tierColor[t], "█"))
	}
	segs = append(segs, plain(strings.Repeat(" ", cols.strip-len(strip))+fmt.Sprintf("%*d", repliesWidth, s.Replies)), plain(gap))
	if sw := s.LastSwitch; sw != nil {
		segs = append(segs, plain(sw.At.Format(timeLayout)+" "), col(tierColor[sw.From], string(sw.From)), plain("→"), col(tierColor[sw.To], string(sw.To)))
	} else {
		segs = append(segs, dim("-"))
	}
	return segs
}

// stateCell is the STATE column: a lost connection first, then the
// session's attention flag, else its mode.
func stateCell(s fleetSession) seg {
	text, color := "◉ "+string(s.Mode), colorOK
	switch {
	case s.disconnected:
		text, color = "✕ disconnected", colorWarn
	case s.Attention == route.AttentionPaused:
		text, color = "‖ paused", colorDim
	case s.Attention == route.AttentionManual:
		text, color = "◐ manual", colorWarn
	case s.Attention == route.AttentionEscalation:
		text, color = "↑ escalation", tierColor[config.TierHigh]
	case s.Attention == route.AttentionStateLost:
		text, color = "! state lost", colorWarn
	case s.Attention == route.AttentionUnknown:
		text, color = "? unknown", colorDim
	case s.Attention != "":
		text, color = "? "+string(s.Attention), colorWarn
	case s.Mode == "":
		text = "◉ " + string(route.ModeOn)
	}
	return col(color, pad(text, stateWidth))
}
