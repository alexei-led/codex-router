package tui

import (
	"fmt"
	"math"
	"slices"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"charm.land/lipgloss/v2"

	"github.com/alexei-led/codex-router/internal/config"
	"github.com/alexei-led/codex-router/internal/route"
)

const (
	minWidth   = 40
	labelWidth = 11
	indent     = "  "
	barCells   = 20
	recentRows = 6
	timeLayout = "15:04"
)

var tierOrder = []config.Tier{config.TierHigh, config.TierMedium, config.TierLow, config.TierMicro}

var tierColor = map[config.Tier]string{
	config.TierMicro:  "#5fd7af",
	config.TierLow:    "#5f9fe0",
	config.TierMedium: "#d7af5f",
	config.TierHigh:   "#e0708a",
}

const (
	colorAccent   = "#7c9cff"
	colorActivity = "#d77757"
	colorOK       = "#5fd7af"
	colorWarn     = "#f0b429"
	colorDim      = "#7d8597"
)

type seg struct {
	text    string
	color   string
	bold    bool
	reverse bool
	act     action // a click on the text does this
}

func plain(t string) seg        { return seg{text: t} }
func dim(t string) seg          { return seg{text: t, color: colorDim} }
func bold(t string) seg         { return seg{text: t, bold: true} }
func col(c, t string) seg       { return seg{text: t, color: c} }
func tierSeg(t config.Tier) seg { return seg{text: "▌" + string(t), color: tierColor[t]} }
func button(label string, on bool) seg {
	if on {
		return seg{text: "[" + label + "]", color: colorAccent, reverse: true}
	}
	return seg{text: " " + label + " ", color: colorAccent}
}

// view is everything render needs; render is pure over it. Fleet is nil
// only in tests that render a bare snapshot: no top bar, no session line, no
// Sessions tab. Height 0 means unknown: the Sessions tab does not scroll.
type view struct {
	snap         route.Snapshot
	tab          Tab
	width        int
	height       int
	disconnected bool
	notice       string
	styled       bool
	fleet        *fleetState
	locked       bool
	cursor       string // Sessions tab selection, "" for the focus
}

type canvas struct {
	styled bool
	width  int
	out    []string
	zones  []zone
}

func render(v view) string {
	out, _ := layout(v)
	return out
}

// layout draws v and lists the clickable zones of the drawn rows.
func layout(v view) (string, []zone) {
	c := &canvas{styled: v.styled, width: max(v.width, minWidth)}
	all := v.fleet != nil && !v.locked
	switch {
	case all:
		c.topBar(*v.fleet)
	case v.fleet != nil:
		c.lockedHeader(*v.fleet)
	}
	c.header(v)
	c.tabs(v.tab, all)
	hints := packHints(keyHints(v), c.width)
	footer := 1 + len(hints)
	if v.notice != "" {
		footer++
	}
	switch {
	case v.tab == TabSessions && all:
		room := 0
		if v.height > 0 {
			room = max(v.height-len(c.out)-footer, 1)
		}
		c.sessionsTab(*v.fleet, v.cursor, room)
	case v.fleet != nil && v.fleet.focus == "":
		c.add(dim("no Codex session yet · run codex-router in a project to start one"))
	case v.tab == TabRoutes:
		c.routes(v.snap)
	case v.tab == TabPolicy:
		c.policy(v.snap)
	case v.tab == TabClassifier:
		c.classifier(v.snap)
	case v.tab == TabUsage:
		c.usage(v.snap)
	default:
		c.now(v.snap)
	}
	c.blank()
	for _, h := range hints {
		c.add(dim(h))
	}
	if v.notice != "" {
		c.add(col(colorWarn, v.notice))
	}
	return strings.Join(c.out, "\n") + "\n", c.zones
}

