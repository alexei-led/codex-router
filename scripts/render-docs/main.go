// Command render-docs draws the pictures in docs/images from the real TUI
// renderer and a sample snapshot. Run it from the repository root after a UI
// change:
//
//	go run ./scripts/render-docs
//
// It writes one SVG per panel screen: the Sessions view stacked at the width
// of a typical pane and side by side, Policy, Usage, the help overlay, the pin
// popover and the panel locked to a session, plus one for the Codex footer
// capture and the README split, and a PNG beside each when rsvg-convert is on
// PATH. The data is made up; it matches the debugging session described in
// docs/user-guide.md.
package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/alexei-led/codex-router/internal/catalog"
	"github.com/alexei-led/codex-router/internal/classifier"
	"github.com/alexei-led/codex-router/internal/config"
	"github.com/alexei-led/codex-router/internal/control"
	"github.com/alexei-led/codex-router/internal/home"
	"github.com/alexei-led/codex-router/internal/policy"
	"github.com/alexei-led/codex-router/internal/route"
	"github.com/alexei-led/codex-router/internal/tui"
)

const (
	// panelWidth is a typical pane: Codex on the left, the panel on the right
	// of a wide terminal.
	panelWidth = 97
	// wideWidth is a panel with its own terminal: the list beside the detail.
	wideWidth = 130
)

// picture is one panel screen of the docs.
type picture struct {
	name  string
	title string
	desc  string
	set   func(*tui.FleetView)
}

var pictures = []picture{
	{"router-tui-sessions", "codex-router panel, Sessions view",
		"codex-router tui in a 97 column pane. The header counts 5 sessions in 2 homes, says Jev is ready, and lists the views Sessions, Policy and Usage. The list groups the sessions by Codex home: the default home has three codex-router runs, the work home has a brain that has run for 2 hours and a router whose Codex has not had a first prompt yet. Each row has the session's state, model and effort, a sparkline of the last turns by tier and the last switch. Below, the selected session: the high tier chip, the debug activity and the route gpt-6.1-sol high, why the router moved up, the classifier's evidence, the pin buttons, the session summary with its strip, context and cache, and one thing worth a look.",
		func(*tui.FleetView) {}},
	{"router-tui-sessions-wide", "codex-router panel, Sessions view, side by side",
		"The Sessions view in a terminal of 130 columns. The list of sessions is on the left and the selected session's detail on the right, with an accent gutter on the focused side.",
		func(v *tui.FleetView) { v.Width = wideWidth }},
	{"router-tui-policy", "codex-router panel, Policy view",
		"Policy view: the preset buttons with balanced marked, the classifier Jev with its model and median latency, the model and effort of each tier, the compatibility group, and the thresholds for switching.",
		func(v *tui.FleetView) { v.Tab = tui.TabPolicy }},
	{"router-tui-usage", "codex-router panel, Usage view",
		"Usage view: the estimated saving against the baseline model as one sentence, a ledger of replies, switches and cache reuse, and block bars of replies by tier and by activity.",
		func(v *tui.FleetView) { v.Tab = tui.TabUsage }},
	{"router-tui-popover", "codex-router panel, pin popover",
		"The Sessions view after p: a small box under the pin row offers 1 micro, 2 low, 3 medium, 4 high and 0 none. The footer lists only the keys that work now.",
		func(v *tui.FleetView) { v.Popover = true }},
	{"router-tui-help", "codex-router panel, help overlay",
		"The help overlay drawn over the panel after ?: every key, grouped as Navigate, Session, Home and Views.",
		func(v *tui.FleetView) { v.Help = true }},
}

func main() {
	out := flag.String("out", "docs/images", "output directory")
	footer := flag.String("footer", "docs/images/src/codex-footer.txt", "plain-text Codex footer capture")
	session := flag.String("session", "docs/images/src/codex-session.txt", "Codex pane of the split picture, with ANSI colors")
	flag.Parse()
	if err := render(*out, *footer, *session); err != nil {
		fmt.Fprintln(os.Stderr, "render-docs:", err)
		os.Exit(1)
	}
}

