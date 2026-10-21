package tui

import (
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"

	"charm.land/bubbles/v2/key"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/alexei-led/codex-router/internal/config"
	"github.com/alexei-led/codex-router/internal/policy"
	"github.com/alexei-led/codex-router/internal/route"
)

const (
	minWidth = 40
	// wideWidth is the width from which the list and the detail sit side by
	// side.
	wideWidth = 120
	// helpWidth is the width from which the header carries its `? help`.
	helpWidth = 64
	indent    = "  "
	// labelWidth is the label column of the detail block and the Policy
	// and Usage views, with its one-cell margin.
	labelWidth = 10
	timeLayout = "15:04"
	sep        = " · "
)

var tabNames = map[Tab]string{TabSessions: "Sessions", TabPolicy: "Policy", TabUsage: "Usage"}

// tierOrder lists the tiers low to high: popover numbers, buttons, sparklines.
var tierOrder = []config.Tier{config.TierMicro, config.TierLow, config.TierMedium, config.TierHigh}

// tierRank orders tiers; a switch to a higher rank is an upgrade.
var tierRank = map[config.Tier]int{config.TierMicro: 0, config.TierLow: 1, config.TierMedium: 2, config.TierHigh: 3}

type seg struct {
	text    string
	role    role
	bg      role
	bold    bool
	reverse bool
	act     action // a click on the text does this
}

func plain(t string) seg            { return seg{text: t} }
func dim(t string) seg              { return seg{text: t, role: roleMuted} }
func bold(t string) seg             { return seg{text: t, bold: true} }
func col(r role, t string) seg      { return seg{text: t, role: r} }
func clickable(s seg, a action) seg { s.act = a; return s }

// pane says which half of the side-by-side Sessions view has the focus.
type pane int

const (
	paneList pane = iota
	paneDetail
)

// popover says which choice popover is open.
type popover int

const (
	popNone popover = iota
	popPin
	popPreset
)

// view is everything render needs; render is pure over it. Fleet is nil
// only in tests that render a bare snapshot: no list, no session title. Height
// 0 means unknown: the list does not scroll.
type view struct {
	snap         route.Snapshot
	tab          Tab
	width        int
	height       int
	disconnected bool
	notice       string
	styled       bool
	light        bool // a light terminal background; dark until known
	fleet        *fleetState
	locked       bool
	loading      bool // the focused session's snapshot has not arrived
	zoom         bool // stacked: the detail alone, without the list
	pane         pane
	help         bool
	popover      popover
	toast        string
	scroll       int // top row of the focused pane's viewport
}

// multi reports whether the panel shows every session.
func (v view) multi() bool { return v.fleet != nil && !v.locked }

func (v view) wide() bool { return v.multi() && v.width >= wideWidth }

// listed reports whether the Sessions view draws the list.
func (v view) listed() bool {
	return v.multi() && v.tab == TabSessions && (v.wide() || !v.zoom)
}

type canvas struct {
	styled  bool
	th      theme
	width   int
	out     []string
	widths  []int
	zones   []zone
	anchors map[string]anchor
	layout  layoutInfo
}

// anchor is a drawn row an overlay hangs from.
type anchor struct{ row, x int }

func newCanvas(v view) *canvas {
	return &canvas{styled: v.styled, th: newTheme(!v.light), width: max(v.width, minWidth), anchors: map[string]anchor{}}
}

// child is an empty canvas of the same look, w wide.
func (c *canvas) child(w int) *canvas {
	return &canvas{styled: c.styled, th: c.th, width: w, anchors: map[string]anchor{}}
}

func render(v view) string {
	out, _ := layout(v)
	return out
}

// layout draws v and lists the clickable zones of the drawn rows.
func layout(v view) (string, []zone) {
	out, zones, _ := layoutWithInfo(v)
	return out, zones
}

type layoutInfo struct {
	detailRow int
	detailX   int
	hasDetail bool
	scrollMax int // the largest useful scroll for the focused pane
}

