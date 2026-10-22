//go:build live

package route

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/alexei-led/codex-router/internal/classifier"
	"github.com/alexei-led/codex-router/internal/config"
)

// turnInput and turnOutput size the turn the cost estimate prices.
const turnInput, turnOutput = 50_000.0, 2_000.0

// onceAsker asks the real classifier once per prompt and replays its answer,
// so every mode routes the same advice.
type onceAsker struct {
	real    classifier.Asker
	mu      sync.Mutex
	answers map[string]classifier.Advice
	errs    map[string]error
}

func (a *onceAsker) Ask(ctx context.Context, in classifier.Input) (classifier.Advice, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if adv, ok := a.answers[in.Prompt]; ok || a.errs[in.Prompt] != nil {
		return adv, a.errs[in.Prompt]
	}
	adv, err := a.real.Ask(ctx, in)
	a.answers[in.Prompt], a.errs[in.Prompt] = adv, err
	return adv, err
}

func (a *onceAsker) Health() classifier.Health { return a.real.Health() }

// TestLiveClassifierRoutes sends testdata/live-prompts.json to the configured
// classifier (CODEX_ROUTER_CONFIG) once per prompt and routes each answer as a
// new session's first turn under every mode. It writes a Markdown report to
// LIVE_OUT. Run: go test -tags live ./internal/route -run TestLiveClassifierRoutes -v
func TestLiveClassifierRoutes(t *testing.T) {
	path := os.Getenv("CODEX_ROUTER_CONFIG")
	if path == "" {
		t.Skip("CODEX_ROUTER_CONFIG is not set")
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	real, why := classifier.Configured(cfg.Classifier, cfg.Classifiers, os.Getenv)
	if real == nil {
		t.Fatalf("classifier off: %s", why)
	}
	var prompts []struct{ Tier, Activity, Prompt string }
	b, err := os.ReadFile("testdata/live-prompts.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &prompts); err != nil {
		t.Fatal(err)
	}
	asker := &onceAsker{real: real, answers: map[string]classifier.Advice{}, errs: map[string]error{}}
	var out strings.Builder
	fmt.Fprintf(&out, "Classifier %s (%s).\n\n", cfg.Classifiers[cfg.Classifier].Label, cfg.Classifiers[cfg.Classifier].Model)
	out.WriteString("| # | Expected | Classified (p) | Activity (p) | Match | cost | balanced | quality |\n| --- | --- | --- | --- | --- | --- | --- | --- |\n")
	mismatches := 0
	type mix struct {
		usd     float64
		models  map[string]int
		efforts map[config.Effort]int
	}
	mixes := map[config.RoutingMode]*mix{}
	for _, mode := range config.RoutingModes {
		mixes[mode] = &mix{models: map[string]int{}, efforts: map[config.Effort]int{}}
	}
	for i, p := range prompts {
		routes := map[config.RoutingMode]Decision{}
		for _, mode := range config.RoutingModes {
			c := cfg
			c.Mode = mode
			c.Policy.ReviewEveryRequests = 0
			h := newHarness(t, c, func(d *Deps) { d.Classifier = asker })
			h.decide(req(c.RouteFor(c.Baseline, ""), userMsg(p.Prompt)), meta(fmt.Sprint("th", i), "t1"))
			routes[mode] = h.last()
		}
		adv, aerr := asker.answers[p.Prompt], asker.errs[p.Prompt]
		got, act := string(adv.Choice), ""
		gotP, actP := adv.Probabilities[adv.Choice], 0.0
		if adv.Activity != nil {
			act, actP = string(adv.Activity.Choice), adv.Activity.Probabilities[adv.Activity.Choice]
		}
		if aerr != nil {
			got = "error: " + aerr.Error()
		}
		match := "yes"
		switch {
		case got != p.Tier && act != p.Activity:
			match = "tier, activity"
		case got != p.Tier:
			match = "tier"
		case act != p.Activity:
			match = "activity"
		}
		if match != "yes" {
			mismatches++
		}
		fmt.Fprintf(&out, "| %d | %s / %s | %s (%.2f) | %s (%.2f) | %s |", i+1, p.Tier, p.Activity, got, gotP, act, actP, match)
		for _, mode := range config.RoutingModes {
			r := routes[mode].Route
			m, price := mixes[mode], cfg.Prices[r.Model]
			m.usd += (turnInput*price.Input + turnOutput*price.Output) / 1e6
			m.models[strings.TrimPrefix(r.Model, "gpt-")]++
			m.efforts[r.Effort]++
			fmt.Fprintf(&out, " %s/%s |", strings.TrimPrefix(r.Model, "gpt-"), r.Effort)
		}
		out.WriteString("\n")
	}
	fmt.Fprintf(&out, "\n%d of %d prompts classified other than expected.\n\n", mismatches, len(prompts))
	fmt.Fprintf(&out, "Estimated cost assumes one uncached turn per prompt of %d input and %d output tokens at list prices; reasoning tokens are not modeled.\n\n", int(turnInput), int(turnOutput))
	out.WriteString("| Mode | Est. cost | Models | Efforts |\n| --- | --- | --- | --- |\n")
	for _, mode := range config.RoutingModes {
		m := mixes[mode]
		fmt.Fprintf(&out, "| %s | $%.3f | %v | %v |\n", mode, m.usd, m.models, m.efforts)
	}
	t.Log("\n" + out.String())
	if dst := os.Getenv("LIVE_OUT"); dst != "" {
		if err := os.WriteFile(dst, []byte(out.String()), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}
