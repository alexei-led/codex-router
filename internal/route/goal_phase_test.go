package route

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/alexei-led/codex-router/internal/classifier"
	"github.com/alexei-led/codex-router/internal/config"
	"github.com/alexei-led/codex-router/internal/responses"
)

type typedAsker struct {
	adapter classifier.Adapter
	answers []string
	bodies  []string
	after   func()
}

func (a *typedAsker) Ask(_ context.Context, in classifier.Input) (classifier.Advice, error) {
	body, err := a.adapter.Build("test", in)
	if err != nil {
		return classifier.Advice{}, err
	}
	a.bodies = append(a.bodies, string(body))
	if len(a.answers) == 0 {
		return classifier.Advice{}, classifier.ErrUnreachable
	}
	reply := a.answers[0]
	a.answers = a.answers[1:]
	advice, err := a.adapter.Parse([]byte(reply))
	if a.after != nil {
		a.after()
	}
	return advice, err
}
func (*typedAsker) Health() classifier.Health { return classifier.Health{} }

func systemAnswer(goal, phase, continuation string) string {
	return fmt.Sprintf(`{"answers":{"route":{"type":"choice","choice":"medium","probabilities":{"medium":0.99}},"continuation":{"type":"noul","noul":%s}%s%s}}`,
		continuation, goal, phase)
}

func TestTypedGoalPhaseControlsRealRoute(t *testing.T) {
	cfg := testConfig()
	cfg.Activities[config.ActivityCode] = map[config.Tier]config.Route{config.TierMedium: debug}
	cfg.Activities[config.ActivityOps] = map[config.Tier]config.Route{config.TierMedium: low}
	goal := `,"activity":{"type":"choice","choice":"code","probabilities":{"code":0.99}}`
	tests := []struct {
		name, phase, goal string
		route             config.Route
		effective         config.Activity
	}{
		{"bounded tests", `,"phase":{"type":"choice","choice":"verify","probabilities":{"verify":0.99}}`, goal, low, config.ActivityOps},
		{"bounded commit", `,"phase":{"type":"choice","choice":"execute","probabilities":{"execute":0.99}}`, goal, low, config.ActivityOps},
		{"tests and fixes", `,"phase":{"type":"choice","choice":"produce","probabilities":{"produce":0.99}}`, goal, debug, config.ActivityCode},
		{"report", `,"phase":{"type":"choice","choice":"report","probabilities":{"report":0.99}}`, goal, debug, config.ActivityCode},
		{"missing phase", "", goal, debug, config.ActivityCode},
		{"low phase confidence", `,"phase":{"type":"choice","choice":"verify","probabilities":{"verify":0.2}}`, goal, debug, config.ActivityCode},
		{"contradictory phase probability", `,"phase":{"type":"choice","choice":"verify","probabilities":{"verify":0.9,"produce":0.9}}`, goal, debug, config.ActivityCode},
		{"invalid phase probability", `,"phase":{"type":"choice","choice":"verify","probabilities":{"verify":1.5}}`, goal, debug, config.ActivityCode},
		{"missing goal", `,"phase":{"type":"choice","choice":"verify","probabilities":{"verify":0.99}}`, "", medium, ""},
		{"invalid goal probability", `,"phase":{"type":"choice","choice":"verify","probabilities":{"verify":0.99}}`, `,"activity":{"type":"choice","choice":"code","probabilities":{"code":1.5}}`, medium, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := &typedAsker{adapter: classifier.SystemOne(), answers: []string{systemAnswer(tt.goal, tt.phase, "0")}}
			h := newHarness(t, cfg, func(d *Deps) { d.Classifier = a })
			h.decide(req(medium, userMsg("Ship the service; run the requested checks")), meta("th", "t1"))
			d := h.last()
			if d.Route != tt.route || d.EffectiveActivity != tt.effective {
				t.Fatalf("route %v effective %q, want %v %q", d.Route, d.EffectiveActivity, tt.route, tt.effective)
			}
			if !strings.Contains(a.bodies[0], `"phase"`) || !strings.Contains(a.bodies[0], `"activity"`) {
				t.Fatalf("missing typed questions: %s", a.bodies[0])
			}
			if tt.name == "bounded tests" && (d.Reading == nil || d.Reading.Activity != config.ActivityCode || d.Reading.Phase != classifier.PhaseVerify) {
				t.Fatalf("goal and phase not preserved: %+v", d.Reading)
			}
		})
	}
}