// keyHints lists the keys the view takes, most useful first.
func keyHints(v view) []string {
	var h []string
	switch {
	case v.fleet == nil:
		h = []string{hint(keys.TabNum, ""), hint(keys.Next, ""), hint(keys.Pin, "pin next"), hint(keys.Toggle, "pause/resume")}
	case v.locked:
		h = []string{hint(keys.TabNum, ""), hint(keys.Pin, "pin next"), hint(keys.Toggle, ""), hint(keys.Home, "")}
	case v.tab == TabSessions:
		h = []string{hint(keys.Up, ""), hint(keys.Enter, ""), hint(keys.Session, ""), hint(keys.TabNum, ""), hint(keys.Toggle, ""), hint(keys.Home, "")}
	default:
		h = []string{hint(keys.Session, ""), hint(keys.List, ""), hint(keys.TabNum, ""), hint(keys.Pin, ""), hint(keys.Toggle, ""), hint(keys.Home, "")}
	}
	h = append(h, hint(keys.Quit, ""))
	if v.tab == TabPolicy {
		h = append(h, hint(keys.Preset, ""))
	}
	return h
}

// packHints joins hints with " · " into as few lines of width as fit.
func packHints(hints []string, width int) []string {
	var out []string
	line := ""
	for _, h := range hints {
		switch {
		case line == "":
			line = h
		case runes(line)+runes(" · ")+runes(h) <= width:
			line += " · " + h
		default:
			out = append(out, line)
			line = h
		}
	}
	if line != "" {
		out = append(out, line)
	}
	return out
}

func (c *canvas) paint(s seg) string {
	if !c.styled || (s.color == "" && !s.bold && !s.reverse) {
		return s.text
	}
	st := lipgloss.NewStyle().Bold(s.bold).Reverse(s.reverse)
	if s.color != "" {
		st = st.Foreground(lipgloss.Color(s.color))
	}
	return st.Render(s.text)
}

// add appends one line, cutting it to the canvas width on plain text.
func (c *canvas) add(segs ...seg) {
	room := c.width
	var b strings.Builder
	for _, s := range segs {
		n := utf8.RuneCountInString(s.text)
		cut := n > room
		if cut {
			if room == 0 {
				break
			}
			s.text = string([]rune(s.text)[:max(room-1, 0)]) + "…"
			n = utf8.RuneCountInString(s.text)
		}
		if s.act.kind != actNone && n > 0 {
			x0 := c.width - room
			c.zones = append(c.zones, zone{action: s.act, row: len(c.out), x0: x0, x1: x0 + n})
		}
		room -= n
		b.WriteString(c.paint(s))
		if cut {
			break
		}
	}
	c.out = append(c.out, b.String())
}

func (c *canvas) blank() { c.out = append(c.out, "") }

func (c *canvas) section(title string, rest ...seg) {
	c.blank()
	c.add(append([]seg{dim(title)}, rest...)...)
}

func (c *canvas) row(label string, segs ...seg) {
	c.add(append([]seg{plain(fmt.Sprintf("%-*s", labelWidth, label))}, segs...)...)
}

// wrapped adds text as lines hanging under a label column.
func (c *canvas) wrapped(lead string, text string, style seg) {
	room := max(c.width-utf8.RuneCountInString(lead), 10)
	var line string
	flush := func() {
		if line != "" {
			t := style
			t.text = line
			c.add(plain(lead), t)
			line = ""
		}
	}
	for _, w := range strings.Fields(text) {
		if line != "" && utf8.RuneCountInString(line)+1+utf8.RuneCountInString(w) > room {
			flush()
		}
		if line != "" {
			line += " "
		}
		line += w
	}
	flush()
}