func render(out, footerPath, sessionPath string) error {
	if err := os.MkdirAll(out, 0o755); err != nil {
		return err
	}
	snap := sampleSnapshot()
	homes, sessions := sampleFleet(snap)
	fleet := tui.FleetView{
		Homes: homes, Sessions: sessions, Brains: sampleBrains(homes), Waiting: sampleWaiting(homes),
		Focus: sessions[0].ID, Snap: snap, Width: panelWidth,
	}
	for _, p := range pictures {
		v := fleet
		p.set(&v)
		if err := write(out, p.name, toSVG(parseANSI(tui.RenderFleetView(v)), p.title, p.desc)); err != nil {
			return err
		}
	}
	fleet.Locked = true
	locked := tui.RenderFleetView(fleet)
	const lockedDesc = "codex-router tui --session, locked to one session. There is no list: the header shows the views and the detail of default · codex-router · main fills the screen. The turn moved up to gpt-6.1-sol high after the same tool failure repeated."
	if err := write(out, "router-tui-locked", toSVG(parseANSI(locked), "codex-router panel locked to one session", lockedDesc)); err != nil {
		return err
	}
	text, err := os.ReadFile(footerPath)
	if err != nil {
		return err
	}
	lines := parseANSI(strings.TrimRight(string(text), "\n ") + "\n")
	const footerDesc = "The Codex terminal UI while a turn runs. The footer line shows the model the router chose for the turn, GPT-6-Sol with medium effort, next to the branch, the context window and the remaining context."
	if err := write(out, "codex-footer", toSVG(lines, "Codex footer showing the routed model", footerDesc)); err != nil {
		return err
	}
	return renderSplit(out, sessionPath, locked)
}

// renderSplit draws the README picture: Codex on the left, the panel locked
// to that session on the right, as two panes of one terminal.
func renderSplit(out, sessionPath, panel string) error {
	text, err := os.ReadFile(sessionPath)
	if err != nil {
		return err
	}
	left := parseANSI(strings.TrimRight(string(text), "\n ") + "\n")
	right := parseANSI(panel)
	const desc = "A terminal split. Left: Codex runs a Go test that fails twice with an edit in between, and its footer shows GPT-6.1-Sol high. Right: the codex-router panel says this turn moved from gpt-6-sol medium to gpt-6.1-sol high because the same tool failure repeated, shows the classifier's tier and activity reading, the session's routed replies by tier, the estimated saving, and that the footer is synced."
	return write(out, "codex-router-split", toSVG(sideBySide(left, right), "Codex and the codex-router panel side by side", desc))
}

// write saves name.svg and, when rsvg-convert exists, name.png.
func write(dir, name, svg string) error {
	path := filepath.Join(dir, name+".svg")
	if err := os.WriteFile(path, []byte(svg), 0o644); err != nil {
		return err
	}
	bin, err := exec.LookPath("rsvg-convert")
	if err != nil {
		return nil
	}
	png := filepath.Join(dir, name+".png")
	if msg, err := exec.Command(bin, "--zoom", "2", "-o", png, path).CombinedOutput(); err != nil {
		return fmt.Errorf("rsvg-convert %s: %w: %s", name, err, msg)
	}
	return nil
}

func at(h, m int) time.Time { return time.Date(2026, 10, 13, h, m, 0, 0, time.UTC) }

// sampleBrains marks the work home as set up, with its brain up for 2 hours.
// The default home runs plain codex-router sessions, one endpoint each.
func sampleBrains(homes []home.Home) map[string]time.Duration {
	for _, h := range homes {
		if h.Name == "work" {
			return map[string]time.Duration{h.ID: 2 * time.Hour}
		}
	}
	return nil
}

// sampleWaiting is a router in the work home whose Codex has not had a first
// prompt yet.
func sampleWaiting(homes []home.Home) map[string][]string {
	for _, h := range homes {
		if h.Name == "work" {
			return map[string][]string{h.ID: {"work · reflex · main"}}
		}
	}
	return nil
}

