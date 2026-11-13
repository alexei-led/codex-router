package tui

import (
	"strings"
	"testing"

	"github.com/alexei-led/codex-router/internal/classifier"
	"github.com/alexei-led/codex-router/internal/config"
)

func TestPanelSeparatesGoalPhaseAndRouteActivity(t *testing.T) {
	s := fixture()
	s.Last.Reading.Activity = config.ActivityCode
	s.Last.Reading.Phase = classifier.PhaseVerify
	s.Last.EffectiveActivity = config.ActivityOps
	s.Recent = nil
	got := renderPlain(s, TabSessions, 150)
	for _, want := range []string{"goal code", "phase verify", "route activity ops"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in panel:\n%s", want, got)
		}
	}
}
