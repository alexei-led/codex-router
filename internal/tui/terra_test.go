package tui

import (
	"strings"
	"testing"

	"github.com/alexei-led/codex-router/internal/config"
)

func TestPolicyRouteColumnsKeepAGapAfterLongModelNames(t *testing.T) {
	s := fixture()
	s.Routes[config.TierMedium] = config.Route{Model: "gpt-6.1-terra", Effort: config.EffortMedium}
	got := render(view{snap: s, tab: TabPolicy, width: 120})
	if strings.Contains(got, "mediumhigh") || !strings.Contains(got, "gpt-6.1-terra · medium  high") {
		t.Fatalf("route columns run together:\n%s", got)
	}
}