// sampleFleet is five sessions in two Codex homes, in panel order. The first
// is the session of snap, the focus.
func sampleFleet(snap route.Snapshot) ([]home.Home, []route.SessionInfo) {
	personal := home.Home{ID: "3fa9c0d1e2b4", Path: "/Users/dev/.codex", Name: "default"}
	work := home.Home{ID: "8c1f5e27a90b", Path: "/Users/dev/.codex-work", Name: "work"}
	tiers := func(s string) []config.Tier {
		names := map[rune]config.Tier{'u': config.TierMicro, 'l': config.TierLow, 'm': config.TierMedium, 'h': config.TierHigh}
		out := make([]config.Tier, 0, len(s))
		for _, r := range s {
			out = append(out, names[r])
		}
		return out
	}
	usd := func(v float64) *float64 { return &v }
	session := func(h home.Home, thread, label string, mod func(*route.SessionInfo)) route.SessionInfo {
		s := route.SessionInfo{
			ID: control.SessionID(h.ID, thread), Home: h.ID, ThreadID: thread, Label: h.Name + " · " + label,
			Mode: route.ModeOn, Tier: config.TierMedium, Route: config.Route{Model: "gpt-6-sol", Effort: config.EffortMedium},
			FooterSynced: true,
		}
		mod(&s)
		return s
	}
	st := snap.Stats
	sessions := []route.SessionInfo{
		session(personal, "019a3c1e-7b2f-7d10-9c4e-2f6b1a8d5e01", "codex-router · main", func(s *route.SessionInfo) {
			s.Tier, s.Route, s.Attention = config.TierHigh, snap.Last.Route, route.AttentionEscalation
			s.Replies, s.Switches, s.Strip, s.LastSwitch = st.Replies, st.Switches, st.Strip[len(st.Strip)-route.SessionStrip:], st.LastSwitch
			s.LastActive, s.SavedUSD, s.SpentUSD = at(18, 43), usd(st.BaselineUSD-st.RoutedUSD), usd(st.RoutedUSD)
		}),
		session(personal, "019a3c52-0e4d-7a61-8b3f-91c2d7e4a6f3", "codex-router · fix/hook", func(s *route.SessionInfo) {
			s.Tier, s.Route = config.TierLow, config.Route{Model: "gpt-6-luna", Effort: config.EffortMedium}
			s.Replies, s.Switches, s.Strip = 31, 4, tiers("mmmmmmlllllllllllullllll")
			s.LastSwitch = &route.Switch{At: at(18, 29), From: config.TierMedium, To: config.TierLow, Activity: config.ActivityExplore}
			s.LastActive, s.SavedUSD, s.SpentUSD = at(18, 40), usd(0.41), usd(0.37)
		}),
		session(personal, "019a3b97-5c18-7f02-a6d9-3e0b8c2f7d14", "notes", func(s *route.SessionInfo) {
			s.Mode, s.Attention, s.Tier, s.Route = route.ModePaused, route.AttentionPaused, config.TierLow, config.Route{Model: "gpt-6-luna", Effort: config.EffortMedium}
			s.Replies, s.Strip = 6, tiers("llllll")
			s.LastActive, s.SavedUSD, s.SpentUSD = at(18, 11), usd(0.05), usd(0.04)
		}),
		session(work, "019a3c3a-2f71-7b95-9d0e-6a4c1f8b3e27", "api · feat/auth", func(s *route.SessionInfo) {
			s.Mode, s.Attention = route.ModeManual, route.AttentionManual
			s.Replies, s.Switches, s.Strip = 58, 7, tiers("mmmmhhhhmmmmmmmllmmmmmmm")
			s.LastSwitch = &route.Switch{At: at(18, 22), From: config.TierLow, To: config.TierMedium, Activity: config.ActivityCode}
			s.LastActive, s.SavedUSD, s.SpentUSD = at(18, 42), usd(0.66), usd(2.31)
		}),
		session(work, "019a3c47-9a03-7c28-b1e5-0d7f2a6c9b48", "infra · main", func(s *route.SessionInfo) {
			s.Replies, s.Switches, s.Strip = 12, 1, tiers("lllllmmmmmmm")
			s.LastSwitch = &route.Switch{At: at(18, 35), From: config.TierLow, To: config.TierMedium, Activity: config.ActivityOps}
			s.LastActive, s.SavedUSD, s.SpentUSD = at(18, 36), usd(0.09), usd(0.48)
		}),
	}
	return []home.Home{personal, work}, sessions
}

