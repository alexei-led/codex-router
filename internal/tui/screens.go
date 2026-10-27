package tui

import (
	"fmt"
	"math"
	"strings"

	"github.com/charmbracelet/x/ansi"

	"github.com/alexei-led/codex-router/internal/config"
	"github.com/alexei-led/codex-router/internal/route"
)

const (
	// kvWidth is the label column of the Policy and Usage views.
	kvWidth = 12
	// routesTwoColumns is the width from which the routes sit in two columns.
	routesTwoColumns = 72
	routeCell        = 22
	tierCell         = 7
	usageBarCells    = 24
	nameCell         = 10
	activityRows     = 8
)

func kv(name string) seg { return plain(fmt.Sprintf(" %-*s", kvWidth-1, name)) }

func blankKV() seg { return plain(strings.Repeat(" ", kvWidth)) }

// policy is the focused session's routing mode, the classifier, the routes
// of each tier under that mode and the thresholds.
func (c *canvas) policy(v view) {
	s := v.snap
	c.modeRow(s)
	c.classifierRow(s)
	c.routesRows(s)
	c.switchingRows(s.Policy)
}

// modeRow is a button per mode, the session's marked, and where it comes
// from. An engine older than routing modes sends no mode.
func (c *canvas) modeRow(s route.Snapshot) {
	segs := []seg{kv("Mode")}
	c.anchors["mode"] = anchor{row: len(c.out), x: segsWidth(segs)}
	for _, mode := range Modes {
		b := seg{text: " " + string(mode) + "  ", role: roleAccent}
		switch mode {
		case route.ModeDefault:
			b.role = roleMuted
		case s.RoutingMode:
			b = seg{text: "▶" + string(mode) + "  ", role: roleAccent, bold: true}
		}
		segs = append(segs, clickable(b, action{kind: actMode, mode: mode}))
	}
	right := []seg{dim("from " + string(s.RoutingModeSource) + " · this session")}
	switch {
	case s.RoutingMode == "":
		right = []seg{dim("— the router predates modes")}
	case s.ClassifierOff:
		right = []seg{col(roleWarn, "no effect while the classifier is off")}
	}
	c.split(segs, right)
}

func (c *canvas) classifierRow(s route.Snapshot) {
	h := s.Classifier
	name := classifierName(s)
	if s.ClassifierModel != "" {
		name += sep + s.ClassifierModel
	}
	var parts [][]seg
	switch {
	case s.ClassifierOff:
		parts = [][]seg{{col(roleWarn, "○ off")}}
		if s.ClassifierOffReason != "" {
			parts = append(parts, []seg{plain(s.ClassifierOffReason)})
		}
		parts = append(parts, []seg{dim("requests keep Codex's model; only pins change it")})
	case classifierPaused(s):
		secs := int(math.Ceil(h.PausedUntil.Sub(s.At).Seconds()))
		parts = [][]seg{{col(roleErr, "✕ "+classifierName(s)+" unreachable")}, {plain(fmt.Sprintf("resumes in %ds", secs))}}
		if h.LastError != "" {
			parts = append(parts, []seg{plain("last error: "), col(roleWarn, h.LastError)})
		}
		parts = append(parts, []seg{dim("requests are forwarded unchanged")})
	case h.Latency == 0 && h.Failures == 0 && h.LastError == "" && s.ClassifierMedian == 0:
		parts = [][]seg{{col(roleOK, "● "+name)}, {dim("ready · no calls yet")}}
	default:
		parts = [][]seg{{col(roleOK, "● "+name)}}
		switch {
		case s.ClassifierMedian > 0:
			parts = append(parts, []seg{plain(fmt.Sprintf("%d ms median", s.ClassifierMedian.Milliseconds()))})
		case h.Latency > 0:
			parts = append(parts, []seg{plain(fmt.Sprintf("%d ms last", h.Latency.Milliseconds()))})
		}
		parts = append(parts, []seg{plain(plural(h.Failures, "failure", "failures"))})
		if h.LastError != "" {
			parts = append(parts, []seg{plain("last error: "), col(roleWarn, h.LastError)})
		}
	}
	c.flow(kv("Classifier"), parts...)
}

func (c *canvas) routesRows(s route.Snapshot) {
	routeWidth := routeCell
	for _, tier := range config.Tiers {
		routeWidth = max(routeWidth, ansi.StringWidth(routeText(s.Routes[tier]))+2)
	}
	cell := func(t config.Tier) []seg {
		text := routeText(s.Routes[t])
		return []seg{col(tierRole[t], pad(string(t), tierCell)), plain(pad(text, routeWidth))}
	}
	group := dim(groupText(s))
	var rows [][]seg
	if c.width >= routesTwoColumns {
		for i := 0; i < len(tierOrder); i += 2 {
			rows = append(rows, append(cell(tierOrder[i]), cell(tierOrder[i+1])...))
		}
	} else {
		for _, t := range tierOrder {
			rows = append(rows, cell(t))
		}
	}
	for i, r := range rows {
		lead := blankKV()
		if i == 0 {
			lead = kv("Routes")
		}
		left := append([]seg{lead}, r...)
		if i == len(rows)-1 {
			c.split(left, []seg{group})
			continue
		}
		c.add(left...)
	}
	for _, b := range s.Blocked {
		c.add(kv("Blocked"), plain(b), dim(" · outside "+s.Baseline.Model+"'s group"))
	}
	switch {
	case !s.CatalogAvailable:
		c.add(dim(" Compatibility unknown · catalog unavailable"))
	case len(s.CompatibilityUnknown) > 0:
		c.add(dim(" Compatibility unknown · missing catalog entries: " + strings.Join(s.CompatibilityUnknown, ", ")))
	case !s.CurrentModelKnown:
		c.add(dim(" Compatibility unknown · current model is missing from the catalog"))
	}
}

