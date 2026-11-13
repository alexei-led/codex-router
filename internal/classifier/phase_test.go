package classifier

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestTypedRequestsAndOptionalPhaseAnswers(t *testing.T) {
	tests := []struct {
		name    string
		adapter Adapter
		answer  func(string) string
		phase   func(string) string
	}{
		{"system-one", SystemOne(), func(p string) string {
			return `{"answers":{"route":{"type":"choice","choice":"low","probabilities":{"low":0.9}}` + p + `}}`
		}, func(p string) string {
			return `,"phase":{"type":"choice","choice":"verify","probabilities":{"verify":` + p + `}}`
		}},
		{"openai", OpenAIDecisions(), func(p string) string {
			return `{"answers":[{"type":"choice","name":"route","choice":"low","probabilities":[{"value":"low","probability":0.9}]}` + p + `]}`
		}, func(p string) string {
			return `,{"type":"choice","name":"phase","choice":"verify","probabilities":[{"value":"verify","probability":` + p + `}]}`
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body, err := tt.adapter.Build("model", Input{Prompt: "continue", CurrentStep: "Implement the remaining endpoint", GoalAnchor: "Build the service"})
			if err != nil {
				t.Fatal(err)
			}
			var req struct {
				State state
				Input string
			}
			if err := json.Unmarshal(body, &req); err != nil {
				t.Fatal(err)
			}
			if req.Input != "" {
				if err := json.Unmarshal([]byte(req.Input), &req.State); err != nil {
					t.Fatal(err)
				}
			}
			if req.State.ActiveGoal != "Build the service" || req.State.CurrentStep != "Implement the remaining endpoint" || !strings.Contains(string(body), `"phase"`) {
				t.Fatalf("request lacks independent goal and phase: %s", body)
			}
			for _, p := range []string{"0.9", "0", "-0.1", "1.1", "null", `"0.9"`} {
				a, err := tt.adapter.Parse([]byte(tt.answer(tt.phase(p))))
				if err != nil {
					t.Fatalf("%s: %v", p, err)
				}
				if (a.Phase != nil) != (p == "0.9") {
					t.Errorf("probability %s: phase = %+v", p, a.Phase)
				}
			}
			a, err := tt.adapter.Parse([]byte(tt.answer("")))
			if err != nil || a.Phase != nil {
				t.Errorf("missing phase: %+v, %v", a.Phase, err)
			}
		})
	}
}

func TestTypedPhaseRejectsContradictoryDistributions(t *testing.T) {
	bodies := []struct {
		adapter Adapter
		body    string
	}{
		{SystemOne(), `{"answers":{"route":{"type":"choice","choice":"low","probabilities":{"low":1}},"phase":{"type":"choice","choice":"verify","probabilities":{"verify":0.9,"produce":0.9}}}}`},
		{OpenAIDecisions(), `{"answers":[{"type":"choice","name":"route","choice":"low","probabilities":[{"value":"low","probability":1}]},{"type":"choice","name":"phase","choice":"verify","probabilities":[{"value":"verify","probability":0.9},{"value":"produce","probability":0.9}]}]}`},
	}
	for _, tt := range bodies {
		a, err := tt.adapter.Parse([]byte(tt.body))
		if err != nil || a.Phase != nil {
			t.Errorf("contradictory phase = %+v, %v", a.Phase, err)
		}
	}
}

func TestUnknownAndDuplicateWeightsCannotSupportPhaseRouting(t *testing.T) {
	tests := []struct {
		name        string
		adapter     Adapter
		body        string
		invalidGoal bool
	}{
		{"system-one unknown goal", SystemOne(), `{"answers":{"route":{"type":"choice","choice":"medium","probabilities":{"medium":1}},"activity":{"type":"choice","choice":"code","probabilities":{"code":0.99,"other":0.01}}}}`, true},
		{"openai unknown goal", OpenAIDecisions(), `{"answers":[{"type":"choice","name":"route","choice":"medium","probabilities":[{"value":"medium","probability":1}]},{"type":"choice","name":"activity","choice":"code","probabilities":[{"value":"code","probability":0.99},{"value":"other","probability":0.01}]}]}`, true},
		{"openai duplicate goal", OpenAIDecisions(), `{"answers":[{"type":"choice","name":"route","choice":"medium","probabilities":[{"value":"medium","probability":1}]},{"type":"choice","name":"activity","choice":"code","probabilities":[{"value":"code","probability":0.99},{"value":"code","probability":0.99}]}]}`, true},
		{"openai duplicate phase", OpenAIDecisions(), `{"answers":[{"type":"choice","name":"route","choice":"medium","probabilities":[{"value":"medium","probability":1}]},{"type":"choice","name":"phase","choice":"verify","probabilities":[{"value":"verify","probability":0.99},{"value":"verify","probability":0.99}]}]}`, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a, err := tt.adapter.Parse([]byte(tt.body))
			if err != nil {
				t.Fatal(err)
			}
			if tt.invalidGoal && a.Activity != nil {
				t.Fatalf("invalid goal accepted: %+v", a.Activity)
			}
			if !tt.invalidGoal && a.Phase != nil {
				t.Fatalf("invalid phase accepted: %+v", a.Phase)
			}
		})
	}
}

func TestInvalidGoalWeightsCannotSupportAnOpsPhase(t *testing.T) {
	for _, weights := range []string{`{"code":0.9,"ops":0.9}`, `{"code":0.9,"debug":-1}`, `{"code":1.1}`} {
		body := fmt.Sprintf(`{"answers":{"route":{"type":"choice","choice":"medium","probabilities":{"medium":1}},"activity":{"type":"choice","choice":"code","probabilities":%s},"phase":{"type":"choice","choice":"verify","probabilities":{"verify":0.99}}}}`, weights)
		a, err := SystemOne().Parse([]byte(body))
		if err != nil || a.Activity != nil {
			t.Errorf("invalid goal weights %s: %+v, %v", weights, a.Activity, err)
		}
	}
}

func TestPhaseRejectsMalformedWeights(t *testing.T) {
	for _, p := range []string{`{}`, `{"verify":0.9,"execute":-1}`, `{"verify":0.9,"produce":0.9}`, `{"verify":0.9,"unexpected":0.2}`, `[]`, `null`} {
		body := fmt.Sprintf(`{"answers":{"route":{"type":"choice","choice":"low","probabilities":{"low":1}},"phase":{"type":"choice","choice":"verify","probabilities":%s}}}`, p)
		a, err := SystemOne().Parse([]byte(body))
		if err != nil || a.Phase != nil {
			t.Errorf("%s: phase %+v, err %v", p, a.Phase, err)
		}
	}
}
