package route

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/alexei-led/codex-router/internal/classifier"
	"github.com/alexei-led/codex-router/internal/config"
	"github.com/alexei-led/codex-router/internal/policy"
	"github.com/alexei-led/codex-router/internal/responses"
)

var updateBalanced = flag.Bool("update-balanced", false, "rewrite testdata/balanced-*.json from the current engine")

// replayed is the part of a Decision the balanced replay pins: what the engine
// decided and why, without fields added after the golden was recorded.
type replayed struct {
	Thread    string
	Turn      string
	Step      int
	Tier      config.Tier
	Activity  config.Activity
	Route     config.Route
	Original  config.Route
	Rewritten bool
	Reason    policy.Reason
	Why       string
	Support   string
	Estimate  *policy.Estimate
}

// replay drives a seeded mix of turns through two threads: tier and activity
// answers, continuations, later steps with tool failures, edits and passing
// checks, replies with growing context, and gaps that let the cache go cold.
// Codex follows the router's writes, so each request carries the last route.
func replay(t *testing.T, cfg config.Config, seed uint64) ([]replayed, *harness) {
	cfg.Policy.ReviewEveryRequests = 0 // background reviews would race the script
	h := newHarness(t, cfg)
	h.sink.engine = nil // the sink's lock check snapshots per decision: slow, and not under test here
	rng := rand.New(rand.NewPCG(seed, seed))
	tiers := []config.Tier{config.TierMicro, config.TierLow, config.TierMedium, config.TierHigh, "uncertain"}
	acts := append(append([]config.Activity(nil), config.Activities...), config.ActivityUncertain)
	type thread struct {
		route config.Route
		items []responses.Item
		input int
	}
	threads := map[string]*thread{}
	for _, id := range []string{"a", "b"} {
		threads[id] = &thread{route: cfg.RouteFor(cfg.Baseline, "")}
	}
	calls := 0
	for turn := range 90 {
		id := []string{"a", "b"}[rng.IntN(2)]
		th := threads[id]
		choice := tiers[rng.IntN(len(tiers))]
		p := 0.5 + rng.Float64()*0.49
		advice := classifier.Advice{Choice: classifier.Choice(choice), Probabilities: map[classifier.Choice]float64{classifier.Choice(choice): p}}
		if i := rng.IntN(len(config.Tiers)); config.Tiers[i] != choice {
			advice.Probabilities[classifier.Choice(config.Tiers[i])] = 1 - p
		}
		if rng.IntN(4) > 0 {
			advice = withActivity(advice, acts[rng.IntN(len(acts))], 0.3+rng.Float64()*0.69)
		}
		if rng.IntN(2) == 0 {
			advice = continuing(advice, rng.Float64())
		}
		h.asker.script(answer(advice))
		th.items = append(th.items, userMsg(fmt.Sprintf("turn %d", turn)))
		m := meta(id, fmt.Sprintf("%s%d", id, turn))
		for step := range 1 + rng.IntN(4) {
			if step > 0 {
				calls++
				cid := fmt.Sprintf("c%d", calls)
				switch rng.IntN(4) {
				case 0:
					th.items = append(th.items, editGo(cid), edited(cid))
				case 1:
					th.items = append(th.items, call(cid, "go test ./pkg/queue"), result(cid, 1, "--- FAIL: TestRetry\nretry_test.go:48: attempts = 2, want 3"))
				default:
					th.items = append(th.items, call(cid, "go test ./..."), result(cid, 0, "ok"))
				}
			}
			if rw, ok := h.decide(req(th.route, th.items...), m); ok {
				th.route = config.Route{Model: rw.Model, Effort: rw.Effort}
			}
			th.input = 5000 + rng.IntN(120_000)
			h.reply(m, th.route.Model, th.input, th.input*rng.IntN(10)/10)
			h.clock.Advance(time.Duration(5+rng.IntN(400)) * time.Second)
		}
	}
	var out []replayed
	for _, d := range h.sink.Decisions() {
		out = append(out, replayed{
			Thread: d.ThreadID, Turn: d.TurnID, Step: d.Step, Tier: d.Tier, Activity: d.Activity,
			Route: d.Route, Original: d.Original, Rewritten: d.Rewritten, Reason: d.Reason,
			Why: d.Why, Support: d.Support, Estimate: d.Estimate,
		})
	}
	return out, h
}

// firstDiff names the first line where got and want differ.
func firstDiff(got, want []byte) string {
	g, w := bytes.Split(got, []byte("\n")), bytes.Split(want, []byte("\n"))
	for i := range min(len(g), len(w)) {
		if !bytes.Equal(g[i], w[i]) {
			return fmt.Sprintf("line %d: got %s, want %s", i+1, g[i], w[i])
		}
	}
	return fmt.Sprintf("got %d lines, want %d", len(g), len(w))
}

// TestBalancedDecisionsAreUnchanged pins balanced routing to decisions the
// engine made before routing modes existed (recorded on v0.8.0's engine):
// with no mode set and with balanced set in the config, every decision of
// the replay matches the golden byte for byte.
func TestBalancedDecisionsAreUnchanged(t *testing.T) {
	for _, tc := range []struct {
		name string
		cfg  func() config.Config
	}{
		{"defaults", config.Default},
		{"unpriced", testConfig},
	} {
		for _, seed := range []uint64{1, 2, 3} {
			path := filepath.Join("testdata", fmt.Sprintf("balanced-%s-%d.json", tc.name, seed))
			t.Run(filepath.Base(path), func(t *testing.T) {
				t.Parallel()
				decisions, _ := replay(t, tc.cfg(), seed)
				got, err := json.MarshalIndent(decisions, "", " ")
				if err != nil {
					t.Fatal(err)
				}
				if *updateBalanced {
					if err := os.WriteFile(path, append(got, '\n'), 0o600); err != nil {
						t.Fatal(err)
					}
					return
				}
				want, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(append(got, '\n'), want) {
					t.Errorf("balanced decisions differ from %s: %s", path, firstDiff(append(got, '\n'), want))
				}
				explicit := tc.cfg()
				explicit.Mode = config.ModeBalanced
				decisions, _ = replay(t, explicit, seed)
				again, err := json.MarshalIndent(decisions, "", " ")
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(again, got) {
					t.Errorf("mode balanced in the config decides differently from no mode")
				}
			})
		}
	}
}
