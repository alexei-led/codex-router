package tui

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/alexei-led/codex-router/internal/config"
	"github.com/alexei-led/codex-router/internal/policy"
	"github.com/alexei-led/codex-router/internal/route"
)

func decided(turn string, t config.Tier, r policy.Reason, minute int) route.Decision {
	return route.Decision{
		TurnID: turn, Tier: t, Route: config.Route{Model: "m-" + string(t)}, Reason: r,
		Why: string(r), At: time.Date(2026, 10, 15, 17, minute, 0, 0, time.UTC),
	}
}

func TestRecentGroups(t *testing.T) {
	upgrade := decided("t1", config.TierHigh, policy.ReasonUpgrade, 40)
	step := decided("t1", config.TierHigh, route.ReasonContinue, 41)
	escalated := decided("t1", config.TierHigh, policy.ReasonEscalation, 42)
	next := decided("t2", config.TierLow, policy.ReasonDowngrade, 43)
	cases := []struct {
		name   string
		recent []route.Decision
		n      int
		want   []recentGroup
	}{
		{"steps of a turn fold under its own reason", []route.Decision{upgrade, step, step, step}, 5,
			[]recentGroup{{decision: upgrade, steps: 4}}},
		{"newest first across turns", []route.Decision{upgrade, step, next}, 5,
			[]recentGroup{{decision: next, steps: 1}, {decision: upgrade, steps: 2}}},
		{"a route change inside a turn is its own row", []route.Decision{
			decided("t1", config.TierMedium, policy.ReasonSameTier, 40), escalated, step}, 5,
			[]recentGroup{{decision: escalated, steps: 2}, {decision: decided("t1", config.TierMedium, policy.ReasonSameTier, 40), steps: 1}}},
		{"capped at n groups", []route.Decision{upgrade, next, upgrade, next}, 2,
			[]recentGroup{{decision: next, steps: 1}, {decision: upgrade, steps: 1}}},
		{"no turn id never folds", []route.Decision{decided("", config.TierLow, policy.ReasonPinned, 40), decided("", config.TierLow, policy.ReasonPinned, 41)}, 5,
			[]recentGroup{{decision: decided("", config.TierLow, policy.ReasonPinned, 41), steps: 1}, {decision: decided("", config.TierLow, policy.ReasonPinned, 40), steps: 1}}},
		{"empty", nil, 5, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := recentGroups(tc.recent, tc.n)
			if len(got) != len(tc.want) {
				t.Fatalf("groups = %d, want %d: %+v", len(got), len(tc.want), got)
			}
			for i := range got {
				if got[i].steps != tc.want[i].steps || got[i].decision.Reason != tc.want[i].decision.Reason || got[i].decision.At != tc.want[i].decision.At {
					t.Errorf("group %d = %s ×%d at %s, want %s ×%d at %s", i,
						got[i].decision.Reason, got[i].steps, got[i].decision.At.Format("15:04"),
						tc.want[i].decision.Reason, tc.want[i].steps, tc.want[i].decision.At.Format("15:04"))
				}
			}
		})
	}
}

func TestTurnDecision(t *testing.T) {
	head := decided("t1", config.TierHigh, policy.ReasonDowngradePending, 40)
	head.Support = "cheaper tiers 62% (needs 93%)"
	head.SupportParts = []string{"Jev · fast gave cheaper tiers 62% (needs 93%)"}
	step := decided("t1", config.TierHigh, route.ReasonContinue, 41)
	moved := decided("t1", config.TierMedium, route.ReasonContinue, 41)
	cases := []struct {
		name   string
		recent []route.Decision
		last   route.Decision
		want   policy.Reason
		why    string
		parts  []string
	}{
		{"a later step shows its turn's reason", []route.Decision{head, step}, step, policy.ReasonDowngradePending, "cheaper tiers 62% (needs 93%)", head.SupportParts},
		{"a turn's own decision stays", []route.Decision{head}, head, policy.ReasonDowngradePending, "cheaper tiers 62% (needs 93%)", head.SupportParts},
		{"a step on another route keeps its own reason", []route.Decision{head, moved}, moved, route.ReasonContinue, "", nil},
		{"no head in recent keeps the step", []route.Decision{step}, step, route.ReasonContinue, "", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := turnDecision(tc.recent, tc.last)
			if got.Reason != tc.want || got.Support != tc.why {
				t.Errorf("turnDecision = %s %q, want %s %q", got.Reason, got.Support, tc.want, tc.why)
			}
			if !slices.Equal(got.SupportParts, tc.parts) {
				t.Errorf("turnDecision support parts = %q, want %q", got.SupportParts, tc.parts)
			}
			if got.At != tc.last.At || got.Route != tc.last.Route {
				t.Errorf("turnDecision changed the step's time or route: %+v", got)
			}
		})
	}
}