func layoutWithInfo(v view) (string, []zone, layoutInfo) {
	c := newCanvas(v)
	foot := c.footerLines(v)
	c.header(v)
	c.blank()
	bodyRows, limited := 0, v.height > 0
	if limited {
		reserve := 1 + len(foot)
		if v.notice != "" {
			reserve++
		}
		bodyRows = max(v.height-len(c.out)-reserve, 0)
	}
	switch {
	case v.fleet != nil && !v.fleet.ended && v.fleet.focus == "" && v.tab != TabSessions:
		c.add(dim("no Codex session yet · run codex-router in a project to start one"))
	case v.tab == TabPolicy:
		c.screen(bodyRows, limited, v.scroll, (*canvas).policy, v)
	case v.tab == TabUsage:
		c.screen(bodyRows, limited, v.scroll, (*canvas).usage, v)
	default:
		c.sessions(v, bodyRows, limited)
	}
	c.blank()
	footerRow := len(c.out)
	for _, l := range foot {
		c.add(l...)
	}
	if v.notice != "" {
		c.add(col(roleWarn, v.notice))
	}
	out, zones := c.compose(v, footerRow)
	return out, zones, c.layout
}

// screen draws one of the non-Sessions screens into the height budget and
// scrolls its body without moving the header or footer.
func (c *canvas) screen(rows int, limited bool, scroll int, draw func(*canvas, view), v view) {
	body := c.child(c.width)
	draw(body, v)
	if limited {
		c.layout.scrollMax = max(len(body.out)-max(rows, 0), 0)
		body = crop(body, rows, scroll)
	}
	c.append(body)
}

// append joins a child's visible rows, hitboxes and anchors to this canvas.
func (c *canvas) append(d *canvas) {
	base := len(c.out)
	for _, z := range d.zones {
		z.row += base
		c.zones = append(c.zones, z)
	}
	for k, a := range d.anchors {
		a.row += base
		c.anchors[k] = a
	}
	c.out = append(c.out, d.out...)
	c.widths = append(c.widths, d.widths...)
}

// crop returns the rows at scroll in d, keeping only visible hitboxes and
// anchors and moving them into viewport coordinates.
func crop(d *canvas, rows, scroll int) *canvas {
	if rows <= 0 {
		return d.child(d.width)
	}
	start := min(max(scroll, 0), max(len(d.out)-rows, 0))
	end := min(start+rows, len(d.out))
	out := &canvas{styled: d.styled, th: d.th, width: d.width, anchors: map[string]anchor{}}
	out.out = append(out.out, d.out[start:end]...)
	out.widths = append(out.widths, d.widths[start:end]...)
	for _, z := range d.zones {
		if z.row >= start && z.row < end {
			z.row -= start
			out.zones = append(out.zones, z)
		}
	}
	for k, a := range d.anchors {
		if a.row >= start && a.row < end {
			a.row -= start
			out.anchors[k] = a
		}
	}
	return out
}

func (c *canvas) paint(s seg) string {
	if !c.styled || (s.role == roleNone && s.bg == roleNone && !s.bold && !s.reverse) {
		return s.text
	}
	st := lipgloss.NewStyle().Bold(s.bold).Reverse(s.reverse)
	if s.role != roleNone {
		st = st.Foreground(c.th[s.role])
	}
	if s.bg != roleNone {
		st = st.Background(c.th[s.bg])
	}
	return st.Render(s.text)
}

// zone adds z, joined to the zone before it when that one has the same action
// and ends where z starts: a key and its text are one button.
func (c *canvas) zone(z zone) {
	if n := len(c.zones); n > 0 {
		if last := &c.zones[n-1]; last.action == z.action && last.row == z.row && last.x1 == z.x0 {
			last.x1 = z.x1
			return
		}
	}
	c.zones = append(c.zones, z)
}

func segsWidth(segs []seg) int {
	n := 0
	for _, s := range segs {
		n += ansi.StringWidth(s.text)
	}
	return n
}

