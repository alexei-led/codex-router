package route

import (
	"testing"

	"github.com/alexei-led/codex-router/internal/config"
	"github.com/alexei-led/codex-router/internal/responses"
)

func TestPassThroughCompactionClearsAwaitingAndKeepsLaterRoute(t *testing.T) {
	h := newHarness(t, testConfig())
	if err := h.engine.Apply(Command{Op: OpPin, Session: "th", Tier: config.TierLow}); err != nil {
		t.Fatal(err)
	}

	m := meta("th", "t1")
	first, firstOK := h.decide(req(medium, userMsg(secretPrompt)), m)
	wantRoute(t, first, firstOK, low)

	compact := compaction("th", "t1")
	h.engine.PassThrough(compact)
	h.reply(compact, medium.Model, 1000, 0)
	if got := h.engine.SnapshotFor("th").Stats.Replies; got != 0 {
		t.Fatalf("compaction reply counted as routed: replies = %d, want 0", got)
	}

	later := req(medium, userMsg(secretPrompt), call("c1", "ls"), result("c1", 0, "a.go"))
	rw, ok := h.decide(later, m)
	wantRoute(t, rw, ok, low)
	h.reply(m, rw.Model, 1000, 0)
	if got := h.engine.SnapshotFor("th").Stats.Replies; got != 1 {
		t.Fatalf("later user-step replies = %d, want 1", got)
	}
	if len(h.sink.Decisions()) != 2 {
		t.Fatalf("decisions = %d, want first and later user-step decisions", len(h.sink.Decisions()))
	}
}

func TestPassThroughDoesNotClearUserStepDecision(t *testing.T) {
	h := newHarness(t, testConfig())
	if err := h.engine.Apply(Command{Op: OpPin, Session: "th", Tier: config.TierLow}); err != nil {
		t.Fatal(err)
	}
	m := meta("th", "t1")
	rw, ok := h.decide(req(medium, userMsg(secretPrompt)), m)
	wantRoute(t, rw, ok, low)

	h.engine.PassThrough(m)
	h.reply(m, rw.Model, 1000, 0)
	if got := h.engine.SnapshotFor("th").Stats.Replies; got != 1 {
		t.Fatalf("user-step reply count = %d, want 1", got)
	}
	if len(h.sink.Decisions()) != 1 {
		t.Fatalf("decisions = %d, want 1", len(h.sink.Decisions()))
	}
	if responses.Classify(m) != responses.KindUserStep {
		t.Fatalf("test metadata is not a user step: %+v", m)
	}
}
