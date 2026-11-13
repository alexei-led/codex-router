//go:build live

package route

import (
	"context"
	"os"
	"slices"
	"testing"

	"github.com/alexei-led/codex-router/internal/classifier"
	"github.com/alexei-led/codex-router/internal/config"
)

// TestLiveGoalPhaseChecks sends only these synthetic prompts, never repository
// or session content. It is an opt-in diagnostic, not a representative benchmark.
func TestLiveGoalPhaseChecks(t *testing.T) {
	if os.Getenv("LIVE_GOAL_PHASE") != "1" {
		t.Skip("LIVE_GOAL_PHASE is not 1")
	}
	cfg := config.Default()
	def := cfg.Classifiers["jev"]
	def.EnvFile = os.Getenv("LIVE_ENV_FILE")
	cfg.Classifiers["jev"] = def
	ask, why := classifier.Configured("jev", cfg.Classifiers, os.Getenv)
	if ask == nil {
		t.Fatal(why)
	}
	cases := []struct {
		name, anchor, prompt, prior string
		goal                        config.Activity
		phases                      []classifier.Phase
	}{
		{"bounded validation", "Implement a temperature conversion service", "Only run go test ./... and report the exit status. Do not diagnose or edit anything.", "Implementation is complete; validation is next.", config.ActivityCode, []classifier.Phase{classifier.PhaseVerify}},
		{"bounded commit", "Implement a temperature conversion service", "Only commit the already staged implementation with message: add conversion service. No edits or investigation.", "The implementation and checks are complete.", config.ActivityCode, []classifier.Phase{classifier.PhaseExecute}},
		{"mixed failures", "Implement a temperature conversion service", "Run the tests and correct all failures until they pass.", "The new service is ready for checks.", config.ActivityCode, []classifier.Phase{classifier.PhaseProduce, classifier.PhaseReason}},
		{"status only", "Implement a temperature conversion service", "Only summarize what changed and the completed check results; do not use tools.", "The service and its tests are finished.", config.ActivityCode, []classifier.Phase{classifier.PhaseReport}},
		{"new docs task", "Implement a temperature conversion service", "New unrelated task: write a user-facing README for a chess puzzle collection.", "The service work is complete.", config.ActivityDocs, []classifier.Phase{classifier.PhaseProduce}},
		{"standalone operation", "", "Only deploy the already-built release 7 to staging using ./ship 7. No edits, investigation or design.", "", config.ActivityOps, []classifier.Phase{classifier.PhaseExecute}},
		{"missing brief", "", "Start the new task described in missing-brief.md. Its contents are not available here.", "", config.ActivityUncertain, []classifier.Phase{classifier.PhaseUncertain}},
		{"new operation overrides code", "Implement a temperature conversion service", "New unrelated task: only tag an already reviewed release v2.3.4 with git tag -a v2.3.4. No code, docs or investigation.", "The service work is complete.", config.ActivityOps, []classifier.Phase{classifier.PhaseExecute}},
		{"continued docs validation", "Write a user manual for a pocket calculator", "Only run the specified markdown linter and report the status; no edits.", "The manual text is finished.", config.ActivityDocs, []classifier.Phase{classifier.PhaseVerify}},
		{"continued code investigation", "Implement a temperature conversion service", "Investigate why the new service test fails, then explain the cause without modifying files.", "The implementation is still in progress.", config.ActivityCode, []classifier.Phase{classifier.PhaseReason}},
		{"multilingual mixed work", "Реализовать сервис конвертации температуры", "Продолжай: запусти тесты и исправь найденные ошибки. Не останавливайся на отчёте.", "Реализация готова к первой проверке.", config.ActivityCode, []classifier.Phase{classifier.PhaseProduce, classifier.PhaseReason}},
		{"fresh task with old verification advice", "", "Implement a bounded retry queue with cancellation. Write implementation and regression tests.", "The previous task is complete. Only run its final tests now.", config.ActivityCode, []classifier.Phase{classifier.PhaseProduce}},
		{"short continuation", "Implement a temperature conversion service", "Yes, proceed.", "I will only run go test ./... and report its exit status without investigating failures.", config.ActivityCode, []classifier.Phase{classifier.PhaseVerify}},
	}
	mismatches := 0
	for _, tc := range cases {
		in := classifier.Input{Prompt: tc.prompt, CurrentStep: tc.prompt, GoalAnchor: tc.anchor, Routes: cfg.Routes}
		if tc.prior != "" {
			in.Dialogue = []classifier.Message{{Role: "assistant", Text: tc.prior}}
		}
		a, err := ask.Ask(context.Background(), in)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		goal, phase := config.Activity(""), classifier.Phase("")
		if a.Activity != nil {
			goal = a.Activity.Choice
		}
		if a.Phase != nil {
			phase = a.Phase.Choice
		}
		match := goal == tc.goal && slices.Contains(tc.phases, phase)
		if !match {
			mismatches++
		}
		t.Logf("%s: goal=%s phase=%s expected=%s/%v match=%v", tc.name, goal, phase, tc.goal, tc.phases, match)
	}
	t.Logf("synthetic development/boundary checks: %d/%d matched; not a routing-quality guarantee", len(cases)-mismatches, len(cases))
	if mismatches > 0 {
		t.Errorf("%d goal/phase mismatches", mismatches)
	}
}
