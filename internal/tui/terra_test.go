package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/alexei-led/codex-router/internal/config"
)

func TestPolicyRouteColumnsUseOneSharedWidth(t *testing.T) {
	for _, styled := range []bool{false, true} {
		s := fixture()
		s.Routes[config.TierMedium] = config.Route{Model: "gpt-6.1-terra", Effort: config.EffortMedium}
		got := ansi.Strip(render(view{snap: s, tab: TabPolicy, width: 120, styled: styled}))
		left, right := -1, -1
		for line := range strings.Lines(got) {
			if strings.Contains(line, "▂ ") && strings.Contains(line, "gpt-6-luna") {
				left = column(line, strings.LastIndex(line, "▂▄     "))
			}
			if strings.Contains(line, "▂▄▆ ") && strings.Contains(line, "gpt-6.1-terra") {
				right = column(line, strings.Index(line, "▂▄▆█   "))
			}
		}
		if left < 0 || right < 0 || left != right {
			t.Fatalf("styled=%v second column offsets %d/%d:\\n%s", styled, left, right, got)
		}
	}
}

func TestPolicyRouteColumnsKeepAGapAfterLongModelNames(t *testing.T) {
	s := fixture()
	s.Routes[config.TierMedium] = config.Route{Model: "gpt-6.1-terra", Effort: config.EffortMedium}
	got := render(view{snap: s, tab: TabPolicy, width: 120})
	if strings.Contains(got, "medium▂▄▆█") || !strings.Contains(got, "gpt-6.1-terra · medium  ▂▄▆█") {
		t.Fatalf("route columns run together:\n%s", got)
	}
}

// column is the display column of byte offset i in line, or -1 for no match;
// the tier meters are three bytes a cell.
func column(line string, i int) int {
	if i < 0 {
		return -1
	}
	return ansi.StringWidth(line[:i])
}
