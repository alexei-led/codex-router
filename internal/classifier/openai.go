package classifier

import (
	"encoding/json"
	"slices"
)

// OpenAIDecisions is the OpenAI Decisions protocol (POST /v1/decisions): the
// state travels as JSON text in `input`, and the questions come back as named
// answers. The route and the activity are choices, the continuation a
// predicate.
func OpenAIDecisions() Adapter { return openAIDecisions{} }

type openAIDecisions struct{}

type oaChoice struct {
	Value       string `json:"value"`
	Description string `json:"description"`
}

type oaQuestion struct {
	Type         string     `json:"type"`
	Name         string     `json:"name"`
	Instructions string     `json:"instructions"`
	Choices      []oaChoice `json:"choices,omitempty"`
}

type oaRequest struct {
	Model     string       `json:"model"`
	Input     string       `json:"input"`
	Questions []oaQuestion `json:"questions"`
}

func oaChoiceQuestion(name string, ins instructions, opts options) oaQuestion {
	q := oaQuestion{Type: "choice", Name: name, Instructions: ins.text()}
	for _, o := range opts {
		q.Choices = append(q.Choices, oaChoice{Value: o.value, Description: o.text()})
	}
	return q
}

func (openAIDecisions) Build(model string, in Input) ([]byte, error) {
	input, err := encode(stateOf(in), "")
	if err != nil {
		return nil, err
	}
	return encode(oaRequest{
		Model: model,
		Input: string(input),
		Questions: []oaQuestion{
			oaChoiceQuestion("route", routeInstructions, routeOptions),
			{Type: "predicate", Name: "continuation", Instructions: continuationInstructions},
			oaChoiceQuestion("activity", activityInstructions, activityOptions),
		},
	}, "")
}

type oaAnswer struct {
	Name          string          `json:"name"`
	Type          string          `json:"type"`
	Choice        string          `json:"choice"`
	Probabilities json.RawMessage `json:"probabilities"`
	Probability   any             `json:"probability"`
}

// weights reads a probability list into value -> probability. A list is a
// JSON array; null, objects and scalars are not one.
func (a oaAnswer) weights() (map[string]any, bool) {
	var list []struct {
		Value       string `json:"value"`
		Probability any    `json:"probability"`
	}
	if json.Unmarshal(a.Probabilities, &list) != nil || list == nil {
		return nil, false
	}
	m := make(map[string]any, len(list))
	for _, e := range list {
		m[e.Value] = e.Probability
	}
	return m, true
}

func (openAIDecisions) Parse(body []byte) (Advice, error) {
	var envelope struct {
		Answers []json.RawMessage `json:"answers"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil || envelope.Answers == nil {
		return Advice{}, ErrMalformed
	}
	byName := map[string]oaAnswer{}
	for _, raw := range envelope.Answers {
		var a oaAnswer
		if json.Unmarshal(raw, &a) == nil {
			byName[a.Name] = a
		}
	}

	route := byName["route"]
	weights, ok := route.weights()
	if route.Type != "choice" || !ok || !slices.Contains(routeOptions.values(), route.Choice) {
		return Advice{}, ErrMalformed
	}
	advice := Advice{
		Choice:        Choice(route.Choice),
		Probabilities: make(map[Choice]float64, len(routeOptions)),
	}
	for _, v := range routeOptions.values() {
		advice.Probabilities[Choice(v)] = clamp(weights[v])
	}

	if c := byName["continuation"]; c.Type == "predicate" {
		p := clamp(c.Probability)
		advice.Continuation = &p
	}

	if a := byName["activity"]; a.Type == "choice" {
		if weights, ok := a.weights(); ok {
			advice.Activity = activityAdvice(a.Choice, func(v string) any { return weights[v] })
		}
	}
	return advice, nil
}