func (c *canvas) header(v view) {
	s := v.snap
	segs := []seg{bold("ROUTING"), plain(" ")}
	switch {
	case v.fleet != nil && v.fleet.ended:
		c.add(append(segs, col(colorWarn, "■ ended"), dim(" · the session's router exited · closing"))...)
		return
	case v.fleet != nil && v.fleet.focus == "":
		c.add(append(segs, dim("○ no sessions"), dim(" · watching the registry"))...)
		return
	}
	if v.disconnected {
		segs = append(segs, col(colorWarn, "✕ disconnected"), dim(" · retrying"))
		if !s.At.IsZero() {
			segs = append(segs, dim(" · last snapshot "+s.At.Format("15:04:05")))
		}
		c.add(segs...)
		return
	}
	switch {
	case s.StateLost:
		segs = append(segs, col(colorWarn, "! state lost"), dim(" · paused"))
	case s.Mode == route.ModeManual:
		segs = append(segs, col(colorWarn, "◐ manual"))
	case s.Mode == route.ModePaused:
		segs = append(segs, col(colorDim, "○ paused"))
	default:
		segs = append(segs, col(colorOK, "◉ on"))
	}
	segs = append(segs, dim(" · "+classifierStatus(s)))
	if s.FooterSynced {
		segs = append(segs, dim(" · footer synced"))
	} else {
		segs = append(segs, col(colorWarn, " · footer not synced"))
	}
	c.add(segs...)
}

// tabs draws the tab row; withSessions adds the Sessions tab 0 in front.
func (c *canvas) tabs(active Tab, withSessions bool) {
	names := map[Tab]string{TabSessions: "Sessions", TabNow: "Now", TabRoutes: "Routes", TabPolicy: "Policy", TabClassifier: "Classifier", TabUsage: "Usage"}
	var segs []seg
	for i, t := range tabsFor(withSessions) {
		if !withSessions {
			i++
		}
		label := fmt.Sprintf("%s %d", names[t], i)
		tab := dim(" " + label + " ")
		if t == active || (active == "" && t == TabNow) {
			tab = seg{text: "[" + label + "]", color: colorAccent, reverse: true}
		}
		tab.act = action{kind: actTab, tab: t}
		segs = append(segs, tab)
	}
	c.blank()
	c.add(segs...)
	c.blank()
}

func classifierName(s route.Snapshot) string {
	if s.ClassifierLabel == "" {
		return "classifier"
	}
	return s.ClassifierLabel
}

func classifierPaused(s route.Snapshot) bool { return s.Classifier.PausedUntil.After(s.At) }