// TestSupportFragments covers both producers: the engine's fragments when the
// snapshot carries them, and an older engine's Support as one whole fragment,
// which may hold the separator inside and is never split.
func TestSupportFragments(t *testing.T) {
	tests := []struct {
		name string
		d    route.Decision
		want []string
	}{
		{"the engine's fragments", route.Decision{
			Support:      "Jev · fast gave high 91% (needs 55%) · switch ≈ 41k uncached tokens",
			SupportParts: []string{"Jev · fast gave high 91% (needs 55%)", "switch ≈ 41k uncached tokens"},
		}, []string{"Jev · fast gave high 91% (needs 55%)", "switch ≈ 41k uncached tokens"}},
		{"an older engine's support stays whole", route.Decision{
			Support: "Jev gave stronger tiers 88% (needs 82%) · switch ≈ 41k uncached tokens",
		}, []string{"Jev gave stronger tiers 88% (needs 82%) · switch ≈ 41k uncached tokens"}},
		{"no evidence", route.Decision{Reason: route.ReasonContinue}, nil},
		{"an empty part list is no evidence", route.Decision{SupportParts: []string{}}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := supportFragments(tt.d); !slices.Equal(got, tt.want) {
				t.Fatalf("supportFragments = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestLegacySupportIsDrawnWhole is the render-level half of the fallback: an
// older engine's separator-joined support reaches the line as one piece, so the
// separator is not taken for the panel's own punctuation.
func TestLegacySupportIsDrawnWhole(t *testing.T) {
	s := fixture()
	s.Last.SupportParts = nil
	got := renderPlain(s, TabSessions, 80)
	if whole := "no classifier needed · switch ≈ 41k uncached tokens · hold 2 turns"; !strings.Contains(got, whole) {
		t.Fatalf("the whole legacy support is not on one line:\n%s", got)
	}
}

// oneLine joins a panel's rows with single spaces, as a wrap replaces the space
// it broke on, so text can be looked for across the lines it wrapped onto.
func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

// segsText is a segment list's text, as the canvas would draw it.
func segsText(segs []seg) string {
	var b strings.Builder
	for _, s := range segs {
		b.WriteString(s.text)
	}
	return b.String()
}

// rowsFit fails when a drawn row is wider than the panel.
func rowsFit(t *testing.T, out string, width int) {
	t.Helper()
	for i, line := range strings.Split(strings.TrimSuffix(out, "\n"), "\n") {
		if cells := ansi.StringWidth(line); cells > width {
			t.Errorf("width %d row %d: %d cells: %q", width, i, cells, line)
		}
	}
}

// TestLegacySupportWrapsWhenTheBandIsNarrow is the fallback at a narrow width:
// an engine older than SupportParts sends the evidence as one string, which at
// 50 and 60 columns is wider than the band. Its tail must wrap onto the
// hanging lines instead of being cut at the edge, and the separator it holds is
// text, not a place the panel splits its fragments.
func TestLegacySupportWrapsWhenTheBandIsNarrow(t *testing.T) {
	const support = "Jev gave stronger tiers 88% (needs 82%) · switch ≈ 41k uncached tokens"
	for _, width := range []int{50, 60, 80} {
		s := fixture()
		s.Last.SupportParts, s.Last.Support = nil, support
		out := renderPlain(s, TabSessions, width)
		if !strings.Contains(oneLine(out), oneLine(support)) {
			t.Errorf("width %d: the legacy support is not whole on the panel:\n%s", width, out)
		}
		rowsFit(t, out, width)
	}
}

// TestOversizedSupportPartsWrapOntoHangingLines is the same for the newer
// producer: a fragment wider than the band wraps onto the hanging lines and
// stays whole. The panel's own separator sits between two fragments on one line;
// a fragment it breaks before starts the next line, as flow has always drawn it.
func TestOversizedSupportPartsWrapOntoHangingLines(t *testing.T) {
	parts := []string{"Jev · fast gave stronger tiers 88% (needs 82%)", "switch ≈ 41k uncached tokens"}
	for _, width := range []int{50, 60, 80} {
		s := fixture()
		s.Last.SupportParts, s.Last.Support = parts, strings.Join(parts, sep)
		out := renderPlain(s, TabSessions, width)
		for _, p := range parts {
			if !strings.Contains(oneLine(out), p) {
				t.Errorf("width %d: the fragment is not whole on the panel:\n%s", width, out)
			}
		}
		rowsFit(t, out, width)
	}
}

// TestSupportWrappingKeepsLongTokensAndGraphemes covers the fragment the panel
// cannot break at a space: it is cut cell-wise, never inside a grapheme, and
// none of it is dropped.
func TestSupportWrappingKeepsLongTokensAndGraphemes(t *testing.T) {
	token := strings.Repeat("z", 30) + "e\u0301" + "👍🏽" + strings.Repeat("z", 30)
	support := "Jev gave stronger tiers 88% (needs 82%) · switch ≈ 41k tokens · 界面 " + token
	s := fixture()
	s.Last.SupportParts, s.Last.Support = nil, support
	out := renderPlain(s, TabSessions, 50)
	joined := strings.ReplaceAll(oneLine(out), " ", "")
	if want := strings.ReplaceAll(oneLine(support), " ", ""); !strings.Contains(joined, want) {
		t.Errorf("the long support lost cells on the panel:\n%s", out)
	}
	for _, whole := range []string{"e\u0301", "👍🏽", "界"} {
		if !strings.Contains(out, whole) {
			t.Errorf("the wrap split %q:\n%s", whole, out)
		}
	}
	rowsFit(t, out, 50)
}

// TestSupportWrappingKeepsALeadingSpaceLabel covers a configured classifier
// label that starts with spaces: the only space the fragment holds before the
// edge sits at column zero, where a break would leave a blank head. The wrap
// must fall back to the cell split there instead of giving up, so the label the
// engine sent reaches the panel whole — from an older engine's Support and from
// the newer SupportParts alike, at 50 and 60 columns.
func TestSupportWrappingKeepsALeadingSpaceLabel(t *testing.T) {
	const letters = 60
	for _, spaces := range []int{1, 3} {
		label := strings.Repeat(" ", spaces) + strings.Repeat("a", letters)
		for _, tc := range []struct {
			name   string
			legacy bool
		}{
			{"an older engine's support", true},
			{"the engine's parts", false},
		} {
			for _, width := range []int{50, 60} {
				s := fixture()
				if tc.legacy {
					s.Last.SupportParts, s.Last.Support = nil, label
				} else {
					s.Last.SupportParts, s.Last.Support = []string{label}, ""
				}
				out := renderPlain(s, TabSessions, width)
				if joined := strings.ReplaceAll(oneLine(out), " ", ""); !strings.Contains(joined, strings.Repeat("a", letters)) {
					t.Errorf("%s, %d leading spaces, width %d: the label lost cells on the panel:\n%s", tc.name, spaces, width, out)
				}
				rowsFit(t, out, width)
			}
		}
	}
}

// TestCutPartNeverLeavesABlankHead pins the wrap's break choice: a space at
// column zero is not a break, because the blank head it leaves stops the part
// from wrapping at all. A space past column zero is a break, a part that only
// starts with spaces and then holds one is still broken at that later space, and
// a token with no space in room is cut cell-wise — never inside a grapheme and
// never losing a cell, so head and tail rejoin into the part.
func TestCutPartNeverLeavesABlankHead(t *testing.T) {
	letters := strings.Repeat("a", 60)
	tests := []struct {
		name       string
		part       string
		room       int
		head, tail string
	}{
		{"a space at column zero is no break", " " + letters, 29, " " + letters[:28], letters[28:]},
		{"several leading spaces are no break either", "   " + letters, 29, "   " + letters[:26], letters[26:]},
		{"a space past column zero still breaks", "Jev fast gave", 8, "Jev fast", "gave"},
		{"a leading space still breaks at the next one", "  alpha beta gamma", 8, "  alpha", "beta gamma"},
		{"a space at the edge is the break", "Jev fast", 3, "Jev", "fast"},
		{"a token with no space is cut cell-wise", strings.Repeat("z", 60), 29, strings.Repeat("z", 29), strings.Repeat("z", 31)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			head, tail := cutPart([]seg{plain(tt.part)}, tt.room)
			if got := segsText(head); got != tt.head || segsText(tail) != tt.tail {
				t.Fatalf("cutPart(%q, %d) = %q, %q; want %q, %q", tt.part, tt.room, got, segsText(tail), tt.head, tt.tail)
			}
			if w := segsWidth(head); w > tt.room {
				t.Errorf("head is %d cells wide, room is %d", w, tt.room)
			}
			if strings.TrimSpace(segsText(head)) == "" {
				t.Errorf("head is blank, so the wrap would not move")
			}
			if strings.ReplaceAll(segsText(head)+segsText(tail), " ", "") != strings.ReplaceAll(tt.part, " ", "") {
				t.Errorf("head and tail lost cells of %q", tt.part)
			}
		})
	}
}

// TestCutPartKeepsGraphemesWhole is the cell split on text a terminal draws as
// wider or combined cells: the break lands on a cell boundary, so neither a wide
// character nor a combining mark is split, and head and tail rejoin into the
// part. The wrap starts a fragment on a line with the hanging room, which is far
// wider than a wide cell on any panel this renders (minWidth is 40), so a room of
// two cells is the narrowest it has to answer for.
func TestCutPartKeepsGraphemesWhole(t *testing.T) {
	for _, part := range []string{
		strings.Repeat("界", 30),
		strings.Repeat("👍🏽", 30),
		"e\u0301" + strings.Repeat("z", 40),
		strings.Repeat("👩‍💻", 20),
	} {
		for room := 2; room < ansi.StringWidth(part); room++ {
			head, tail := cutPart([]seg{plain(part)}, room)
			h, tl := segsText(head), segsText(tail)
			if h == "" || tl == "" {
				t.Fatalf("cutPart(%q, %d) left %q, %q", part, room, h, tl)
			}
			if h+tl != part {
				t.Errorf("cutPart(%q, %d) lost cells: %q + %q", part, room, h, tl)
			}
			if w := ansi.StringWidth(h); w > room {
				t.Errorf("cutPart(%q, %d): head is %d cells wide", part, room, w)
			}
		}
	}
}

// TestSupportWrappingStillBreaksAtSpaces is the ordinary case the leading-space
// fallback must not cost: a fragment of words still wraps at its spaces, so no
// word is cut in two.
func TestSupportWrappingStillBreaksAtSpaces(t *testing.T) {
	support := "Jev gave stronger tiers 88% (needs 82%) · switch ≈ 41k uncached tokens · hold 2 turns"
	words := strings.Fields(strings.ReplaceAll(support, "·", " "))
	for _, width := range []int{50, 60} {
		s := fixture()
		s.Last.SupportParts, s.Last.Support = nil, support
		out := renderPlain(s, TabSessions, width)
		rows := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
		for _, word := range words {
			found := slices.ContainsFunc(rows, func(row string) bool {
				return strings.Contains(" "+row+" ", " "+word+" ")
			})
			if !found {
				t.Errorf("width %d: the wrap cut %q in two:\n%s", width, word, out)
			}
		}
		if !strings.Contains(oneLine(out), oneLine(support)) {
			t.Errorf("width %d: the support is not whole on the panel:\n%s", width, out)
		}
		rowsFit(t, out, width)
	}
}

// TestClassifierOffReasonWraps is the band's other evidence line: the reason the
// classifier is off comes from the engine too and wraps the same way.
func TestClassifierOffReasonWraps(t *testing.T) {
	s := fixture()
	s.ClassifierOff, s.Last = true, nil
	s.ClassifierOffReason = "TYPESAFE_API_KEY is not set; export it before starting codex-router"
	out := renderPlain(s, TabSessions, 50)
	if !strings.Contains(oneLine(out), oneLine(s.ClassifierOffReason)) {
		t.Errorf("the reason is not whole on the panel:\n%s", out)
	}
	rowsFit(t, out, 50)
}

// TestSupportPartsAreDrawnAsFragments is the new producer's half: the panel
// puts its own separator between the fragments and leaves a separator inside
// one of them alone.
func TestSupportPartsAreDrawnAsFragments(t *testing.T) {
	s := fixture()
	s.Last.Support = "Jev · fast gave high 91% · switch ≈ 41k uncached tokens"
	s.Last.SupportParts = []string{"Jev · fast gave high 91%", "switch ≈ 41k uncached tokens"}
	got := renderPlain(s, TabSessions, 80)
	for _, want := range []string{"Jev · fast gave high 91% · switch ≈ 41k uncached tokens", "Jev high 18%"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in\n%s", want, got)
		}
	}
}