// add appends one line, cutting it to the canvas width on plain text.
func (c *canvas) add(segs ...seg) {
	room := c.width
	var b strings.Builder
	for _, s := range segs {
		n := ansi.StringWidth(s.text)
		cut := n > room
		if cut {
			if room == 0 {
				break
			}
			s.text = ansi.Truncate(s.text, room, "…")
			n = ansi.StringWidth(s.text)
		}
		if s.act.kind != actNone && n > 0 {
			x0 := c.width - room
			c.zone(zone{action: s.act, row: len(c.out), x0: x0, x1: x0 + n})
		}
		room -= n
		b.WriteString(c.paint(s))
		if cut {
			break
		}
	}
	line, w := b.String(), c.width-room
	if !c.styled {
		line = strings.TrimRight(line, " ")
		w = ansi.StringWidth(line)
	}
	c.out = append(c.out, line)
	c.widths = append(c.widths, w)
}

func (c *canvas) blank() {
	c.out = append(c.out, "")
	c.widths = append(c.widths, 0)
}

// split adds left and right on one line, right against the edge; right is
// dropped when the two do not fit.
func (c *canvas) split(left, right []seg) {
	gap := c.width - segsWidth(left) - segsWidth(right)
	if len(right) == 0 || gap < 1 {
		c.add(left...)
		return
	}
	c.add(append(append(slices.Clone(left), plain(strings.Repeat(" ", gap))), right...)...)
}

// label is a detail-block label cell.
func label(name string) seg { return plain(fmt.Sprintf(" %-*s", labelWidth-1, name)) }

// flow adds parts joined by " · " under lead, breaking before a part that
// does not fit; later lines hang under the lead.
func (c *canvas) flow(lead seg, parts ...[]seg) {
	c.flowParts(lead, parts, false)
}

// flowWrap is flow for evidence the panel does not size: the engine's support
// and the classifier's reason can be longer than a line, so a part that does not
// fit on a line of its own wraps at its own spaces onto the hanging lines
// instead of being cut at the edge. A token wider than the line is cut cell-wise,
// as wrapped cuts one, and joining the lines leaves every cell of the part.
func (c *canvas) flowWrap(lead seg, parts ...[]seg) {
	c.flowParts(lead, parts, true)
}

// flowParts draws parts joined by sep under lead. With wrap set, a part too wide
// for the line it starts on continues on the lines after it, each hanging under
// the lead.
func (c *canvas) flowParts(lead seg, parts [][]seg, wrap bool) {
	hang := plain(strings.Repeat(" ", ansi.StringWidth(lead.text)))
	line := []seg{lead}
	room := c.width - segsWidth(line)
	first := true
	for _, p := range parts {
		w := segsWidth(p)
		if !first && room < ansi.StringWidth(sep)+w {
			c.add(line...)
			line, room, first = []seg{hang}, c.width-segsWidth([]seg{hang}), true
		}
		if !first {
			line = append(line, dim(sep))
			room -= ansi.StringWidth(sep)
		}
		if wrap {
			for room > 0 && segsWidth(p) > room {
				head, tail := cutPart(p, room)
				if len(head) == 0 || len(tail) == 0 {
					break
				}
				c.add(append(line, head...)...)
				line, room, p = []seg{hang}, c.width-segsWidth([]seg{hang}), tail
			}
		}
		line = append(line, p...)
		room -= segsWidth(p)
		first = false
	}
	c.add(line...)
}

// cutPart splits p for a line of room cells: head is what fits, ending at a
// space when p holds one past its first cell, and tail is the rest without that
// space, as a wrap replaces the space it broke on. A part with no such space in
// room — a long token, or one whose spaces only start at column zero, where a
// break would leave a blank head and the wrap no progress — is cut cell-wise,
// which never splits a grapheme or wide cell.
func cutPart(p []seg, room int) (head, tail []seg) {
	w := segsWidth(p)
	if col, run, ok := lastSpace(p, room); ok && col > 0 {
		return slicePart(p, 0, col), slicePart(p, col+run, w)
	}
	return slicePart(p, 0, room), slicePart(p, room, w)
}

