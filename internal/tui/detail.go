package tui

import (
	"fmt"
	"slices"
	"strings"

	"github.com/alexei-led/codex-router/internal/config"
	"github.com/alexei-led/codex-router/internal/policy"
	"github.com/alexei-led/codex-router/internal/route"
)

const (
	contextCells = 10
	// stripSuffixWidth leaves room for `  4 replies per cell` after the strip.
	stripSuffixWidth = 22
)

// reasonMark points the way a decision moved the tier.
func reasonMark(r policy.Reason) string {
	switch r {
	case policy.ReasonEscalation, policy.ReasonUpgrade, policy.ReasonJump, policy.ReasonFit:
		return "↑ "
	case policy.ReasonDowngrade:
		return "↓ "
	}
	return ""
}

// chip is the band's state word as a reversed block.
func chip(text string, r role) seg {
	return seg{text: " " + text + " ", role: r, bold: true, reverse: true}
}

// title is the session's name and home, the first line of its detail.
func (c *canvas) title(v view) {
	st := v.fleet
	if st == nil {
		return
	}
	s, ok := st.focused()
	if !ok {
		return
	}
	h, _ := st.homeOf(s.Home)
	left := []seg{bold(strings.TrimPrefix(s.Label, h.home.Name+" · "))}
	right := []seg{dim(h.home.Name + sep + shortID(s.Home))}
	if !v.disconnected && !v.loading && !v.snap.FooterSynced {
		right = append(right, col(roleWarn, sep+"footer not synced"))
	}
	c.split(left, right)
}

func shortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

// detail is the focused session: why it runs the model it runs, and how the
// session is going.
func (c *canvas) detail(v view) {
	c.title(v)
	s := v.snap
	switch {
	case v.loading:
		c.add(dim(" loading the session…"))
		return
	case v.disconnected:
		segs := []seg{col(roleWarn, " ✕ disconnected"), dim(" · retrying")}
		if !s.At.IsZero() {
			segs = append(segs, dim(" · last snapshot "+s.At.Format("15:04:05")))
		}
		c.add(segs...)
	case v.fleet != nil && v.fleet.ended:
		c.add(col(roleWarn, " ■ ended"), dim(" · the session's router exited"))
	}
	c.band(s)
	c.pinRow(s)
	c.sessionRows(s)
	c.contextRows(s)
	c.look(s)
	c.recent(s)
}

// band is the decision: the state chip, the activity and the route, the
// reason against the edge, then the evidence.
func (c *canvas) band(s route.Snapshot) {
	var chipSeg seg
	var text, right []seg
	var evidence [][]seg
	switch {
	case s.StateLost:
		chipSeg = chip("STATE LOST", roleWarn)
		text = []seg{plain("routing paused until you resume")}
		right = []seg{dim("P resumes the home")}
	case s.Mode == route.ModeManual:
		chipSeg = chip("MANUAL", roleWarn)
		text = []seg{plain("footer picked with /model · routing holds "), bold(routeText(s.Footer))}
		right = []seg{dim("space resumes")}
	case s.Mode == route.ModePaused:
		chipSeg = chip("PAUSED", roleMuted)
		text = []seg{plain("every request goes to " + routeText(s.Baseline) + " unchanged")}
		right = []seg{dim("space resumes")}
	case s.Pin != nil:
		chipSeg = chip(strings.ToUpper(string(*s.Pin)), tierRole[*s.Pin])
		text = []seg{plain("pinned → "), bold(routeText(s.Routes[*s.Pin]))}
		right = []seg{dim("next turn only")}
	case s.ClassifierOff:
		chipSeg = chip("PINS ONLY", roleWarn)
		text = []seg{plain("requests keep "), bold(routeText(s.Baseline))}
		evidence = [][]seg{{col(roleWarn, "○ "+classifierName(s)+" off")}}
		if s.ClassifierOffReason != "" {
			evidence = append(evidence, []seg{plain(s.ClassifierOffReason)})
		}
		evidence = append(evidence, []seg{dim("pin a tier with p")})
	case s.Last != nil:
		chipSeg, text, right, evidence = c.decision(s, turnDecision(s.Recent, *s.Last))
	default:
		chipSeg = chip("WAITING", roleMuted)
		text = []seg{dim("no routed request yet · baseline " + routeText(s.Baseline))}
	}
	lead := []seg{chipSeg, plain(" ")}
	c.split(append(lead, text...), right)
	if gap := c.width - segsWidth(lead) - segsWidth(text) - segsWidth(right); len(right) > 0 && gap < 1 {
		c.add(append([]seg{plain(strings.Repeat(" ", segsWidth(lead)))}, right...)...)
	}
	if len(evidence) > 0 {
		c.flow(plain(strings.Repeat(" ", segsWidth(lead))), evidence...)
	}
}