func goalRequest(t *testing.T, prompts ...string) *responses.Request {
	t.Helper()
	var items []any
	for _, p := range prompts {
		items = append(items, map[string]any{"type": "message", "role": "user", "content": p})
	}
	body, err := json.Marshal(map[string]any{"model": medium.Model, "reasoning": map[string]string{"effort": string(medium.Effort)}, "input": items})
	if err != nil {
		t.Fatal(err)
	}
	r, err := responses.Parse(body)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestGoalAnchorResetsOnUnmeasuredHistoryReplacement(t *testing.T) {
	for _, prompts := range [][]string{
		{"continue"},
		{"Different task", "continue"},
		{"Different task", "more context", "continue"},
	} {
		t.Run(strconv.Itoa(len(prompts)), func(t *testing.T) {
			a := &typedAsker{adapter: classifier.SystemOne(), answers: []string{
				systemAnswer(`,"activity":{"type":"choice","choice":"code","probabilities":{"code":0.99}}`, "", "0"),
				systemAnswer("", "", "0.99"),
			}}
			h := newHarness(t, testConfig(), func(d *Deps) { d.Classifier = a })
			h.decide(goalRequest(t, "Build the service"), meta("th", "t1"))
			h.decide(goalRequest(t, prompts...), meta("th", "t2"))
			if strings.Contains(a.bodies[1], `"activeGoal":`) {
				t.Fatalf("replaced history reused old goal without usage: %s", a.bodies[1])
			}
		})
	}
}

func TestBoundedPhaseCannotBypassSameTaskDownGate(t *testing.T) {
	cfg := testConfig()
	cfg.Activities[config.ActivityCode] = map[config.Tier]config.Route{config.TierMedium: debug}
	cfg.Activities[config.ActivityOps] = map[config.Tier]config.Route{config.TierMedium: low}
	goal := `,"activity":{"type":"choice","choice":"code","probabilities":{"code":0.99}}`
	a := &typedAsker{adapter: classifier.SystemOne(), answers: []string{
		systemAnswer(goal, "", "0"),
		systemAnswer(goal, `,"phase":{"type":"choice","choice":"verify","probabilities":{"verify":0.99}}`, "0.99"),
	}}
	h := newHarness(t, cfg, func(d *Deps) { d.Classifier = a })
	m := meta("th", "t1")
	h.decide(goalRequest(t, "Build the service"), m)
	h.reply(m, debug.Model, 10000, 0)
	h.decide(goalRequest(t, "Build the service", "Only run the requested checks"), meta("th", "t2"))
	d := h.last()
	if d.Route != debug || d.EffectiveActivity != config.ActivityCode || d.Reading.Phase != classifier.PhaseVerify {
		t.Fatalf("phase bypassed dwell/passed-check gate: %+v", d)
	}
}

func TestMissingBriefCannotReuseAnUncertainGoal(t *testing.T) {
	a := &typedAsker{adapter: classifier.SystemOne(), answers: []string{
		systemAnswer("", `,"phase":{"type":"choice","choice":"uncertain","probabilities":{"uncertain":0.99}}`, "0"),
		systemAnswer("", "", "0.99"),
	}}
	h := newHarness(t, testConfig(), func(d *Deps) { d.Classifier = a })
	h.decide(goalRequest(t, "Start the task in the unavailable brief"), meta("th", "t1"))
	h.decide(goalRequest(t, "Start the task in the unavailable brief", "continue"), meta("th", "t2"))
	if strings.Contains(a.bodies[1], `"activeGoal":`) || h.last().EffectiveActivity == config.ActivityOps {
		t.Fatalf("missing evidence manufactured a goal or ops route: %s, %+v", a.bodies[1], h.last())
	}
}

func TestUnclassifiedTurnDropsTheOldGoal(t *testing.T) {
	goal := `,"activity":{"type":"choice","choice":"code","probabilities":{"code":0.99}}`
	a := &typedAsker{adapter: classifier.SystemOne(), answers: []string{systemAnswer(goal, "", "0"), systemAnswer("", "", "0.99")}}
	h := newHarness(t, testConfig(), func(d *Deps) { d.Classifier = a })
	h.decide(goalRequest(t, "Build the service"), meta("th", "t1"))
	cat := h.cat.Current()
	model := cat.Models[medium.Model]
	delete(cat.Models, medium.Model)
	h.cat.Set(cat)
	h.decide(goalRequest(t, "Build the service", "Start another task"), meta("th", "t2"))
	cat.Models[medium.Model] = model
	h.cat.Set(cat)
	h.decide(goalRequest(t, "Build the service", "Start another task", "continue"), meta("th", "t3"))
	if strings.Contains(a.bodies[1], `"activeGoal":`) {
		t.Fatalf("unclassified task retained old goal: %s", a.bodies[1])
	}
}

func TestAmbiguousContinuityDoesNotCreateAnAnchor(t *testing.T) {
	for _, continuation := range []string{"null", "0.5"} {
		t.Run(continuation, func(t *testing.T) {
			goal := `,"activity":{"type":"choice","choice":"code","probabilities":{"code":0.99}}`
			a := &typedAsker{adapter: classifier.SystemOne(), answers: []string{
				systemAnswer(goal, "", "0"), systemAnswer(goal, "", continuation), systemAnswer("", "", "0.99"),
			}}
			h := newHarness(t, testConfig(), func(d *Deps) { d.Classifier = a })
			h.decide(goalRequest(t, "Build the service"), meta("th", "t1"))
			h.decide(goalRequest(t, "Build the service", "Continue or start something else"), meta("th", "t2"))
			h.decide(goalRequest(t, "Build the service", "Continue or start something else", "continue"), meta("th", "t3"))
			if strings.Contains(a.bodies[2], `"activeGoal":`) {
				t.Fatalf("ambiguous continuity created an anchor: %s", a.bodies[2])
			}
		})
	}
}

func TestCancelledNewTurnDropsTheOldAnchor(t *testing.T) {
	goal := `,"activity":{"type":"choice","choice":"code","probabilities":{"code":0.99}}`
	a := &typedAsker{adapter: classifier.SystemOne(), answers: []string{
		systemAnswer(goal, "", "0"), systemAnswer(goal, "", "0"), systemAnswer("", "", "0.99"),
	}}
	h := newHarness(t, testConfig(), func(d *Deps) { d.Classifier = a })
	h.decide(goalRequest(t, "Build the service"), meta("th", "t1"))
	ctx, cancel := context.WithCancel(context.Background())
	a.after = cancel
	h.engine.Decide(ctx, goalRequest(t, "Build the service", "New task"), meta("th", "t2"))
	a.after = nil
	h.decide(goalRequest(t, "Build the service", "New task", "continue"), meta("th", "t3"))
	if strings.Contains(a.bodies[2], `"activeGoal":`) {
		t.Fatalf("cancelled turn reused old goal: %s", a.bodies[2])
	}
}

func TestCommandUseAloneCannotSelectOps(t *testing.T) {
	cfg := testConfig()
	cfg.Activities[config.ActivityOps] = map[config.Tier]config.Route{config.TierMedium: high}
	h := newHarness(t, cfg)
	h.asker.script(answer(withActivity(tier("medium", 1), config.ActivityCode, 1)))
	m := meta("th", "t1")
	items := []responses.Item{userMsg("Build the service")}
	h.decide(req(medium, items...), m)
	items = append(items, call("check", "go test ./..."), result("check", 0, "ok"))
	h.decide(req(medium, items...), m)
	if d := h.last(); d.EffectiveActivity == config.ActivityOps || d.Route == high {
		t.Fatalf("commands inferred ops: %+v", d)
	}
}

func TestGoalAnchorTracksOnlyAcceptedTurnContinuations(t *testing.T) {
	goal := `,"activity":{"type":"choice","choice":"code","probabilities":{"code":0.99}}`
	a := &typedAsker{adapter: classifier.SystemOne(), answers: []string{
		systemAnswer(goal, "", "0"),
		systemAnswer(goal, `,"phase":{"type":"choice","choice":"verify","probabilities":{"verify":0.99}}`, "0.95"),
		systemAnswer(goal, `,"phase":{"type":"choice","choice":"execute","probabilities":{"execute":0.99}}`, "0.95"),
		systemAnswer(goal, `,"phase":{"type":"choice","choice":"report","probabilities":{"report":0.99}}`, "0.95"),
		systemAnswer(`,"activity":{"type":"choice","choice":"docs","probabilities":{"docs":0.99}}`, "", "0"),
		systemAnswer(`,"activity":{"type":"choice","choice":"docs","probabilities":{"docs":0.99}}`, "", "0.95"),
	}}
	h := newHarness(t, testConfig(), func(d *Deps) { d.Classifier = a })
	items := []string{"Build the service", "run the checks", "commit", "report results", "New task: write a guide", "continue"}
	var history []string
	for i, prompt := range items {
		history = append(history, prompt)
		h.decide(goalRequest(t, history...), meta("th", fmt.Sprintf("t%d", i)))
	}
	saved, err := json.Marshal(h.engine.Intent())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(saved), "Build the service") || strings.Contains(string(saved), "write a guide") {
		t.Fatalf("goal text persisted in intent: %s", saved)
	}
	want := []string{"", "Build the service", "Build the service", "Build the service", "Build the service", "New task: write a guide"}
	for i, body := range a.bodies {
		var req struct {
			State struct {
				ActiveGoal string `json:"activeGoal"`
			} `json:"state"`
		}
		if err := json.Unmarshal([]byte(body), &req); err != nil {
			t.Fatal(err)
		}
		if req.State.ActiveGoal != want[i] {
			t.Errorf("turn %d anchor %q, want %q", i, req.State.ActiveGoal, want[i])
		}
	}
}