func groupText(s route.Snapshot) string {
	if !s.CatalogAvailable || !s.CurrentModelKnown {
		return ""
	}
	return "group of " + s.Baseline.Model
}

func (c *canvas) switchingRows(p config.Policy) {
	c.flow(kv("Switching"),
		[]seg{plain(fmt.Sprintf("up after %s, bar %.2f", plural(p.UpgradeVotes, "vote", "votes"), p.UpgradeBase))},
		[]seg{plain(fmt.Sprintf("down after %s, bar %.2f", plural(p.DowngradeVotes, "vote", "votes"), p.DowngradeMass))},
		[]seg{plain("min dwell " + plural(p.MinDwellRequests, "request", "requests"))})
	c.flow(blankKV(),
		[]seg{dim("escalation hold " + plural(p.EscalationHoldTurns, "turn", "turns"))},
		[]seg{dim("review every " + plural(p.ReviewEveryRequests, "request", "requests"))},
		[]seg{dim(fmt.Sprintf("jump bar %.2f", p.JumpConfidence))})
}

// usage is the focused session's savings as a sentence, a ledger, and the
// replies by tier and activity as block bars.
func (c *canvas) usage(v view) {
	s := v.snap
	st := s.Stats
	c.title(v)
	if st.Replies == 0 {
		c.add(kv("Usage"), dim("no replies yet"))
		return
	}
	c.add(append([]seg{kv("Usage")}, savedSegs(st)...)...)
	if pricingComplete(st) {
		if st.SavingsUSD > 0 || st.ExtraUSD > 0 {
			c.flow(blankKV(),
				[]seg{col(roleOK, "savings ≈ "+dollars(st.SavingsUSD))},
				[]seg{col(roleWarn, "extra cost ≈ "+dollars(st.ExtraUSD))})
		}
		c.flow(blankKV(),
			[]seg{dim(fmt.Sprintf("list-price estimate %s vs %s per-reply native basis", dollars(st.RoutedUSD), dollars(st.BaselineUSD)))},
			[]seg{dim("plan billing is limits, not $")})
	}
	ledger := [][]seg{
		{plain(plural(st.Replies, "reply", "replies") + " since " + st.Since.Format(timeLayout))},
		{plain(plural(st.Switches, "switch", "switches"))},
		{plain(fmt.Sprintf("cache reuse %d%%", st.CacheReusePct))},
	}
	if st.UncachedSwitchTokens > 0 {
		ledger = append(ledger, []seg{dim("switches wrote " + fmtTokens(st.UncachedSwitchTokens) + " uncached tokens")})
	}
	c.blank()
	c.flow(kv("Ledger"), ledger...)
	if sw := st.LastSwitch; sw != nil {
		line := []seg{blankKV(), plain(fmt.Sprintf("last switch %s  %s → %s · %s", sw.At.Format(timeLayout), sw.From, sw.To, sw.Activity))}
		if sw.MidTurn {
			line = append(line, dim(" (inside a turn)"))
		}
		c.add(line...)
	}

	c.blank()
	top := 0
	for _, n := range st.ByTier {
		top = max(top, n)
	}
	for i, t := range tierOrder {
		lead := blankKV()
		if i == 0 {
			lead = kv("Tiers")
		}
		c.countRow(lead, col(tierRole[t], pad(string(t), nameCell)), tierRole[t], st.ByTier[t], top, st.Replies)
	}
	acts := topActivities(st.ByActivity, activityRows)
	if len(acts) == 0 {
		return
	}
	c.blank()
	top = acts[0].n
	for i, a := range acts {
		lead := blankKV()
		if i == 0 {
			lead = kv("Activity")
		}
		c.countRow(lead, col(roleActivity, pad(string(a.name), nameCell)), roleActivity, a.n, top, st.Replies)
	}
}

// countRow is a name, a block bar scaled to top, the count and its share.
func (c *canvas) countRow(lead, name seg, r role, n, top, total int) {
	room := min(usageBarCells, max(c.width-kvWidth-nameCell-10, 4))
	cells := 0
	if top > 0 {
		cells = (n*room + top - 1) / top
	}
	cells = min(cells, room)
	c.add(lead, name, col(r, strings.Repeat("█", cells)), plain(strings.Repeat(" ", room-cells)), plain(fmt.Sprintf(" %4d", n)), dim(fmt.Sprintf(" %3d%%", n*100/max(total, 1))))
}
