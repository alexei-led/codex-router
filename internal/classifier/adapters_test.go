package classifier

import (
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/alexei-led/codex-router/internal/config"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

type routeRequests struct {
	Prompt string    `json:"prompt"`
	Turns  []Message `json:"turns"`
	Bodies struct {
		Jev    string `json:"jev"`
		Ollama string `json:"ollama"`
	} `json:"bodies"`
}

func portedRequests(t *testing.T) routeRequests {
	t.Helper()
	var r routeRequests
	if err := json.Unmarshal(fixture(t, "route-requests.json"), &r); err != nil {
		t.Fatal(err)
	}
	return r
}

var portedRoutes = map[config.Tier]config.Route{
	config.TierMicro:  {Model: "haiku", Effort: config.EffortMedium},
	config.TierLow:    {Model: "haiku", Effort: config.EffortHigh},
	config.TierMedium: {Model: "opus", Effort: config.EffortMedium},
	config.TierHigh:   {Model: "opus", Effort: config.EffortXHigh},
}

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestOptionsCoverEveryTierAndActivity(t *testing.T) {
	var tiers, activities []string
	for _, tier := range config.Tiers {
		tiers = append(tiers, string(tier))
	}
	for _, a := range config.Activities {
		activities = append(activities, string(a))
	}
	if got, want := routeOptions.values(), slices.Concat(tiers, []string{string(ChoiceUncertain)}); !slices.Equal(got, want) {
		t.Errorf("route values = %v, want %v", got, want)
	}
	if got, want := activityOptions.values(), slices.Concat(activities, []string{string(config.ActivityUncertain)}); !slices.Equal(got, want) {
		t.Errorf("activity values = %v, want %v", got, want)
	}
	for _, opt := range activityOptions[:len(activityOptions)-1] {
		if !strings.HasPrefix(opt.Covers, "Produces ") || len(opt.NotFor) == 0 {
			t.Errorf("activity %s: criterion must name what it produces and what it is not for", opt.value)
		}
	}
}

func TestSystemOneBuildKeepsThePortedRouteBodyAndAddsTheActivity(t *testing.T) {
	r := portedRequests(t)
	got, err := SystemOne().Build("jev-1.13.0", Input{Prompt: r.Prompt, Dialogue: r.Turns, Routes: portedRoutes})
	if err != nil {
		t.Fatal(err)
	}
	shared := strings.TrimSuffix(r.Bodies.Jev, "}}") + `,"activity":`
	if !strings.HasPrefix(string(got), shared) {
		t.Fatalf("body diverges from the ported route body:\n got %s\nwant prefix %s", got, shared)
	}

	var body struct {
		Questions struct {
			Activity struct {
				Type         string                     `json:"type"`
				Instructions instructions               `json:"instructions"`
				Criteria     map[string]json.RawMessage `json:"criteria"`
			} `json:"activity"`
		} `json:"questions"`
	}
	if err := json.Unmarshal(got, &body); err != nil {
		t.Fatal(err)
	}
	activity := body.Questions.Activity
	if activity.Type != "choice" || !strings.HasPrefix(activity.Instructions.Question, "Which activity") {
		t.Errorf("activity question = %+v", activity)
	}
	if len(activity.Criteria) != len(activityOptions) {
		t.Errorf("activity criteria = %d entries, want %d", len(activity.Criteria), len(activityOptions))
	}
}

func TestSystemOneBuildSendsAnEmptyDialogueAsAList(t *testing.T) {
	got, err := SystemOne().Build("jev", Input{Prompt: "do x"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), `"recentDialogue":[]`) {
		t.Errorf("body = %s", got)
	}
}

func TestSystemOneParseReadsCloudflareAndBareAnswersAlike(t *testing.T) {
	wrapped := fixture(t, "clef-flash-response.json")
	var envelope struct {
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(wrapped, &envelope); err != nil {
		t.Fatal(err)
	}
	fromEnvelope, err := SystemOne().Parse(wrapped)
	if err != nil {
		t.Fatal(err)
	}
	bare, err := SystemOne().Parse(envelope.Result)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range []Advice{fromEnvelope, bare} {
		if a.Choice != "micro" || a.Probabilities["micro"] != 0.5923 {
			t.Errorf("advice = %+v", a)
		}
		if a.Continuation == nil || *a.Continuation != 0.0164 {
			t.Errorf("continuation = %v, want 0.0164", a.Continuation)
		}
		if a.Activity != nil {
			t.Errorf("activity = %+v, want nil without an activity answer", a.Activity)
		}
	}
}

func TestSystemOneParseRejects(t *testing.T) {
	for name, body := range map[string]string{
		"not json":                  `PRIVATE_PROMPT`,
		"an array":                  `[]`,
		"missing route":             `{"answers":{}}`,
		"wrong type":                `{"answers":{"route":{"type":"score","probabilities":{}}}}`,
		"unknown choice":            `{"answers":{"route":{"type":"choice","choice":"opus","probabilities":{}}}}`,
		"null probabilities":        `{"answers":{"route":{"type":"choice","choice":"medium","probabilities":null}}}`,
		"list probabilities":        `{"answers":{"route":{"type":"choice","choice":"medium","probabilities":[]}}}`,
		"cloudflare error envelope": `{"result":null,"success":false,"errors":[{"code":5007,"message":"x"}]}`,
		"cloudflare without answer": `{"result":{"model":"clef"},"success":true,"errors":[]}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := SystemOne().Parse([]byte(body)); !errors.Is(err, ErrMalformed) {
				t.Errorf("err = %v, want %v", err, ErrMalformed)
			}
		})
	}
}

func TestSystemOneParseDegradesMissingNumbers(t *testing.T) {
	a, err := SystemOne().Parse([]byte(`{"answers":{"route":{"type":"choice","choice":"low","confidence":2,"probabilities":{"low":1,"high":"0.9"}}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if a.Probabilities["low"] != 1 || a.Probabilities["high"] != 0 || len(a.Probabilities) != 5 {
		t.Errorf("advice = %+v", a)
	}
	if a.Continuation != nil {
		t.Errorf("continuation = %v, want nil", *a.Continuation)
	}
}

func TestSystemOneParseReadsTheActivity(t *testing.T) {
	a, err := SystemOne().Parse(fixture(t, "jev-activity-response.json"))
	if err != nil {
		t.Fatal(err)
	}
	if a.Choice != "low" || *a.Continuation != 0.12 {
		t.Errorf("route = %+v", a)
	}
	if a.Activity == nil || a.Activity.Choice != config.ActivityOps || a.Activity.Probabilities[config.ActivityOps] != 0.81 {
		t.Fatalf("activity = %+v", a.Activity)
	}
	if len(a.Activity.Probabilities) != len(activityOptions) {
		t.Errorf("activity probabilities = %v", a.Activity.Probabilities)
	}
}

func TestSystemOneParseTreatsAnUnreadableActivityAsNone(t *testing.T) {
	for name, activity := range map[string]string{
		"missing":                    ``,
		"null":                       `null`,
		"a string":                   `"ops"`,
		"a refusal":                  `{"type":"refusal"}`,
		"an unknown choice":          `{"type":"choice","choice":"deploy","probabilities":{"ops":1}}`,
		"a route value":              `{"type":"choice","choice":"low","probabilities":{"low":1}}`,
		"no probabilities":           `{"type":"choice","choice":"ops"}`,
		"empty probabilities":        `{"type":"choice","choice":"ops","probabilities":{}}`,
		"a null probability":         `{"type":"choice","choice":"ops","probabilities":{"ops":null}}`,
		"a text probability":         `{"type":"choice","choice":"ops","probabilities":{"ops":"0.9"}}`,
		"all-zero probabilities":     `{"type":"choice","choice":"ops","probabilities":{"ops":0,"code":0}}`,
		"no weight on its choice":    `{"type":"choice","choice":"ops","probabilities":{"code":0.9}}`,
		"a zero on its choice":       `{"type":"choice","choice":"ops","probabilities":{"ops":0,"code":0.9}}`,
		"probabilities as a list":    `{"type":"choice","choice":"ops","probabilities":[{"value":"ops","probability":1}]}`,
		"a choice that is no string": `{"type":"choice","choice":7,"probabilities":{"ops":1}}`,
	} {
		t.Run(name, func(t *testing.T) {
			body := `{"answers":{"route":{"type":"choice","choice":"low","probabilities":{"low":0.71}}`
			if activity != "" {
				body += `,"activity":` + activity
			}
			body += `}}`
			a, err := SystemOne().Parse([]byte(body))
			if err != nil {
				t.Fatal(err)
			}
			if a.Choice != "low" || a.Probabilities["low"] != 0.71 || a.Activity != nil {
				t.Errorf("advice = %+v, activity = %+v", a, a.Activity)
			}
		})
	}
}

func TestOllamaBuildMatchesThePortedBody(t *testing.T) {
	r := portedRequests(t)
	got, err := Ollama().Build("qwen3.5:9b", Input{Prompt: r.Prompt, Dialogue: r.Turns, Routes: portedRoutes})
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != r.Bodies.Ollama {
		t.Errorf("body differs:\n got %s\nwant %s", got, r.Bodies.Ollama)
	}
}

func TestOllamaActivityStepReusesTheRoutePrefix(t *testing.T) {
	r := portedRequests(t)
	in := Input{Prompt: r.Prompt, Dialogue: r.Turns}
	route, err := Ollama().Build("qwen3.5:9b", in)
	if err != nil {
		t.Fatal(err)
	}
	step, err := Ollama().(ActivityStep).BuildActivity("qwen3.5:9b", in)
	if err != nil {
		t.Fatal(err)
	}
	routeMessages, routeRest := decodeOllama(t, route)
	stepMessages, stepRest := decodeOllama(t, step)

	if routeMessages[0] != stepMessages[0] {
		t.Errorf("system message differs: %q vs %q", routeMessages[0].Content, stepMessages[0].Content)
	}
	routeText, stepText := routeMessages[1].Content, stepMessages[1].Content
	prefix := routeText[:strings.Index(routeText, "\n\nTask: ")+len("\n\nTask: ")]
	if !strings.HasPrefix(stepText, prefix) || stepText == routeText {
		t.Errorf("activity question does not reuse the state prefix:\n%s", stepText)
	}
	for i, v := range activityOptions.values() {
		if !strings.Contains(stepText, "\n"+string(rune('A'+i))+". "+v+": ") {
			t.Errorf("activity question lacks option %c. %s", 'A'+i, v)
		}
	}
	if !strings.HasSuffix(stepText, "Answer with one letter.") {
		t.Errorf("activity question ends %q", stepText[len(stepText)-30:])
	}
	if routeRest != stepRest {
		t.Errorf("non-message fields differ: %s vs %s", routeRest, stepRest)
	}
}

func decodeOllama(t *testing.T, raw []byte) ([]ollamaMessage, string) {
	t.Helper()
	var body struct {
		Messages []ollamaMessage `json:"messages"`
	}
	var rest map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &rest); err != nil {
		t.Fatal(err)
	}
	delete(rest, "messages")
	others, err := json.Marshal(rest)
	if err != nil {
		t.Fatal(err)
	}
	return body.Messages, string(others)
}

func ollamaAnswer(tokens ...any) []byte {
	var top []map[string]any
	for i := 0; i < len(tokens); i += 2 {
		top = append(top, map[string]any{"token": tokens[i], "logprob": math.Log(tokens[i+1].(float64))})
	}
	body, _ := json.Marshal(map[string]any{"logprobs": []any{map[string]any{"token": "C", "top_logprobs": top}}})
	return body
}

func TestOllamaParseReadsLetterProbabilities(t *testing.T) {
	a, err := Ollama().Parse(fixture(t, "ollama-route-response.json"))
	if err != nil {
		t.Fatal(err)
	}
	if a.Choice != "medium" || a.Probabilities["medium"] <= 0.95 || a.Continuation != nil || a.Activity != nil {
		t.Errorf("advice = %+v", a)
	}
	total := 0.0
	for _, p := range a.Probabilities {
		total += p
	}
	if !near(total, 1) {
		t.Errorf("probabilities sum to %v", total)
	}
}

func TestOllamaParseCountsASpacePrefixedTokenAsTheSameLetter(t *testing.T) {
	a, err := Ollama().Parse(ollamaAnswer("B", 0.3, " B", 0.2, "D", 0.1))
	if err != nil {
		t.Fatal(err)
	}
	if a.Choice != "low" || !near(a.Probabilities["low"], 0.5/0.6) || !near(a.Probabilities["high"], 0.1/0.6) || a.Probabilities["micro"] != 0 {
		t.Errorf("advice = %+v", a)
	}
}

func TestOllamaParseRejects(t *testing.T) {
	for name, body := range map[string][]byte{
		"no logprobs":          []byte(`{"message":{"content":"C"}}`),
		"no candidate letters": ollamaAnswer("The", 0.6, "I", 0.4),
		"a top list string":    []byte(`{"logprobs":[{"token":"C","top_logprobs":"C"}]}`),
		"an empty top list":    []byte(`{"logprobs":[{"top_logprobs":[]}]}`),
		"a null logprob":       []byte(`{"logprobs":[{"top_logprobs":[{"token":"C","logprob":null}]}]}`),
		"not an object":        []byte(`"C"`),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Ollama().Parse(body); !errors.Is(err, ErrMalformed) {
				t.Errorf("err = %v, want %v", err, ErrMalformed)
			}
		})
	}
}

func TestOllamaParseActivityMapsLettersToActivities(t *testing.T) {
	a := Ollama().(ActivityStep).ParseActivity(fixture(t, "ollama-activity-response.json"))
	if a == nil || a.Choice != config.ActivityCode {
		t.Fatalf("activity = %+v", a)
	}
	if len(a.Probabilities) != len(activityOptions) || a.Probabilities[config.ActivityUncertain] <= 0 {
		t.Errorf("probabilities = %v", a.Probabilities)
	}
	if !near(a.Probabilities[config.ActivityCode], 0.87/0.984) {
		t.Errorf("code probability = %v, want A and \" A\" summed over letter mass", a.Probabilities[config.ActivityCode])
	}
}

func TestOllamaParseActivityReadsUnusableAnswersAsNone(t *testing.T) {
	for name, body := range map[string][]byte{
		"no logprobs":           []byte(`{"message":{"content":"A"}}`),
		"only words":            ollamaAnswer("The", 0.9),
		"no mass on its letter": []byte(`{"logprobs":[{"top_logprobs":[{"token":"A","logprob":null}]}]}`),
		"not an object":         []byte(`"A"`),
	} {
		t.Run(name, func(t *testing.T) {
			if a := Ollama().(ActivityStep).ParseActivity(body); a != nil {
				t.Errorf("activity = %+v, want nil", a)
			}
		})
	}
}

func TestOpenAIDecisionsBuildAsksRouteContinuationAndActivity(t *testing.T) {
	got, err := OpenAIDecisions().Build("gpt-6-luna", Input{
		Prompt:   "continue",
		Dialogue: []Message{{Role: "user", Text: "fix tests"}},
		Routes:   portedRoutes,
	})
	if err != nil {
		t.Fatal(err)
	}
	var body struct {
		Model     string       `json:"model"`
		Input     string       `json:"input"`
		Questions []oaQuestion `json:"questions"`
	}
	if err := json.Unmarshal(got, &body); err != nil {
		t.Fatal(err)
	}
	if body.Model != "gpt-6-luna" {
		t.Errorf("model = %q", body.Model)
	}
	if want := `{"currentRequest":{"text":"continue"},"recentDialogue":[{"role":"user","text":"fix tests"}]}`; body.Input != want {
		t.Errorf("input = %s, want %s", body.Input, want)
	}
	want := []struct {
		typ, name, instructions string
		opts                    options
	}{
		{"choice", "route", routeInstructions.text(), routeOptions},
		{"predicate", "continuation", continuationInstructions, nil},
		{"choice", "activity", activityInstructions.text(), activityOptions},
	}
	if len(body.Questions) != len(want) {
		t.Fatalf("questions = %d, want %d", len(body.Questions), len(want))
	}
	for i, w := range want {
		q := body.Questions[i]
		if q.Type != w.typ || q.Name != w.name || q.Instructions != w.instructions || len(q.Choices) != len(w.opts) {
			t.Errorf("question %d = %+v", i, q)
		}
		for j, o := range w.opts {
			if q.Choices[j] != (oaChoice{Value: o.value, Description: o.text()}) {
				t.Errorf("question %s choice %d = %+v", w.name, j, q.Choices[j])
			}
		}
	}
	if d := body.Questions[0].Choices[3].Description; !strings.Contains(d, "Use when: frontier reasoning materially reduces rework.") {
		t.Errorf("high description = %q", d)
	}
	if strings.Contains(string(got), "gpt-6.1-sol") || strings.Contains(string(got), `"route":{`) {
		t.Errorf("body carries route models: %s", got)
	}
}

func TestOpenAIDecisionsBuildSendsAnEmptyDialogueAsAList(t *testing.T) {
	got, err := OpenAIDecisions().Build("m", Input{Prompt: "do x"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), `\"recentDialogue\":[]`) {
		t.Errorf("body = %s", got)
	}
}

func TestOpenAIDecisionsParseReadsRouteAndContinuation(t *testing.T) {
	a, err := OpenAIDecisions().Parse(fixture(t, "openai-decisions-response.json"))
	if err != nil {
		t.Fatal(err)
	}
	if a.Choice != "medium" || a.Probabilities["high"] != 0.18 || a.Probabilities["uncertain"] != 0.02 || len(a.Probabilities) != 5 {
		t.Errorf("advice = %+v", a)
	}
	if a.Continuation == nil || *a.Continuation != 0.03 {
		t.Errorf("continuation = %v, want 0.03", a.Continuation)
	}
	if a.Activity != nil {
		t.Errorf("activity = %+v, want nil without an activity answer", a.Activity)
	}
}

func TestOpenAIDecisionsParseReadsTheActivity(t *testing.T) {
	a, err := OpenAIDecisions().Parse(fixture(t, "openai-decisions-activity-response.json"))
	if err != nil {
		t.Fatal(err)
	}
	if a.Choice != "medium" || a.Activity == nil || a.Activity.Choice != config.ActivityDebug {
		t.Fatalf("advice = %+v, activity = %+v", a, a.Activity)
	}
	if a.Activity.Probabilities[config.ActivityDebug] != 0.72 || a.Activity.Probabilities[config.ActivityDocs] != 0 || len(a.Activity.Probabilities) != len(activityOptions) {
		t.Errorf("activity probabilities = %v", a.Activity.Probabilities)
	}
}

func TestOpenAIDecisionsParseRejects(t *testing.T) {
	for name, body := range map[string]string{
		"not json":                   `PRIVATE_PROMPT`,
		"a refusal":                  `{"answers":[{"type":"refusal","name":"route"}]}`,
		"no answers":                 `{}`,
		"answers as an object":       `{"answers":{"route":{}}}`,
		"no route answer":            `{"answers":[{"type":"predicate","name":"continuation","probability":1}]}`,
		"a choice outside the route": `{"answers":[{"type":"choice","name":"route","choice":"opus","probabilities":[]}]}`,
		"an activity as the route":   `{"answers":[{"type":"choice","name":"route","choice":"ops","probabilities":[]}]}`,
		"probabilities as an object": `{"answers":[{"type":"choice","name":"route","choice":"low","probabilities":{}}]}`,
		"null probabilities":         `{"answers":[{"type":"choice","name":"route","choice":"low","probabilities":null}]}`,
		"a choice that is no string": `{"answers":[{"type":"choice","name":"route","choice":7,"probabilities":[]}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := OpenAIDecisions().Parse([]byte(body)); !errors.Is(err, ErrMalformed) {
				t.Errorf("err = %v, want %v", err, ErrMalformed)
			}
		})
	}
}

func TestOpenAIDecisionsParseDegradesMissingNumbers(t *testing.T) {
	a, err := OpenAIDecisions().Parse([]byte(`{"answers":[{"type":"choice","name":"route","choice":"low","confidence":2,` +
		`"probabilities":[{"value":"low","probability":7},{"value":"high","probability":"0.9"},{"value":"micro","probability":-1}]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if a.Probabilities["low"] != 1 || a.Probabilities["high"] != 0 || a.Probabilities["micro"] != 0 || a.Probabilities["medium"] != 0 || len(a.Probabilities) != 5 {
		t.Errorf("probabilities = %v", a.Probabilities)
	}
	if a.Continuation != nil {
		t.Errorf("continuation = %v, want nil without the predicate", *a.Continuation)
	}
}

func TestOpenAIDecisionsParseTreatsAnUnreadableActivityAsNone(t *testing.T) {
	for name, activity := range map[string]string{
		"missing":                    ``,
		"null":                       `null`,
		"a refusal":                  `{"type":"refusal","name":"activity"}`,
		"an unknown choice":          `{"type":"choice","name":"activity","choice":"deploy","probabilities":[{"value":"ops","probability":1}]}`,
		"a route value":              `{"type":"choice","name":"activity","choice":"low","probabilities":[{"value":"low","probability":1}]}`,
		"no probabilities":           `{"type":"choice","name":"activity","choice":"ops"}`,
		"empty probabilities":        `{"type":"choice","name":"activity","choice":"ops","probabilities":[]}`,
		"a null probability":         `{"type":"choice","name":"activity","choice":"ops","probabilities":[{"value":"ops","probability":null}]}`,
		"a text probability":         `{"type":"choice","name":"activity","choice":"ops","probabilities":[{"value":"ops","probability":"0.9"}]}`,
		"all-zero probabilities":     `{"type":"choice","name":"activity","choice":"ops","probabilities":[{"value":"ops","probability":0},{"value":"code","probability":0}]}`,
		"no weight on its choice":    `{"type":"choice","name":"activity","choice":"ops","probabilities":[{"value":"code","probability":0.9}]}`,
		"a zero on its choice":       `{"type":"choice","name":"activity","choice":"ops","probabilities":[{"value":"ops","probability":0},{"value":"code","probability":0.9}]}`,
		"probabilities as an object": `{"type":"choice","name":"activity","choice":"ops","probabilities":{"ops":1}}`,
	} {
		t.Run(name, func(t *testing.T) {
			body := `{"answers":[{"type":"choice","name":"route","choice":"medium","probabilities":[{"value":"medium","probability":0.7}]},` +
				`{"type":"predicate","name":"continuation","probability":0.03}`
			if activity != "" {
				body += `,` + activity
			}
			body += `]}`
			a, err := OpenAIDecisions().Parse([]byte(body))
			if err != nil {
				t.Fatal(err)
			}
			if a.Choice != "medium" || a.Probabilities["medium"] != 0.7 || a.Activity != nil || a.Continuation == nil || *a.Continuation != 0.03 {
				t.Errorf("advice = %+v, activity = %+v", a, a.Activity)
			}
		})
	}
}
