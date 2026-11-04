package route

import (
	"slices"
	"strings"
	"testing"

	"github.com/alexei-led/codex-router/internal/config"
	"github.com/alexei-led/codex-router/internal/policy"
)

var reasonGlyphs = []string{"↑", "↓", "=", "…", "!", "⏵", "○", "‖", "◐"}

// withDetail are the reasons whose text carries a number or a name from the
// decision, so whyText adds to the shared text.
var withDetail = []policy.Reason{policy.ReasonHold, policy.ReasonFit, policy.ReasonOutOfGroup}

func TestReasonTextsLeadWithAGlyph(t *testing.T) {
	for reason, text := range reasonTexts {
		t.Run(string(reason), func(t *testing.T) {
			if !slices.ContainsFunc(reasonGlyphs, func(g string) bool { return strings.HasPrefix(text, g+" ") }) {
				t.Errorf("text %q does not start with a legend glyph and a space", text)
			}
		})
	}
}

func TestWhyTextSharesTheReasonText(t *testing.T) {
	cfg := config.Default()
	for reason, want := range reasonTexts {
		if slices.Contains(withDetail, reason) {
			continue
		}
		t.Run(string(reason), func(t *testing.T) {
			if got := whyText(Decision{Reason: reason}, cfg, 0); got != want {
				t.Errorf("whyText = %q, want the shared %q", got, want)
			}
			if got, ok := ReasonText(reason); !ok || got != want {
				t.Errorf("ReasonText = %q, %v, want %q", got, ok, want)
			}
		})
	}
}

func TestWhyTextAddsTheDecisionsDetail(t *testing.T) {
	cfg := config.Default()
	tests := []struct {
		name string
		d    Decision
		want string
	}{
		{"hold names the turns", Decision{Reason: policy.ReasonHold, RoutingMode: config.ModeBalanced}, "⏵ hold 2 turns after escalation"},
		{"fit names the context", Decision{Reason: policy.ReasonFit}, "↑ ~90K tokens need a larger window"},
		{"out of group names the model", Decision{Reason: policy.ReasonOutOfGroup, Original: config.Route{Model: "gpt-6.1-sol"}}, "! route outside gpt-6.1-sol's group · request kept"},
		{"activity up names the activity", Decision{Reason: ReasonActivityUp, Activity: config.ActivityDebug}, "↑ debug route stronger"},
		{"activity down names the activity", Decision{Reason: ReasonActivityDown, Activity: config.ActivityDocs}, "↓ docs route cheaper"},
		{"observed names the activity", Decision{Reason: ReasonObserved, Activity: config.ActivityOps}, "= tool calls show ops"},
		{"mode names the mode", Decision{Reason: ReasonMode, RoutingMode: config.ModeCost}, "cost mode route"},
		{"mode pending names the mode", Decision{Reason: ReasonModePending, RoutingMode: config.ModeQuality}, "… quality mode waits: needs more support"},
		{"unknown code falls back to the code", Decision{Reason: "future-reason"}, "future-reason"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := whyText(tc.d, cfg, 90_000); got != tc.want {
				t.Errorf("whyText = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestKiloUsesUpperCaseK(t *testing.T) {
	tests := []struct {
		n    int
		want string
	}{{0, "0"}, {999, "999"}, {1000, "1K"}, {41_400, "41K"}, {212_000, "212K"}}
	for _, tc := range tests {
		if got := kilo(tc.n); got != tc.want {
			t.Errorf("kilo(%d) = %q, want %q", tc.n, got, tc.want)
		}
	}
}

func TestSessionStateText(t *testing.T) {
	tests := []struct {
		name string
		s    SessionInfo
		want string
	}{
		{"on", SessionInfo{Mode: ModeOn}, "● on"},
		{"paused by mode", SessionInfo{Mode: ModePaused}, "‖ paused"},
		{"paused by attention", SessionInfo{Mode: ModeOn, Attention: AttentionPaused}, "‖ paused"},
		{"manual", SessionInfo{Mode: ModeManual, Attention: AttentionManual}, "◐ manual"},
		{"escalated keeps the word", SessionInfo{Mode: ModeOn, Tier: config.TierHigh, Attention: AttentionEscalation}, "↑ escalated"},
		{"state lost", SessionInfo{Attention: AttentionStateLost}, "! state lost"},
		{"unknown", SessionInfo{Attention: AttentionUnknown}, "? unknown"},
		{"a future flag is shown as sent", SessionInfo{Attention: "cache-cold"}, "? cache-cold"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.s.StateText(); got != tc.want {
				t.Errorf("StateText() = %q, want %q", got, tc.want)
			}
		})
	}
}
