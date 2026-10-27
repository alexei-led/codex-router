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
			if strings.Contains(line, "micro") && strings.Contains(line, "gpt-6-luna") {
				left = strings.LastIndex(line, "low    ")
			}
			if strings.Contains(line, "medium") && strings.Contains(line, "gpt-6.1-terra") {
				right = strings.Index(line, "high   ")
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
	if strings.Contains(got, "mediumhigh") || !strings.Contains(got, "gpt-6.1-terra · medium  high") {
		t.Fatalf("route columns run together:\n%s", got)
	}
}
