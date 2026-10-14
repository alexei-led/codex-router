// Command render-docs draws the pictures in docs/images from the real TUI
// renderer and a sample snapshot. Run it from the repository root after a UI
// change:
//
//	go run ./scripts/render-docs
//
// It writes one SVG per TUI tab and for the Codex footer capture, and a PNG
// beside each when rsvg-convert is on PATH. The data is made up; it matches
// the debugging session in docs/ui-selected.html.
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
	"github.com/alexei-led/codex-router/internal/policy"
	"github.com/alexei-led/codex-router/internal/route"
	"github.com/alexei-led/codex-router/internal/tui"
)

const panelWidth = 100

var tabDescriptions = map[tui.Tab]string{
	tui.TabNow:        "Now tab: the next turn's tier and model, the activity and why, the pin buttons with classifier support, the session strip of routed replies by tier, worth-a-look findings, the Codex footer sync, context and cache.",
	tui.TabRoutes:     "Routes tab: the model and effort of each tier, models blocked by the compatibility group, and the most recent routing decisions.",
	tui.TabPolicy:     "Policy tab: the preset buttons and the switching thresholds in effect.",
	tui.TabClassifier: "Classifier tab: classifier name, last and median latency, consecutive failures.",
	tui.TabUsage:      "Usage tab: requests, switches, cache reuse, list-price estimate against the baseline model, and counts by tier and activity.",
}

func main() {
	out := flag.String("out", "docs/images", "output directory")
	footer := flag.String("footer", "docs/images/src/codex-footer.txt", "plain-text Codex footer capture")
	flag.Parse()
	if err := render(*out, *footer); err != nil {
		fmt.Fprintln(os.Stderr, "render-docs:", err)
		os.Exit(1)
	}
}

func render(out, footerPath string) error {
	if err := os.MkdirAll(out, 0o755); err != nil {
		return err
	}
	snap := sampleSnapshot()
	for _, tab := range tui.Tabs {
		name := "router-tui-" + string(tab)
		svg := toSVG(parseANSI(tui.Render(snap, tab, panelWidth)), "codex-router panel, "+string(tab)+" tab", tabDescriptions[tab])
		if err := write(out, name, svg); err != nil {
			return err
		}
	}
	text, err := os.ReadFile(footerPath)
	if err != nil {
		return err
	}
	lines := parseANSI(strings.TrimRight(string(text), "\n ") + "\n")
	const footerDesc = "The Codex terminal UI while a turn runs. The footer line shows the model the router chose for the turn, GPT-6-Sol with medium effort, next to the branch, the context window and the remaining context."
	return write(out, "codex-footer", toSVG(lines, "Codex footer showing the routed model", footerDesc))
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

func sampleSnapshot() route.Snapshot {
	cfg := config.Default()
	now := at(18, 43)
	pin := config.TierHigh
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
		step(18, 41, config.TierHigh, config.ActivityDebug, sol61, policy.ReasonEscalation),
		step(18, 41, config.TierHigh, config.ActivityDebug, sol61, policy.ReasonHold),
		step(18, 42, config.TierHigh, config.ActivityDebug, sol61, policy.ReasonDowngradePending),
	}
	last := recent[len(recent)-1]
	last.Original = solMed
	last.Rewritten = true
	last.Why = "waiting to go down: same task, no passed check since the last switch"
	last.Support = "Jev gave cheaper tiers 62% (needs 90%)"
	last.Reading = &route.Reading{
		Tiers:      map[config.Tier]float64{config.TierMicro: 0.02, config.TierLow: 0.09, config.TierMedium: 0.71, config.TierHigh: 0.18},
		Activity:   config.ActivityDebug,
		ActivityP:  0.72,
		Alternates: []route.Share{{Activity: config.ActivityCode, P: 0.15}, {Activity: config.ActivityExplore, P: 0.06}},
	}

	// 142 routed replies, four per cell.
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
		Pin:             &pin,
		Baseline:        solMed,
		Footer:          sol61,
		FooterSynced:    true,
		Last:            &last,
		Recent:          recent,
		Classifier:      classifier.Health{Latency: 412 * time.Millisecond},
		ClassifierLabel: "Jev", ClassifierMedian: 388 * time.Millisecond,
		Routes: cfg.Routes,
		Group:  catalog.Group{CompHash: "9f3c1a7e"},
		Policy: cfg.Effective(),
		Stats: route.Stats{
			Since: at(18, 2), Replies: 142, Switches: 19,
			ByTier:           map[config.Tier]int{config.TierHigh: 31, config.TierMedium: 74, config.TierLow: 29, config.TierMicro: 8},
			ByActivity:       map[config.Activity]int{config.ActivityCode: 51, config.ActivityDebug: 38, config.ActivityExplore: 27, config.ActivityPlan: 9, config.ActivityReview: 7, config.ActivityOps: 6, config.ActivityDocs: 4},
			Strip:            strip,
			RepliesPerCell:   4,
			LastSwitch:       &route.Switch{At: at(18, 41), From: config.TierMedium, To: config.TierHigh, FromRoute: solHigh, ToRoute: sol61, Activity: config.ActivityDebug, MidTurn: true},
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
