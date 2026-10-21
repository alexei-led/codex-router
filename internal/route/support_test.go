package route

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/alexei-led/codex-router/internal/policy"
	"github.com/alexei-led/codex-router/internal/responses"
)

// upgradeWithTax drives a thread to a switch that writes uncached tokens, so
// the step's evidence holds both fragments: the classifier's support for the
// tier and the switch's tax. The last published decision is that step's.
func upgradeWithTax(t *testing.T) *harness {
	t.Helper()
	h := newHarness(t, testConfig())
	h.asker.script(answer(tier("medium", 0.99)), answer(tier("high", 0.99)), answer(tier("high", 0.99)))
	m := meta("th", "t1")
	h.decide(req(medium, userMsg(secretPrompt)), m)
	h.reply(m, medium.Model, 50_000, 0)
	items := []responses.Item{userMsg(secretPrompt), userMsg("big refactor")}
	h.decide(req(medium, items...), meta("th", "t2"))
	rw, ok := h.decide(req(medium, append(items, userMsg("go on"))...), meta("th", "t3"))
	wantRoute(t, rw, ok, high)
	return h
}

// TestDecisionCarriesSupportParts publishes the fragments a panel shows, and
// keeps Support the words it has always spelled.
func TestDecisionCarriesSupportParts(t *testing.T) {
	h := upgradeWithTax(t)
	d := h.last()
	if len(d.SupportParts) != 2 {
		t.Fatalf("SupportParts = %q, want the classifier's support and the switch's tax", d.SupportParts)
	}
	if !strings.HasPrefix(d.SupportParts[0], "Jev gave stronger tiers ") {
		t.Errorf("first fragment = %q, want the classifier's support", d.SupportParts[0])
	}
	if d.SupportParts[1] != "switch ≈ 50k uncached tokens" {
		t.Errorf("second fragment = %q, want the uncached tokens", d.SupportParts[1])
	}
	if want := strings.Join(d.SupportParts, " · "); d.Support != want {
		t.Errorf("Support = %q, want the fragments joined: %q", d.Support, want)
	}
}

// TestNoEvidenceHasNoFragments covers the empty case: a turn the classifier
// never answered keeps Support empty and sends no fragments at all.
func TestNoEvidenceHasNoFragments(t *testing.T) {
	h := newHarness(t, testConfig(), func(d *Deps) { d.Classifier = nil })
	h.decide(req(medium, userMsg(secretPrompt)), meta("th", "t1"))
	d := h.last()
	if d.Reason != policy.ReasonNoClassifier {
		t.Fatalf("reason = %s, want %s", d.Reason, policy.ReasonNoClassifier)
	}
	if d.Support != "" || d.SupportParts != nil {
		t.Errorf("Support = %q, parts = %q; want no evidence", d.Support, d.SupportParts)
	}
}

// TestEmptyHistoryStaysNull keeps the wire's null spelling for a thread that
// has no decisions yet, where copying the history must not turn it into an
// empty list.
func TestEmptyHistoryStaysNull(t *testing.T) {
	h := newHarness(t, testConfig())
	h.engine.thread("th")
	b, err := json.Marshal(h.engine.SnapshotFor("th"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"Last":null`, `"Recent":null`} {
		if !strings.Contains(string(b), want) {
			t.Fatalf("empty thread published %s, want %s", b, want)
		}
	}
}

// TestPublishedDecisionIsCopyIsolated proves a sink that edits the fragments it
// was handed cannot change what the engine keeps and serves next.
func TestPublishedDecisionIsCopyIsolated(t *testing.T) {
	h := upgradeWithTax(t)
	published := h.sink.Decisions()
	for _, d := range published {
		for j := range d.SupportParts {
			d.SupportParts[j] = "tampered"
		}
	}
	s := h.engine.SnapshotFor("th")
	if s.Last == nil {
		t.Fatal("no last decision in the engine's snapshot")
	}
	if len(s.Last.SupportParts) != 2 {
		t.Fatalf("published decision lost its fragments: %q", s.Last.SupportParts)
	}
	for _, d := range append([]Decision{*s.Last}, s.Recent...) {
		if strings.Contains(d.Support, "tampered") || strings.Contains(strings.Join(d.SupportParts, "|"), "tampered") {
			t.Fatalf("a published decision's edit reached the engine: %+v", d)
		}
	}
}

// TestPublishedSnapshotIsCopyIsolated is the same promise for snapshots: their
// decisions carry fragments of their own, so a consumer cannot edit routing
// state through one.
func TestPublishedSnapshotIsCopyIsolated(t *testing.T) {
	h := upgradeWithTax(t)
	snaps := h.sink.Snapshots()
	if len(snaps) == 0 {
		t.Fatal("no snapshot published")
	}
	pub := snaps[len(snaps)-1]
	if pub.Last == nil || len(pub.Last.SupportParts) == 0 {
		t.Fatalf("the published snapshot carries no fragments to edit: %+v", pub.Last)
	}
	pub.Last.SupportParts[0] = "tampered"
	for i := range pub.Recent {
		for j := range pub.Recent[i].SupportParts {
			pub.Recent[i].SupportParts[j] = "tampered"
		}
	}
	s := h.engine.SnapshotFor("th")
	if strings.Contains(s.Last.Support, "tampered") || strings.Contains(strings.Join(s.Last.SupportParts, "|"), "tampered") {
		t.Fatalf("an edit through a published snapshot reached the engine: %+v", s.Last)
	}
	for _, d := range s.Recent {
		if strings.Contains(d.Support, "tampered") || strings.Contains(strings.Join(d.SupportParts, "|"), "tampered") {
			t.Fatalf("an edit through a published snapshot reached the engine: %+v", d)
		}
	}
}
