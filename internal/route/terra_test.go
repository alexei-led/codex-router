package route

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/alexei-led/codex-router/internal/classifier"
	"github.com/alexei-led/codex-router/internal/config"
	"github.com/alexei-led/codex-router/internal/responses"
)

const terra = "gpt-6.1-terra"

func TestTerraDefaultPreferences(t *testing.T) {
	data, err := os.ReadFile("testdata/default-preferences.json")
	if err != nil {
		t.Fatal(err)
	}
	var cells []struct {
		Mode     config.RoutingMode
		Tier     config.Tier
		Activity config.Activity
		Model    string
		Effort   config.Effort
	}
	if err := json.Unmarshal(data, &cells); err != nil {
		t.Fatal(err)
	}
	if len(cells) != 96 {
		t.Fatalf("cells = %d, want 96", len(cells))
	}
	for _, c := range cells {
		t.Run(string(c.Mode)+"/"+string(c.Tier)+"/"+string(c.Activity), func(t *testing.T) {
			cfg := config.Default()
			cfg.Mode = c.Mode
			act := c.Activity
			if act == "generic" {
				act = ""
			}
			want := config.Route{Model: c.Model, Effort: c.Effort}
			if got := target(cfg, c.Mode, c.Tier, act, "", func(string) []config.Effort { return config.Efforts }); got != want {
				t.Errorf("preferred = %v, want %v", got, want)
			}
			h := newHarness(t, cfg)
			a := classifier.Advice{Choice: classifier.Choice(c.Tier), Probabilities: map[classifier.Choice]float64{classifier.Choice(c.Tier): 1}}
			if act != "" {
				a = withActivity(a, act, 1)
			}
			h.asker.script(answer(a))
			original := cfg.Routes[cfg.Baseline]
			body, err := json.Marshal(map[string]any{
				"model": original.Model, "reasoning": map[string]any{"effort": original.Effort},
				"input": []any{map[string]any{"type": "message", "role": "user", "content": []any{map[string]string{"type": "input_text", "text": secretPrompt}}}},
				"store": false,
			})
			if err != nil {
				t.Fatal(err)
			}
			request, err := responses.Parse(body)
			if err != nil {
				t.Fatal(err)
			}
			rw, rewritten := h.decide(request, meta("th", "turn"))
			actual := body
			if rewritten {
				actual, err = request.Apply(rw)
				if err != nil {
					t.Fatal(err)
				}
			}
			parsed, err := responses.Parse(actual)
			if err != nil {
				t.Fatal(err)
			}
			if parsed.Model != want.Model || parsed.Effort != want.Effort {
				t.Errorf("request bytes model/effort = %s/%s, want %v", parsed.Model, parsed.Effort, want)
			}
			d := h.last()
			if d.Route != want {
				t.Errorf("effective = %v (%s), want %v", d.Route, d.Reason, want)
			}
			if want == original {
				wantUnchanged(t, rw, rewritten)
			} else {
				wantRoute(t, rw, rewritten, want)
			}
			if s := h.engine.SnapshotFor("th"); s.Last == nil || s.Last.Route != want {
				t.Errorf("snapshot differs: %+v", s.Last)
			}
			if act == "" {
				if got := h.engine.SnapshotFor("th").Routes[c.Tier]; got != want {
					t.Errorf("preview = %v, want %v", got, want)
				}
			}
		})
	}
}