// lastSpace is the column of the last space in p that starts at or before room,
// and the cells that space runs for: where a wrap breaks when the head it leaves
// holds text.
func lastSpace(p []seg, room int) (col, run int, ok bool) {
	start := 0
	for _, s := range p {
		for i := 0; i < len(s.text); i++ {
			if s.text[i] != ' ' {
				continue
			}
			c := start + ansi.StringWidth(s.text[:i])
			if c > room {
				return col, run, ok
			}
			n := len(s.text[i:]) - len(strings.TrimLeft(s.text[i:], " "))
			col, run, ok = c, n, true
			i += n - 1
		}
		start += ansi.StringWidth(s.text)
	}
	return col, run, ok
}

// slicePart is p's cells from to, keeping each run's style, so the pieces of a
// split part draw as the part did.
func slicePart(p []seg, from, to int) []seg {
	var out []seg
	start := 0
	for _, s := range p {
		end := start + ansi.StringWidth(s.text)
		switch {
		case end <= from, start >= to: // the run is outside the slice
		case start >= from && end <= to:
			out = append(out, s)
		default:
			if text := ansi.Cut(s.text, max(from-start, 0), min(to-start, end-start)); text != "" {
				s.text = text
				out = append(out, s)
			}
		}
		start = end
	}
	return out
}

// wrapped adds text as lines under lead; later lines hang under it.
func (c *canvas) wrapped(lead seg, text string, style seg) {
	w := ansi.StringWidth(lead.text)
	room := max(c.width-w, 1)
	first := true
	line := ""
	flush := func() {
		if line == "" {
			return
		}
		style.text = line
		if first {
			c.add(lead, style)
		} else {
			c.add(plain(strings.Repeat(" ", w)), style)
		}
		first, line = false, ""
	}
	for _, word := range strings.Fields(text) {
		wordWidth := ansi.StringWidth(word)
		if wordWidth > room {
			flush()
			for _, part := range strings.Split(ansi.Hardwrap(word, room, false), "\n") {
				line = part
				flush()
			}
			continue
		}
		if line != "" && ansi.StringWidth(line)+1+wordWidth > room {
			flush()
		}
		if line != "" {
			line += " "
		}
		line += word
	}
	flush()
}

// header is the top line: the name, totals and state on the left, the views and
// `? help` on the right.
func (c *canvas) header(v view) {
	var right []seg
	for _, t := range Tabs {
		s := plain(" " + tabNames[t] + " ")
		if t == v.tab {
			s = seg{text: s.text, role: roleAccent, bold: true, reverse: true}
		}
		right = append(right, clickable(s, action{kind: actTab, tab: t}))
	}
	if c.width >= helpWidth {
		right = append(right, plain("  "), clickable(dim("? help"), action{kind: actKey, key: handle(keys.Help)}))
	}
	room := c.width - segsWidth(right) - 1
	status := v.status()
	var left []seg
	for _, totals := range v.totals() {
		cand := []seg{bold("codex-router"), plain("  ")}
		for _, group := range append(totals, status) {
			if len(cand) > 2 {
				cand = append(cand, dim(sep))
			}
			cand = append(cand, group...)
		}
		if segsWidth(cand) <= room {
			left = cand
			break
		}
	}
	if left == nil {
		left = []seg{bold("codex-router")}
	}
	c.split(left, right)
}

// totals lists header candidates from the fullest to the leanest.
func (v view) totals() [][][]seg {
	if !v.multi() {
		return [][][]seg{nil}
	}
	n, h := len(v.fleet.all()), len(v.fleet.homes)
	if n == 0 {
		return [][][]seg{nil}
	}
	return [][][]seg{
		{{plain(plural(n, "session", "sessions"))}, {plain(plural(h, "home", "homes"))}},
		{{plain(plural(n, "session", "sessions"))}},
		nil,
	}
}