func sampleSnapshot() route.Snapshot {
	cfg := config.Default()
	now := at(18, 43)
	solMed := config.Route{Model: "gpt-6-sol", Effort: config.EffortMedium}
	solHigh := config.Route{Model: "gpt-6-sol", Effort: config.EffortHigh}
	lunaMed := cfg.Routes[config.TierLow]
	sol61 := cfg.Routes[config.TierHigh]

	step := func(h, m int, tier config.Tier, act config.Activity, r config.Route, why policy.Reason) route.Decision {
		return route.Decision{At: at(h, m), Tier: tier, Activity: act, Route: r, Reason: why}
	}
	recent := []route.Decision{
		step(18, 31, config.TierMedium, config.ActivityCode, solMed, policy.ReasonUpgrade),
		step(18, 33, config.TierMedium, config.ActivityCode, solMed, policy.ReasonSameTier),
		step(18, 36, config.TierLow, config.ActivityExplore, lunaMed, policy.ReasonDowngrade),
		step(18, 38, config.TierMedium, config.ActivityDebug, solHigh, policy.ReasonUpgrade),
		step(18, 38, config.TierMedium, config.ActivityDebug, solHigh, policy.ReasonSameTier),
		step(18, 40, config.TierMedium, config.ActivityDebug, solHigh, policy.ReasonSameTier),
		step(18, 41, config.TierMedium, config.ActivityDebug, solHigh, policy.ReasonSameTier),
		step(18, 42, config.TierHigh, config.ActivityDebug, sol61, policy.ReasonEscalation),
	}
	last := recent[len(recent)-1]
	last.Original = solMed
	last.Rewritten = true
	last.MidTurn = true
	last.Why = "escalate: same tool failure twice, edit in between"
	last.Support = "no classifier needed · switch ≈ 38k uncached tokens"
	last.Reading = &route.Reading{
		Tiers:      map[config.Tier]float64{config.TierMicro: 0.02, config.TierLow: 0.09, config.TierMedium: 0.71, config.TierHigh: 0.18},
		Activity:   config.ActivityDebug,
		ActivityP:  0.72,
		Alternates: []route.Share{{Activity: config.ActivityCode, P: 0.15}, {Activity: config.ActivityExplore, P: 0.06}},
	}

	// 144 routed replies, four per cell.
	strip := make([]config.Tier, 0, 36)
	for _, t := range []struct {
		tier config.Tier
		n    int
	}{
		{config.TierMedium, 9}, {config.TierLow, 5}, {config.TierMedium, 6}, {config.TierMicro, 2},
		{config.TierLow, 4}, {config.TierMedium, 4}, {config.TierHigh, 6},
	} {
		for range t.n {
			strip = append(strip, t.tier)
		}
	}

	return route.Snapshot{
		At:              now,
		Mode:            route.ModeOn,
		Preset:          cfg.Preset,
		Baseline:        solMed,
		Footer:          sol61,
		FooterSynced:    true,
		Last:            &last,
		Recent:          recent,
		Classifier:      classifier.Health{Latency: 412 * time.Millisecond},
		ClassifierLabel: "Jev", ClassifierModel: "jev-1.13.0", ClassifierMedian: 388 * time.Millisecond,
		Routes: cfg.Routes,
		Group:  catalog.Group{CompHash: "9f3c1a7e"},
		Policy: cfg.Effective(),
		Stats: route.Stats{
			Since: at(18, 2), Replies: 144, Switches: 19,
			ByTier:           map[config.Tier]int{config.TierHigh: 24, config.TierMedium: 76, config.TierLow: 36, config.TierMicro: 8},
			ByActivity:       map[config.Activity]int{config.ActivityCode: 53, config.ActivityDebug: 38, config.ActivityExplore: 27, config.ActivityPlan: 9, config.ActivityReview: 7, config.ActivityOps: 6, config.ActivityDocs: 4},
			Strip:            strip,
			RepliesPerCell:   4,
			LastSwitch:       &route.Switch{At: at(18, 42), From: config.TierMedium, To: config.TierHigh, Activity: config.ActivityDebug, MidTurn: true},
			CacheReusePct:    64,
			LastInputTokens:  44_000,
			LastCachedTokens: 42_700,
			// Switches write uncached tokens; this is what pulls reuse down.
			UncachedSwitchTokens: 612_000,
			FitHeld:              3,
			RoutedUSD:            4.12,
			BaselineUSD:          5.30,
		},
		ApproxTokens: 91_000,
		Window:       828_000,
		Worth: []route.Finding{
			{Kind: route.FindingCache, Text: "cache reuse 64% after 19 switches; switches wrote 612K uncached tokens"},
			{Kind: route.FindingContext, Text: "context guard kept 3 steps on a larger window than the policy chose"},
		},
	}
}