// decision draws the engine's last decision: the tier chip, the activity and
// route, the reason, and the classifier's support against its bar.
func (c *canvas) decision(s route.Snapshot, d route.Decision) (seg, []seg, []seg, [][]seg) {
	kept := slices.Contains(keptReasons, d.Reason)
	chipSeg := chip(strings.ToUpper(string(d.Tier)), tierRole[d.Tier])
	if kept {
		chipSeg = chip("KEPT", roleMuted)
	}
	var text []seg
	if a := activityOf(d); a != "" {
		text = append(text, col(roleActivity, a), plain(" → "))
	}
	text = append(text, bold(routeText(d.Route)))
	if kept {
		text = append(text, dim(" · "+whyOf(d)))
		return chipSeg, text, nil, supportSegs(s, d)
	}
	right := []seg{plain(reasonMark(d.Reason)), plain(whyOf(d))}
	return chipSeg, text, right, supportSegs(s, d)
}

func activityOf(d route.Decision) string {
	if r := d.Reading; r != nil && r.Activity != "" {
		return string(r.Activity)
	}
	return string(d.Activity)
}

// supportSegs is the evidence line: the classifier's answer for the tier
// against the policy's bar, then the engine's own support text.
func supportSegs(s route.Snapshot, d route.Decision) [][]seg {
	var parts [][]seg
	if r := d.Reading; r != nil {
		if p, ok := r.Tiers[d.Tier]; ok {
			parts = append(parts, []seg{plain(fmt.Sprintf("%s %s %s", classifierName(s), d.Tier, percent(p)))})
		}
	}
	for _, p := range strings.Split(d.Support, sep) {
		if p != "" {
			parts = append(parts, []seg{plain(p)})
		}
	}
	return parts
}

// pinRow is the tier buttons, low to high. The running tier is marked ▶, the
// pinned one reversed.
func (c *canvas) pinRow(s route.Snapshot) {
	segs := []seg{plain(fmt.Sprintf(" %-*s", labelWidth-2, "Pin"))}
	c.anchors["pin"] = anchor{row: len(c.out), x: segsWidth(segs) + 1}
	for _, t := range tierOrder {
		pinned := s.Pin != nil && *s.Pin == t
		running := s.Last != nil && s.Last.Tier == t && s.Mode == route.ModeOn && s.Pin == nil &&
			!s.ClassifierOff && !slices.Contains(keptReasons, s.Last.Reason)
		text := " " + string(t) + "  "
		if running {
			text = "▶" + string(t) + "  "
		}
		b := seg{text: text, role: tierRole[t], reverse: pinned, bold: pinned || running}
		segs = append(segs, clickable(b, action{kind: actPin, tier: t}))
	}
	c.split(segs, []seg{dim("next turn only")})
}

// sessionRows is the session's summary and its strip of routed replies.
func (c *canvas) sessionRows(s route.Snapshot) {
	st := s.Stats
	lead := label("Session")
	if st.Replies == 0 {
		c.add(lead, dim("no routed replies yet"))
		return
	}
	saved := []seg{dim(`savings need "prices" in the config`)}
	if st.BaselineUSD > 0 {
		saved = savedPart(st, s.Baseline)
	}
	parts := [][]seg{{plain(plural(st.Replies, "reply", "replies"))}, {plain(plural(st.Switches, "switch", "switches"))}, saved}
	c.flow(lead, parts...)
	room := c.width - labelWidth - stripSuffixWidth
	cells := st.Strip
	if len(cells) > room {
		cells = cells[len(cells)-room:]
	}
	line := []seg{plain(strings.Repeat(" ", labelWidth))}
	for _, t := range cells {
		line = append(line, col(tierRole[t], spark[t]))
	}
	if st.RepliesPerCell > 0 {
		unit := "replies"
		if st.RepliesPerCell == 1 {
			unit = "reply"
		}
		line = append(line, dim(fmt.Sprintf("  %d %s per cell", st.RepliesPerCell, unit)))
	}
	c.add(line...)
}

// savedPart is `saved ≈ $1.18 (22%) vs gpt-6-sol · medium`.
func savedPart(st route.Stats, baseline config.Route) []seg {
	diff := st.BaselineUSD - st.RoutedUSD
	word, r := "saved", roleOK
	if diff < 0 {
		word, r = "cost", roleWarn
	}
	pct := int(abs(diff)/st.BaselineUSD*100 + 0.5)
	return []seg{plain(word + " "), col(r, fmt.Sprintf("≈ $%.2f (%d%%)", abs(diff), pct)), plain(" vs " + routeText(baseline))}
}