func classifierStatus(s route.Snapshot) string {
	h := s.Classifier
	switch {
	case s.ClassifierOff:
		return classifierName(s) + " off"
	case classifierPaused(s):
		return fmt.Sprintf("%s paused until %s", classifierName(s), h.PausedUntil.Format("15:04:05"))
	case h.Latency > 0:
		return fmt.Sprintf("%s ready · %d ms", classifierName(s), h.Latency.Milliseconds())
	default:
		return classifierName(s) + " ready"
	}
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

func (c *canvas) now(s route.Snapshot) {
	c.nextTurn(s)
	if d := s.Last; d != nil {
		c.row("Activity", activitySegs(d, s.Policy.ActivityMass)...)
		why := d.Why
		if why == "" {
			why = string(d.Reason)
		}
		c.wrapped(fmt.Sprintf("%-*s", labelWidth, "Why"), why, plain(""))
		if d.Support != "" {
			c.wrapped(strings.Repeat(" ", labelWidth), d.Support, dim(""))
		}
	}
	c.pinRow(s)
	c.session(s)
	c.worth(s.Worth)
	c.blank()
	sync := "synced via thread/settings/update"
	if !s.FooterSynced {
		sync = "not synced yet"
	}
	c.row("Codex", plain("footer shows "), bold(routeText(s.Footer)), dim(" · "+sync))
	c.contextRow(s)
	c.cacheRow(s)
}

func (c *canvas) nextTurn(s route.Snapshot) {
	switch {
	case s.Mode == route.ModeManual:
		c.row("Next turn", col(colorWarn, routeText(s.Footer)), dim(" picked with /model · routing holds it · space to resume"))
	case s.Mode == route.ModePaused:
		c.row("Next turn", dim("paused · every request goes to "+routeText(s.Baseline)+" unchanged"))
	case s.Pin != nil:
		c.row("Next turn", tierSeg(*s.Pin), plain("  "), bold(routeText(s.Routes[*s.Pin])), dim("  pinned"))
	case s.Last != nil:
		d := s.Last
		label := "Next turn"
		if d.MidTurn {
			label = "This turn"
		}
		segs := []seg{tierSeg(d.Tier), plain("  "), bold(routeText(d.Route))}
		if d.Rewritten {
			segs = append(segs, dim("  base "+routeText(d.Original)))
		}
		c.row(label, segs...)
	default:
		c.row("Next turn", dim("no routed request yet · baseline "+routeText(s.Baseline)))
	}
}

// activitySegs is the activity row: the classifier's activity answer with its
// probability and the likeliest alternates, else the decision's activity.
func activitySegs(d *route.Decision, applyAt float64) []seg {
	r := d.Reading
	if r == nil || r.Activity == "" {
		if d.Activity == "" {
			return []seg{dim("none")}
		}
		return []seg{col(colorActivity, string(d.Activity))}
	}
	name := col(colorActivity, string(r.Activity))
	if !slices.Contains(config.Activities, r.Activity) {
		name = dim(string(r.Activity)) // "uncertain" is an answer, not an activity
	}
	segs := []seg{name, plain(" " + percent(r.ActivityP))}
	if len(r.Alternates) > 0 {
		alts := make([]string, len(r.Alternates))
		for i, a := range r.Alternates {
			alts[i] = string(a.Activity) + " " + percent(a.P)
		}
		segs = append(segs, dim("   "+strings.Join(alts, " · ")))
	}
	if applyAt > 0 {
		segs = append(segs, dim("   applies at "+percent(applyAt)+"+"))
	}
	return segs
}

func percent(p float64) string { return fmt.Sprintf("%d%%", int(math.Round(p*100))) }

func (c *canvas) pinRow(s route.Snapshot) {
	var segs []seg
	var support map[config.Tier]float64
	if s.Last != nil && s.Last.Reading != nil {
		support = s.Last.Reading.Tiers
	}
	for _, t := range tierOrder {
		label := string(t)
		if s.Last != nil && s.Last.Tier == t && s.Mode == route.ModeOn && s.Pin == nil {
			label = "▶ " + label
		}
		if p, ok := support[t]; ok {
			label += " " + percent(p)
		} else if support != nil {
			label += " —"
		}
		b := button(label, s.Pin != nil && *s.Pin == t)
		b.color = tierColor[t]
		b.act = action{kind: actPin, tier: t}
		segs = append(segs, b)
	}
	c.row("Pin next", append(segs, dim(" next turn only · p cycles"))...)
}

func (c *canvas) session(s route.Snapshot) {
	st := s.Stats
	if st.Replies == 0 {
		c.section("SESSION", dim(" · no routed replies yet"))
		return
	}
	head := fmt.Sprintf(" · since %s · %d routed replies · %d route switches", st.Since.Format(timeLayout), st.Replies, st.Switches)
	c.section("SESSION", plain(head))
	cells := st.Strip
	if room := c.width - 2*len(indent); len(cells) > room {
		cells = cells[len(cells)-room:]
	}
	strip := make([]seg, 0, len(cells)+2)
	strip = append(strip, plain(indent))
	for _, t := range cells {
		strip = append(strip, col(tierColor[t], "█"))
	}
	if st.RepliesPerCell > 0 {
		unit := "replies"
		if st.RepliesPerCell == 1 {
			unit = "reply"
		}
		strip = append(strip, dim(fmt.Sprintf("  %d %s per cell", st.RepliesPerCell, unit)))
	}
	c.add(strip...)

	counts := []seg{plain(indent)}
	for _, t := range tierOrder {
		if n := st.ByTier[t]; n > 0 {
			counts = append(counts, col(tierColor[t], "■"), plain(fmt.Sprintf(" %s %d   ", t, n)))
		}
	}
	c.add(counts...)
	if acts := topActivities(st.ByActivity, 3); len(acts) > 0 {
		line := []seg{plain(indent)}
		for i, a := range acts {
			if i > 0 {
				line = append(line, plain(" · "))
			}
			line = append(line, col(colorActivity, string(a.name)), plain(fmt.Sprintf(" %d", a.n)))
		}
		c.add(line...)
	}
	if sw := st.LastSwitch; sw != nil {
		line := plain(fmt.Sprintf("%slast switch %s  %s → %s · %s", indent, sw.At.Format(timeLayout), sw.From, sw.To, sw.Activity))
		if sw.MidTurn {
			c.add(line, dim(" (inside a turn)"))
		} else {
			c.add(line)
		}
	}
	c.add(append([]seg{plain(indent)}, savedSegs(st, s.Baseline)...)...)
}

func savedSegs(st route.Stats, baseline config.Route) []seg {
	if st.BaselineUSD <= 0 {
		return []seg{dim("no list-price baseline yet")}
	}
	diff := st.BaselineUSD - st.RoutedUSD
	pct := int(abs(diff)/st.BaselineUSD*100 + 0.5)
	word, color := "saved", colorOK
	if diff < 0 {
		word, color = "cost", colorWarn
	}
	segs := []seg{plain("Routing " + word + " "), col(color, fmt.Sprintf("≈ $%.2f (%d%%)", abs(diff), pct)), plain(" vs " + routeText(baseline))}
	if st.UncachedSwitchTokens > 0 {
		segs = append(segs, dim(" · switch writes "+fmtTokens(st.UncachedSwitchTokens)+" tokens"))
	}
	return segs
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
	sort.Slice(out, func(i, j int) bool {
		if out[i].n != out[j].n {
			return out[i].n > out[j].n
		}
		return out[i].name < out[j].name
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

func (c *canvas) contextRow(s route.Snapshot) {
	if s.Window <= 0 {
		c.row("Context", dim("window unknown"))
		return
	}
	pct := min(s.ApproxTokens*100/s.Window, 100)
	c.row("Context", plain(bar(pct)+fmt.Sprintf(" %d%%  ", pct)), dim(fmt.Sprintf("~%s / %s · fit guard: smallest window", fmtTokens(s.ApproxTokens), fmtTokens(s.Window))))
}

func (c *canvas) cacheRow(s route.Snapshot) {
	st := s.Stats
	if st.Replies == 0 {
		c.row("Cache", dim("no replies yet"))
		return
	}
	if st.LastInputTokens <= 0 {
		c.row("Cache", plain(fmt.Sprintf("%d%% reused this session", st.CacheReusePct)), dim(" · $ are list-price estimates"))
		return
	}
	c.row("Cache", plain(fmt.Sprintf("%d%% reused on the last reply", st.LastCachedTokens*100/st.LastInputTokens)),
		dim(fmt.Sprintf(" · %d%% this session · $ at list price", st.CacheReusePct)))
}

// findingHint names where a finding is changed.
var findingHint = map[route.FindingKind]string{
	route.FindingClassifier: "Classifier 4",
	route.FindingManual:     "space resumes",
	route.FindingContext:    "Routes 2",
	route.FindingBlocked:    "Routes 2",
	route.FindingCache:      "Policy 3",
	route.FindingState:      "P resumes the home",
}

func (c *canvas) worth(findings []route.Finding) {
	if len(findings) == 0 {
		return
	}
	c.section("WORTH A LOOK", dim(" · this session"))
	for _, f := range findings {
		text := f.Text
		if hint := findingHint[f.Kind]; hint != "" {
			text += " · " + hint
		}
		c.wrapped(indent, text, plain(""))
	}
}

func bar(pct int) string {
	n := pct * barCells / 100
	return strings.Repeat("█", n) + strings.Repeat("░", barCells-n)
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

func tierLabel(t config.Tier) seg {
	return seg{text: fmt.Sprintf("%-8s", t), color: tierColor[t]}
}

func (c *canvas) routes(s route.Snapshot) {
	c.add(dim("ROUTES"), dim(" · group "+groupText(s)))
	for _, t := range tierOrder {
		c.add(tierLabel(t), plain(routeText(s.Routes[t])))
	}
	c.section("BLOCKED")
	if len(s.Blocked) == 0 {
		c.add(dim("none"))
	}
	for _, b := range s.Blocked {
		c.add(dim(b))
	}
	c.section("RECENT")
	if len(s.Recent) == 0 {
		c.add(dim("no decisions yet"))
	}
	start := max(len(s.Recent)-recentRows, 0)
	for i := len(s.Recent) - 1; i >= start; i-- {
		d := s.Recent[i]
		c.add(plain(d.At.Format(timeLayout)+" "), tierSeg(d.Tier), plain(" "+routeText(d.Route)+"  "), col(colorActivity, string(d.Activity)), dim("  "+string(d.Reason)))
	}
}

func groupText(s route.Snapshot) string {
	g := s.Group
	if g.CompHash == "" {
		return "unknown"
	}
	out := "comp_hash " + g.CompHash
	if g.Lite {
		out += " · lite"
	}
	return out
}

func (c *canvas) policy(s route.Snapshot) {
	c.add(dim("PRESET"))
	var segs []seg
	for _, p := range Presets {
		b := button(string(p), p == s.Preset)
		b.act = action{kind: actPreset, preset: p}
		segs = append(segs, b)
	}
	c.add(segs...)
	p := s.Policy
	c.section("THRESHOLDS")
	c.add(plain(fmt.Sprintf("votes to move up %d · down %d · escalation hold %d turns", p.UpgradeVotes, p.DowngradeVotes, p.EscalationHoldTurns)))
	c.add(plain(fmt.Sprintf("min dwell %d requests · review every %d requests", p.MinDwellRequests, p.ReviewEveryRequests)))
	c.add(plain(fmt.Sprintf("upgrade bar %.2f · jump confidence %.2f · downgrade mass %.2f", p.UpgradeBase, p.JumpConfidence, p.DowngradeMass)))
	c.add(dim("on router error: forward the original request"))
}

func (c *canvas) classifier(s route.Snapshot) {
	h := s.Classifier
	c.add(dim("CLASSIFIER"))
	mark, color := "◉", colorOK
	if classifierPaused(s) || s.ClassifierOff {
		mark, color = "○", colorWarn
	}
	c.add(col(color, mark), plain(" "+classifierName(s)))
	if s.ClassifierOff {
		c.add(col(colorWarn, "off"), dim(" · no key, or off in the config; requests keep their model unless pinned"))
		return
	}
	if h.Latency > 0 {
		c.add(plain(fmt.Sprintf("last latency %d ms", h.Latency.Milliseconds())))
	}
	if s.ClassifierMedian > 0 {
		c.add(plain(fmt.Sprintf("median %d ms", s.ClassifierMedian.Milliseconds())), dim(" · recent answers"))
	}
	c.add(plain(fmt.Sprintf("consecutive failures %d", h.Failures)))
	if classifierPaused(s) {
		c.add(col(colorWarn, "paused until "+h.PausedUntil.Format("15:04:05")), dim(" · requests are forwarded unchanged"))
	}
}

func (c *canvas) usage(s route.Snapshot) {
	st := s.Stats
	c.add(dim("USAGE"), dim(" · this session"))
	c.add(plain(fmt.Sprintf("requests %d · switches %d · cache reuse %d%%", st.Replies, st.Switches, st.CacheReusePct)))
	c.add(savedSegs(st, s.Baseline)...)
	if st.BaselineUSD > 0 {
		c.add(dim(fmt.Sprintf("list-price estimate $%.2f vs $%.2f all-%s (plan billing is limits, not $)", st.RoutedUSD, st.BaselineUSD, routeText(s.Baseline))))
	}
	c.section("BY TIER")
	for _, t := range tierOrder {
		c.add(tierLabel(t), plain(strconv.Itoa(st.ByTier[t])))
	}
	c.section("BY ACTIVITY")
	acts := topActivities(st.ByActivity, 8)
	if len(acts) == 0 {
		c.add(dim("none"))
	}
	for _, a := range acts {
		c.add(col(colorActivity, fmt.Sprintf("%-10s", a.name)), plain(strconv.Itoa(a.n)))
	}
}