// status is the header's state: the session's router, else the focused home's
// classifier.
func (v view) status() []seg {
	s := v.snap
	switch {
	case v.fleet != nil && v.fleet.ended:
		return []seg{col(roleWarn, "■ ended"), dim(" · closing")}
	case v.fleet != nil && v.fleet.focus == "":
		return []seg{dim("○ no sessions")}
	case v.disconnected:
		return []seg{col(roleWarn, "✕ disconnected")}
	case s.ClassifierOff:
		return []seg{col(roleWarn, "○ "+classifierName(s)+" off")}
	case classifierPaused(s):
		secs := int(math.Ceil(s.Classifier.PausedUntil.Sub(s.At).Seconds()))
		return []seg{col(roleErr, "✕ "+classifierName(s)+" unreachable"), dim(fmt.Sprintf(" · resumes in %ds", secs))}
	}
	return []seg{col(roleOK, classifierName(s)+" ready")}
}

func classifierName(s route.Snapshot) string {
	if s.ClassifierLabel == "" {
		return "classifier"
	}
	return s.ClassifierLabel
}

func classifierPaused(s route.Snapshot) bool { return s.Classifier.PausedUntil.After(s.At) }

// hint is one footer entry: the key in the accent, its text muted; a click
// on it runs the binding.
func hint(b key.Binding) []seg {
	h := b.Help()
	a := action{kind: actKey, key: handle(b)}
	return []seg{clickable(seg{text: h.Key, role: roleAccent, bold: true}, a), clickable(dim(" "+h.Desc), a)}
}

// footerBindings lists the keys that work now, most useful first, ending with
// help. Disconnected, only the keys that move around; with no session, none.
func footerBindings(v view) []key.Binding {
	st := v.fleet
	switch {
	case v.help:
		return []key.Binding{described(keys.Back, "close")}
	case v.popover == popPin:
		return []key.Binding{keys.PinPick, described(keys.Back, "close")}
	case v.popover == popPreset:
		return []key.Binding{keys.PresetPick, described(keys.Back, "close")}
	}
	if st != nil && (st.ended || st.focus == "") {
		return []key.Binding{keys.Quit, keys.Help}
	}
	var out []key.Binding
	scroll := v.tab != TabSessions || v.zoom || v.locked || v.fleet == nil || v.wide() && v.pane == paneDetail
	if scroll {
		out = append(out, described(keys.Up, "scroll"))
	} else if v.listed() && len(st.all()) > 1 {
		out = append(out, described(keys.Up, "select"))
	}
	if !v.disconnected {
		toggle := "pause"
		if toggleCommand("", v.snap.Mode).Op == route.OpResume {
			toggle = "resume"
		}
		home := "pause home"
		if st != nil && st.homeResumes() {
			home = "resume home"
		}
		if v.tab == TabSessions {
			out = append(out, keys.Pin)
		}
		if v.tab == TabPolicy && !v.snap.ClassifierOff {
			out = append(out, keys.Preset)
		}
		out = append(out, described(keys.Toggle, toggle), described(keys.Home, home))
	}
	return append(out, keys.Next, keys.Quit, keys.Help)
}

// footerLines packs the footer entries into lines of the canvas width.
func (c *canvas) footerLines(v view) [][]seg {
	var items [][]seg
	for _, b := range footerBindings(v) {
		items = append(items, hint(b))
	}
	var out [][]seg
	var line []seg
	for _, it := range items {
		switch {
		case line == nil:
			line = it
		case segsWidth(line)+ansi.StringWidth(sep)+segsWidth(it) <= c.width:
			line = append(append(line, dim(sep)), it...)
		default:
			out = append(out, line)
			line = it
		}
	}
	return append(out, line)
}

func routeText(r config.Route) string {
	if r.Model == "" {
		return "-"
	}
	if r.Effort == "" {
		return r.Model
	}
	return r.Model + " · " + string(r.Effort)
}

// keptReasons are the decisions that leave Codex's own model in place. Their
// tier is the engine's guess for that model, so the panel does not show it.
var keptReasons = []policy.Reason{policy.ReasonError, policy.ReasonNoClassifier, policy.ReasonPassthrough, policy.ReasonOutOfGroup}

