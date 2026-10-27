package route

import (
	"context"
	"fmt"
	"testing"

	"github.com/alexei-led/codex-router/internal/classifier"
	"github.com/alexei-led/codex-router/internal/config"
	"github.com/alexei-led/codex-router/internal/policy"
	"github.com/alexei-led/codex-router/internal/responses"
)

func repeatedCheck(items []responses.Item, id string) []responses.Item {
	return append(items, call(id+"a", "go test ./..."), result(id+"a", 1, "FAIL retry"),
		editGo(id+"edit"), edited(id+"edit"), call(id+"b", "go test ./..."), result(id+"b", 1, "FAIL retry"))
}

func TestCostHighPhysicalRecovery(t *testing.T) {
	for _, act := range []config.Activity{"", config.ActivityCode, config.ActivityDebug, config.ActivityPlan, config.ActivityReview} {
		t.Run(string(act), func(t *testing.T) {
			cfg := config.Default()
			cfg.Mode = config.ModeCost
			cfg.Policy.ReviewEveryRequests = 0
			h := newHarness(t, cfg)
			a := classifier.Advice{Choice: "high", Probabilities: map[classifier.Choice]float64{"high": 1}}
			if act != "" {
				a = withActivity(a, act, 1)
			}
			h.asker.script(answer(a), answer(continuing(a, 1)), answer(continuing(a, 1)), answer(continuing(a, 1)), answer(continuing(a, 1)))
			native := cfg.Routes[config.TierMedium]
			items := []responses.Item{userMsg(secretPrompt)}
			m := meta("th", "t1")
			h.decide(req(native, items...), m)
			initial := h.last().Route
			if initial.Model != terra {
				t.Fatalf("cost high = %v", initial)
			}
			h.reply(m, terra, 10000, 0)
			items = repeatedCheck(items, "r")
			rw, ok := h.decide(req(native, items...), m)
			want := config.Route{Model: sol, Effort: config.EffortHigh}
			if act == config.ActivityDebug || act == config.ActivityPlan || act == config.ActivityReview {
				want.Effort = config.EffortXHigh
			}
			wantRoute(t, rw, ok, want)
			if d := h.last(); d.Tier != config.TierHigh || d.Reason != policy.ReasonEscalation {
				t.Fatalf("recovery = %+v", d)
			}
			h.engine.mu.Lock()
			until := h.engine.lookup("th").state.HoldUntilTurn
			h.engine.mu.Unlock()
			rw2, ok2 := h.decide(req(native, items...), m)
			if rw2 != rw || ok2 != ok {
				t.Fatal("original request retry changed recovery")
			}
			for i := range 5 {
				items = append(items, call(fmt.Sprintf("read%d", i), "ls"), result(fmt.Sprintf("read%d", i), 0, "ok"))
				rw, ok = h.decide(req(native, items...), m)
				wantRoute(t, rw, ok, want)
			}
			for n := 2; n <= 4; n++ {
				items = append(items, userMsg("continue"))
				m = meta("th", fmt.Sprint("t", n))
				rw, ok = h.decide(req(native, items...), m)
				wantRoute(t, rw, ok, want)
				h.reply(m, sol, 10000, 9000)
			}
			h.engine.mu.Lock()
			got := h.engine.lookup("th").state.HoldUntilTurn
			h.engine.mu.Unlock()
			if got != until {
				t.Fatalf("signature rearmed hold %d -> %d", until, got)
			}
			items = append(items, call("passed", "go test ./..."), result("passed", 0, "ok"))
			h.decide(req(native, items...), m)
			h.reply(m, sol, 10000, 9000)
			items = append(items, userMsg("continue after passing checks"))
			m = meta("th", "t5")
			h.decide(req(native, items...), m)
			if d := h.last(); d.Route != initial {
				t.Fatalf("verified recovery did not return to cost: %+v", d)
			}
			items = repeatedCheck(items, "again")
			rw, ok = h.decide(req(native, items...), m)
			if ok && rw.Model == sol {
				t.Fatal("identical failure signature rearmed physical recovery")
			}
		})
	}
}

func TestModeChangeFencesAnInFlightClassifier(t *testing.T) {
	cfg := config.Default()
	h := newHarness(t, cfg)
	h.asker.script(func() (classifier.Advice, error) {
		h.apply(Command{Op: OpMode, Session: "th", Mode: config.ModeCost})
		return mediumTier, nil
	})
	rw, ok := h.engine.Decide(context.Background(), req(cfg.Routes[cfg.Baseline], userMsg(secretPrompt)), meta("th", "turn"))
	wantUnchanged(t, rw, ok)
	if len(h.sink.Decisions()) != 0 {
		t.Fatal("stale mode advice committed a route")
	}
}
