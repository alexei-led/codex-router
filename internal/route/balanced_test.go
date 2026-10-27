package route

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"sync"
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
//
// rich adds what the plain mix leaves out: background reviews (each awaited,
// so the order stays fixed), pins, context compaction, a thread seeded by a
// resume before its first request and one seeded mid-run.
func replay(t *testing.T, cfg config.Config, seed uint64, rich bool) ([]replayed, *harness) {
	asker := &replayAsker{gen: rand.New(rand.NewPCG(seed, seed+1))}
	var opts []func(*Deps)
	if rich {
		cfg.Policy.ReviewEveryRequests = 2 // the replay's turns are short
		opts = append(opts, func(d *Deps) { d.Classifier = asker })
	} else {
		cfg.Policy.ReviewEveryRequests = 0 // background reviews would race the script
	}
	h := newHarness(t, cfg, opts...)
	h.sink.engine = nil // the sink's lock check snapshots per decision: slow, and not under test here
	rng := rand.New(rand.NewPCG(seed, seed))
	type thread struct {
		route config.Route
		items []responses.Item
		input int
	}
	threads := map[string]*thread{}
	ids := []string{"a", "b"}
	for _, id := range ids {
		threads[id] = &thread{route: cfg.RouteFor(cfg.Baseline, "")}
	}
	seedThread := func(id string) {
		r := cfg.RouteFor(config.TierHigh, "")
		h.engine.Resumed(id, r)
		threads[id] = &thread{route: r}
		ids = append(ids, id)
	}
	if rich {
		seedThread("c")
	}
	calls := 0
	for turn := range 90 {
		if rich && turn == 45 {
			seedThread("d")
		}
		id := ids[rng.IntN(len(ids))]
		th := threads[id]
		advice := randomAdvice(rng)
		if rich {
			if rng.IntN(10) == 0 {
				h.apply(Command{Op: OpPin, Session: id, Tier: config.Tiers[rng.IntN(len(config.Tiers))]})
			}
			if len(th.items) > 6 && rng.IntN(12) == 0 {
				th.items = slices.Clone(th.items[len(th.items)-2:])
			}
			asker.set(advice)
		} else {
			h.asker.script(answer(advice))
		}
		th.items = append(th.items, userMsg(fmt.Sprintf("turn %d", turn)))
		m := meta(id, fmt.Sprintf("%s%d", id, turn))
		steps := 1 + rng.IntN(4)
		if rich {
			steps += rng.IntN(3)
		}
		for step := range steps {
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
			h.engine.reviews.Wait()
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

// randomAdvice draws a classifier answer: a tier with its probability, maybe
// a second tier, an activity, and a continuation mass.
func randomAdvice(rng *rand.Rand) classifier.Advice {
	tiers := []config.Tier{config.TierMicro, config.TierLow, config.TierMedium, config.TierHigh, "uncertain"}
	acts := append(append([]config.Activity(nil), config.Activities...), config.ActivityUncertain)
	choice := tiers[rng.IntN(len(tiers))]
	p := 0.5 + float64(rng.Float64()*0.49) // conversions stop FMA fusion: same values on every CPU
	advice := classifier.Advice{Choice: classifier.Choice(choice), Probabilities: map[classifier.Choice]float64{classifier.Choice(choice): p}}
	if i := rng.IntN(len(config.Tiers)); config.Tiers[i] != choice {
		advice.Probabilities[classifier.Choice(config.Tiers[i])] = 1 - p
	}
	if rng.IntN(4) > 0 {
		advice = withActivity(advice, acts[rng.IntN(len(acts))], 0.3+float64(rng.Float64()*0.69))
	}
	if rng.IntN(2) == 0 {
		advice = continuing(advice, rng.Float64())
	}
	return advice
}

// replayAsker answers a turn's first ask with the answer the replay set, and
// background reviews with answers drawn from its own generator.
type replayAsker struct {
	mu   sync.Mutex
	next *classifier.Advice
	gen  *rand.Rand
}

func (a *replayAsker) set(advice classifier.Advice) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.next = &advice
}

func (a *replayAsker) Ask(context.Context, classifier.Input) (classifier.Advice, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.next != nil {
		advice := *a.next
		a.next = nil
		return advice, nil
	}
	return randomAdvice(a.gen), nil
}

func (a *replayAsker) Health() classifier.Health { return classifier.Health{} }

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

// legacyConfig freezes the pre-Terra defaults to keep the v0.8 behavior
// oracle useful for generic/custom ladders as shipped preferences evolve.
func legacyConfig() config.Config {
	c := config.Default()
	c.Routes[config.TierMedium] = config.Route{Model: sol, Effort: config.EffortMedium}
	c.Activities = map[config.Activity]map[config.Tier]config.Route{
		config.ActivityDebug: {config.TierMedium: {Model: sol, Effort: config.EffortHigh}},
	}
	delete(c.Prices, terra)
	return c
}

// TestBalancedDecisionsAreUnchanged pins balanced routing to decisions the
// engine made before routing modes existed (recorded on v0.8.0's engine):
// with no mode set and with balanced set in the config, every decision of
// both replays matches the golden byte for byte.
func TestBalancedDecisionsAreUnchanged(t *testing.T) {
	for _, tc := range []struct {
		name string
		cfg  func() config.Config
	}{
		{"defaults", legacyConfig},
		{"unpriced", testConfig},
	} {
		for _, rich := range []bool{false, true} {
			for _, seed := range []uint64{1, 2, 3} {
				name := fmt.Sprintf("balanced-%s-%d", tc.name, seed)
				if rich {
					name += "-rich"
				}
				path := filepath.Join("testdata", name+".json")
				t.Run(name, func(t *testing.T) {
					t.Parallel()
					decisions, _ := replay(t, tc.cfg(), seed, rich)
					got, err := json.MarshalIndent(decisions, "", " ")
					if err != nil {
						t.Fatal(err)
					}
					got = append(got, '\n')
					if *updateBalanced {
						if err := os.WriteFile(path, got, 0o600); err != nil {
							t.Fatal(err)
						}
						return
					}
					want, err := os.ReadFile(path)
					if err != nil {
						t.Fatal(err)
					}
					if !bytes.Equal(got, want) {
						t.Errorf("balanced decisions differ from %s: %s", path, firstDiff(got, want))
					}
					explicit := tc.cfg()
					explicit.Mode = config.ModeBalanced
					decisions, _ = replay(t, explicit, seed, rich)
					again, err := json.MarshalIndent(decisions, "", " ")
					if err != nil {
						t.Fatal(err)
					}
					if !bytes.Equal(append(again, '\n'), got) {
						t.Errorf("mode balanced in the config decides differently from no mode")
					}
				})
			}
		}
	}
}