// reasonText says what a reason code means, for decisions that carry no why.
var reasonText = map[policy.Reason]string{
	policy.ReasonEscalation:       "escalated after a repeated failure",
	policy.ReasonHold:             "holding the escalated tier",
	policy.ReasonNoAdvice:         "no classifier answer; kept the tier",
	policy.ReasonUncertain:        "classifier unsure; kept the tier",
	policy.ReasonSameTier:         "classifier agrees with the tier",
	policy.ReasonUpgrade:          "moved up a tier",
	policy.ReasonJump:             "moved up a tier",
	policy.ReasonUpgradePending:   "waiting to go up: needs more support",
	policy.ReasonDowngrade:        "moved down a tier",
	policy.ReasonDowngradeHeld:    "waiting to go down: no passed check yet",
	policy.ReasonDowngradePending: "waiting to go down: needs more support",
	policy.ReasonPinned:           "pinned for this turn",
	policy.ReasonPaused:           "routing paused",
	policy.ReasonManual:           "model picked with /model",
	policy.ReasonFit:              "context needs a larger window",
	policy.ReasonOutOfGroup:       "route outside the compatibility group; request kept",
	policy.ReasonPassthrough:      "request kept",
	policy.ReasonError:            "classifier failed; request kept",
	policy.ReasonNoClassifier:     "classifier off; request keeps Codex's model",
}

// whyOf is d's reason in words: the engine's text, else a sentence for its
// code, else a plain fallback for a code this panel does not know.
func whyOf(d route.Decision) string {
	if d.Why != "" {
		return d.Why
	}
	if t, ok := reasonText[d.Reason]; ok {
		return t
	}
	return "reason not shown by this panel version"
}

func percent(p float64) string { return fmt.Sprintf("%d%%", int(math.Round(p*100))) }

// savedSegs is the estimate against each reply's native model as a sentence.
func pricingComplete(st route.Stats) bool {
	total := st.EligibleReplies
	if total == 0 {
		total = st.Replies
	}
	return total > 0 && st.PricedReplies == total
}

func pricingCoverage(st route.Stats) []seg {
	total := st.EligibleReplies
	if total == 0 {
		total = st.Replies
	}
	word := "partial"
	if st.PricedReplies == 0 {
		word = "unknown"
	}
	return []seg{dim(fmt.Sprintf("estimate %s · %d of %d replies priced", word, st.PricedReplies, total))}
}

func savedSegs(st route.Stats) []seg {
	if !pricingComplete(st) {
		return pricingCoverage(st)
	}
	prefix := "Routing "
	if st.BaselineUSD == st.RoutedUSD {
		prefix = "Routing: "
	}
	return append([]seg{plain(prefix)}, savedPart(st)...)
}

// dollars preserves sub-cent differences so a cost increase never reads as zero.
func dollars(v float64) string {
	if v > 0 && v < .0001 {
		return "< $0.0001"
	}
	if v > 0 && v < .01 {
		return fmt.Sprintf("$%.4f", v)
	}
	return fmt.Sprintf("$%.2f", v)
}

type activityCount struct {
	name config.Activity
	n    int
}

func topActivities(m map[config.Activity]int, limit int) []activityCount {
	out := make([]activityCount, 0, len(m))
	for k, n := range m {
		if n > 0 {
			out = append(out, activityCount{k, n})
		}
	}
	slices.SortFunc(out, func(a, b activityCount) int {
		if a.n != b.n {
			return b.n - a.n
		}
		return strings.Compare(string(a.name), string(b.name))
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

// bar is pct as a filled bar of cells cells.
func bar(pct, cells int) string {
	n := min(max(pct, 0), 100) * cells / 100
	return strings.Repeat("█", n) + strings.Repeat("░", cells-n)
}

func fmtTokens(n int) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1e6)
	case n >= 1000 && n%1000 == 0:
		return fmt.Sprintf("%dK", n/1000)
	case n >= 1000:
		return fmt.Sprintf("%.1fK", float64(n)/1000)
	}
	return strconv.Itoa(n)
}

func abs(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}
