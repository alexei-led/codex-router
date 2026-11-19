package route

import (
	"sync"
	"testing"

	"github.com/alexei-led/codex-router/internal/classifier"
	"github.com/alexei-led/codex-router/internal/config"
	"github.com/alexei-led/codex-router/internal/policy"
	"github.com/alexei-led/codex-router/internal/responses"
)

func TestReadyDowngradeReviewDoesNotWeakenFailureEscalation(t *testing.T) {
	cfg := testConfig()
	cfg.Policy.ReviewEveryRequests = 1
	h := newHarness(t, cfg)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	h.asker.script(answer(tier(config.TierMedium, 0.99)), func() (classifier.Advice, error) {
		once.Do(func() { close(entered) })
		<-release
		return tier(config.TierLow, 0.99), nil
	})
	meta := meta("th", "t1")
	failing := func(id string) []responses.Item {
		return []responses.Item{
			call(id, "go test ./pkg/queue"),
			result(id, 1, "--- FAIL: TestRetry\nretry_test.go:48: attempts = 2, want 3"),
		}
	}

	items := []responses.Item{userMsg(secretPrompt)}
	h.decide(req(medium, items...), meta)
	items = append(items, failing("c1")...)
	h.decide(req(medium, items...), meta)
	<-entered
	items = append(items, editGo("c2"), edited("c2"))
	h.decide(req(medium, items...), meta)
	close(release)
	h.engine.reviews.Wait()
	h.engine.mu.Lock()
	review := h.engine.lookup("th").review
	ready := review != nil && review.ready && review.answer != nil && review.answer.Choice == "low"
	h.engine.mu.Unlock()
	if !ready {
		t.Fatal("background review was not ready with its downgrade advice")
	}

	items = append(items, failing("c3")...)
	rw, ok := h.decide(req(medium, items...), meta)

	wantRoute(t, rw, ok, high)
	h.engine.mu.Lock()
	accepted := h.engine.lookup("th").advice != nil && h.engine.lookup("th").advice.Choice == "low"
	h.engine.mu.Unlock()
	if !accepted {
		t.Fatal("the ready downgrade review was not applied at the failure boundary")
	}
	if d := h.last(); d.Reason != policy.ReasonEscalation || d.Tier != config.TierHigh || !d.MidTurn {
		t.Fatalf("decision = %+v, want a mid-turn escalation from the failing medium route", d)
	}
}