// contextRows are the context window and the cache, side by side when they fit.
func (c *canvas) contextRows(s route.Snapshot) {
	var ctx, cache []seg
	if s.Window <= 0 {
		ctx = []seg{label("Context"), dim("window unknown")}
	} else {
		pct := min(s.ApproxTokens*100/s.Window, 100)
		ctx = []seg{label("Context"), plain(bar(pct, contextCells) + fmt.Sprintf(" %d%%  ", pct)), dim(fmt.Sprintf("%s / %s", fmtTokens(s.ApproxTokens), fmtTokens(s.Window)))}
	}
	st := s.Stats
	switch {
	case st.Replies == 0:
	case st.LastInputTokens <= 0:
		cache = []seg{bold("Cache"), plain(fmt.Sprintf("  %d%% this session", st.CacheReusePct))}
	default:
		cache = []seg{bold("Cache"), plain(fmt.Sprintf("  %d%% last reply", st.LastCachedTokens*100/st.LastInputTokens)), dim(fmt.Sprintf(" · %d%% session", st.CacheReusePct))}
	}
	if cache == nil {
		c.add(ctx...)
		return
	}
	if segsWidth(ctx)+6+segsWidth(cache) <= c.width {
		c.add(append(append(ctx, plain("      ")), cache...)...)
		return
	}
	c.add(ctx...)
	cache[1].text = strings.TrimLeft(cache[1].text, " ")
	c.add(append([]seg{label("Cache")}, cache[1:]...)...)
}

// findingHint names where a finding is changed.
var findingHint = map[route.FindingKind]string{
	route.FindingClassifier: "see Policy",
	route.FindingManual:     "space resumes",
	route.FindingContext:    "see Policy",
	route.FindingBlocked:    "see Policy",
	route.FindingCache:      "see Policy",
	route.FindingState:      "P resumes the home",
}

// look is what the engine says is worth a look, a line for each finding.
func (c *canvas) look(s route.Snapshot) {
	for i, f := range s.Worth {
		text := f.Text
		if hint := findingHint[f.Kind]; hint != "" {
			text += sep + hint
		}
		lead := plain(strings.Repeat(" ", labelWidth))
		if i == 0 {
			lead = label("Look")
		}
		c.wrapped(lead, text, plain(""))
	}
}

// recentShown bounds the Recent block.
const recentShown = 5

// recent lists the last decisions, newest first, so each turn in the strip
// can be traced to its route and reason.
func (c *canvas) recent(s route.Snapshot) {
	for n, g := range recentGroups(s.Recent, recentShown) {
		lead := plain(strings.Repeat(" ", labelWidth))
		if n == 0 {
			lead = label("Recent")
		}
		d := g.decision
		mark := col(tierRole[d.Tier], fmt.Sprintf("%-7s", d.Tier))
		if slices.Contains(keptReasons, d.Reason) {
			mark = dim(fmt.Sprintf("%-7s", "kept"))
		}
		segs := []seg{lead, dim(d.At.Format("15:04") + "  "), mark, plain(routeText(d.Route)), dim(sep + whyOf(d))}
		if g.steps > 1 {
			segs = append(segs, dim(fmt.Sprintf(" · +%d steps", g.steps-1)))
		}
		c.add(segs...)
	}
}

// turnDecision is d with the reason, reading and support of its turn's
// first decision when d only carries the turn on (a later step keeps the
// route the turn chose; its own reason says nothing new).
func turnDecision(recent []route.Decision, d route.Decision) route.Decision {
	if d.Reason != route.ReasonContinue {
		return d
	}
	for i := len(recent) - 1; i >= 0; i-- {
		h := recent[i]
		if h.TurnID != d.TurnID || h.Reason == route.ReasonContinue {
			continue
		}
		if h.Route == d.Route {
			d.Reason, d.Why, d.Reading, d.Support = h.Reason, h.Why, h.Reading, h.Support
		}
		break
	}
	return d
}

// recentGroup is consecutive decisions of one turn on one route.
type recentGroup struct {
	decision route.Decision
	steps    int
}

// recentGroups folds recent, newest first, into at most n groups: the steps
// of a turn that kept its route are one row with the turn's own reason.
func recentGroups(recent []route.Decision, n int) []recentGroup {
	var out []recentGroup
	for i := len(recent) - 1; i >= 0; i-- {
		d := recent[i]
		if k := len(out) - 1; k >= 0 && d.TurnID != "" && out[k].decision.TurnID == d.TurnID && out[k].decision.Route == d.Route {
			out[k].steps++
			if out[k].decision.Reason == route.ReasonContinue {
				out[k].decision = d
			}
			continue
		}
		if len(out) == n {
			break
		}
		out = append(out, recentGroup{decision: d, steps: 1})
	}
	return out
}
