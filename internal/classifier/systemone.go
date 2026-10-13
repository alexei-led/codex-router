package classifier

import (
	"encoding/json"
	"slices"

	"github.com/alexei-led/codex-router/internal/config"
)

// SystemOne is the typed-questions protocol of Jev and Clef: the route and the
// activity as choices, the continuation as a noul, all in one request.
// Cloudflare wraps the answer in `result`; Parse reads both shapes.
func SystemOne() Adapter { return systemOne{} }

type systemOne struct{}

type soChoiceQuestion struct {
	Type         string       `json:"type"`
	Instructions instructions `json:"instructions"`
	Criteria     options      `json:"criteria"`
}

type soNoulQuestion struct {
	Type         string `json:"type"`
	Instructions string `json:"instructions"`
}

type soRequest struct {
	Model     string `json:"model"`
	State     state  `json:"state"`
	Questions struct {
		Route        soChoiceQuestion `json:"route"`
		Continuation soNoulQuestion   `json:"continuation"`
		Activity     soChoiceQuestion `json:"activity"`
	} `json:"questions"`
}

func (systemOne) Build(model string, in Input) ([]byte, error) {
	criteria := slices.Clone(routeOptions)
	for i := range criteria {
		if r, ok := in.Routes[config.Tier(criteria[i].value)]; ok {
			criteria[i].Route = &r
		}
	}
	req := soRequest{Model: model, State: stateOf(in)}
	req.Questions.Route = soChoiceQuestion{Type: "choice", Instructions: routeInstructions, Criteria: criteria}
	req.Questions.Continuation = soNoulQuestion{Type: "noul", Instructions: continuationInstructions}
	req.Questions.Activity = soChoiceQuestion{Type: "choice", Instructions: activityInstructions, Criteria: activityOptions}
	return encode(req, "")
}

type soChoiceAnswer struct {
	Type          string          `json:"type"`
	Choice        string          `json:"choice"`
	Confidence    any             `json:"confidence"`
	Probabilities json.RawMessage `json:"probabilities"`
}

// record reads raw as a JSON object; null, arrays and scalars are not one.
func record(raw json.RawMessage) (map[string]any, bool) {
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil || m == nil {
		return nil, false
	}
	return m, true
}

func (systemOne) Parse(body []byte) (Advice, error) {
	var envelope struct {
		Answers map[string]json.RawMessage `json:"answers"`
		Result  *struct {
			Answers map[string]json.RawMessage `json:"answers"`
		} `json:"result"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return Advice{}, ErrMalformed
	}
	answers := envelope.Answers
	if answers == nil && envelope.Result != nil {
		answers = envelope.Result.Answers
	}

	var route soChoiceAnswer
	if err := json.Unmarshal(answers["route"], &route); err != nil || route.Type != "choice" {
		return Advice{}, ErrMalformed
	}
	weights, ok := record(route.Probabilities)
	if !ok || !slices.Contains(routeOptions.values(), route.Choice) {
		return Advice{}, ErrMalformed
	}
	advice := Advice{
		Choice:        Choice(route.Choice),
		Confidence:    clamp(route.Confidence),
		Probabilities: make(map[Choice]float64, len(routeOptions)),
	}
	for _, v := range routeOptions.values() {
		advice.Probabilities[Choice(v)] = clamp(weights[v])
	}

	var continuation struct {
		Type string `json:"type"`
		Noul any    `json:"noul"`
	}
	if json.Unmarshal(answers["continuation"], &continuation) == nil && continuation.Type == "noul" {
		c := clamp(continuation.Noul)
		advice.Continuation = &c
	}

	var activity soChoiceAnswer
	if json.Unmarshal(answers["activity"], &activity) == nil && activity.Type == "choice" {
		if weights, ok := record(activity.Probabilities); ok {
			advice.Activity = activityAdvice(activity.Choice, func(v string) any { return weights[v] })
		}
	}
	return advice, nil
}
