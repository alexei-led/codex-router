package responses

import (
	"bytes"
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/alexei-led/codex-router/internal/config"
)

// Body keys the router reads or rewrites. Keys match exactly, as the
// upstream reads them; encoding/json alone would also match "Model".
const (
	keyModel     = "model"
	keyReasoning = "reasoning"
	keyEffort    = "effort"
	keyInput     = "input"
)

// Input item types the router decodes. Other types keep only Type.
const (
	TypeMessage              = "message"
	TypeFunctionCall         = "function_call"
	TypeFunctionCallOutput   = "function_call_output"
	TypeCustomToolCall       = "custom_tool_call"
	TypeCustomToolCallOutput = "custom_tool_call_output"
	TypeLocalShellCall       = "local_shell_call"
	TypeReasoning            = "reasoning"
)

// Parse reads model, reasoning.effort and input items from a Responses
// request body. The body must be a JSON object with a string model.
func Parse(body []byte) (*Request, error) {
	if !json.Valid(body) {
		return nil, errors.New("responses: body is not valid JSON")
	}
	top, err := members(body, skipSpace(body, 0))
	if err != nil {
		return nil, err
	}
	r := &Request{Bytes: len(body), raw: body}

	m, ok := lookup(top, keyModel)
	if !ok {
		return nil, errors.New("responses: body has no model")
	}
	if err := json.Unmarshal(body[m.start:m.end], &r.Model); err != nil || r.Model == "" {
		return nil, errors.New("responses: model is not a non-empty string")
	}

	if m, ok := lookup(top, keyReasoning); ok {
		if r.Effort, err = parseEffort(body, m); err != nil {
			return nil, err
		}
	}

	if m, ok := lookup(top, keyInput); ok {
		var raw []json.RawMessage
		if err := json.Unmarshal(body[m.start:m.end], &raw); err != nil {
			return nil, fmt.Errorf("responses: input is not an array: %w", err)
		}
		r.Input = make([]Item, len(raw))
		for i, it := range raw {
			r.Input[i] = parseItem(it)
		}
		r.readContext(top, raw)
	}
	return r, nil
}

func parseEffort(body []byte, reasoning member) (config.Effort, error) {
	switch body[reasoning.start] {
	case 'n':
		return "", nil
	case '{':
	default:
		return "", errors.New("responses: reasoning is not an object")
	}
	inner, err := members(body, reasoning.start)
	if err != nil {
		return "", err
	}
	m, ok := lookup(inner, keyEffort)
	if !ok {
		return "", nil
	}
	var effort *string
	if err := json.Unmarshal(body[m.start:m.end], &effort); err != nil {
		return "", errors.New("responses: reasoning.effort is not a string")
	}
	if effort == nil {
		return "", nil
	}
	return config.Effort(*effort), nil
}

type wireItem struct {
	Type      string          `json:"type"`
	Role      string          `json:"role"`
	Name      string          `json:"name"`
	CallID    string          `json:"call_id"`
	Arguments string          `json:"arguments"`
	Input     string          `json:"input"`
	Content   json.RawMessage `json:"content"`
	Output    json.RawMessage `json:"output"`
	Summary   json.RawMessage `json:"summary"`
	Action    json.RawMessage `json:"action"`
}

// parseItem flattens one input item. An unknown type, or a known type in a
// shape this code does not expect, keeps only Type: the item stays opaque and
// still counts toward the input length.
func parseItem(raw json.RawMessage) Item {
	var head struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(raw, &head) != nil {
		return Item{}
	}
	opaque := Item{Type: head.Type}
	switch head.Type {
	case TypeMessage, TypeFunctionCall, TypeFunctionCallOutput, TypeCustomToolCall,
		TypeCustomToolCallOutput, TypeLocalShellCall, TypeReasoning:
	default:
		return opaque
	}
	var w wireItem
	if json.Unmarshal(raw, &w) != nil {
		return opaque
	}
	it := Item{Type: w.Type, Role: w.Role, Name: w.Name, CallID: w.CallID}
	switch w.Type {
	case TypeMessage:
		it.Text = joinText(w.Content)
	case TypeReasoning:
		it.Text = joinText(w.Summary)
	case TypeFunctionCall:
		it.Arguments = w.Arguments
	case TypeCustomToolCall:
		it.Arguments = w.Input
	case TypeLocalShellCall:
		it.Arguments = string(w.Action)
	case TypeFunctionCallOutput, TypeCustomToolCallOutput:
		it.Output = joinText(w.Output)
	}
	return it
}

// joinText reads a string, or an array of content parts whose text fields
// are joined by newlines. Parts without text (images, files) are skipped.
func joinText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var parts []struct {
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &parts) != nil {
		return ""
	}
	texts := make([]string, 0, len(parts))
	for _, p := range parts {
		if p.Text != "" {
			texts = append(texts, p.Text)
		}
	}
	return strings.Join(texts, "\n")
}

type edit struct {
	start, end int
	value      string
}

// Apply returns the body with model and reasoning.effort set from rw. Only
// those values change; every other byte, including key order and
// whitespace, stays as Codex sent it. An absent reasoning object is added
// with the effort only. A no-op rewrite returns the original body, which the
// caller must not modify.
func (r *Request) Apply(rw Rewrite) ([]byte, error) {
	if r.raw == nil {
		return nil, errors.New("responses: request was not parsed")
	}
	root := skipSpace(r.raw, 0)
	top, err := members(r.raw, root)
	if err != nil {
		return nil, err
	}
	var edits []edit
	if rw.Model != "" && rw.Model != r.Model {
		edits = append(edits, replaceAll(top, keyModel, quote(rw.Model))...)
	}
	if rw.Effort != "" && rw.Effort != r.Effort {
		e, err := r.effortEdits(root, top, quote(string(rw.Effort)))
		if err != nil {
			return nil, err
		}
		edits = append(edits, e...)
	}
	if len(edits) == 0 {
		return r.raw, nil
	}
	return splice(r.raw, edits), nil
}

// effortEdits sets effort in every reasoning member (duplicates included, so
// the upstream sees the new value whichever duplicate it reads).
func (r *Request) effortEdits(root int, top []member, effort string) ([]edit, error) {
	if _, ok := lookup(top, keyReasoning); !ok {
		return []edit{insert(root, top, keyReasoning, "{"+field(keyEffort, effort)+"}")}, nil
	}
	var edits []edit
	for _, m := range top {
		if m.key != keyReasoning {
			continue
		}
		if r.raw[m.start] != '{' {
			edits = append(edits, edit{m.start, m.end, "{" + field(keyEffort, effort) + "}"})
			continue
		}
		inner, err := members(r.raw, m.start)
		if err != nil {
			return nil, err
		}
		if _, ok := lookup(inner, keyEffort); !ok {
			edits = append(edits, insert(m.start, inner, keyEffort, effort))
			continue
		}
		edits = append(edits, replaceAll(inner, keyEffort, effort)...)
	}
	return edits, nil
}

func replaceAll(ms []member, key, value string) []edit {
	var edits []edit
	for _, m := range ms {
		if m.key == key {
			edits = append(edits, edit{m.start, m.end, value})
		}
	}
	return edits
}

// insert adds key:value as the first member of the object whose '{' is at open.
func insert(open int, existing []member, key, value string) edit {
	v := field(key, value)
	if len(existing) > 0 {
		v += ","
	}
	return edit{open + 1, open + 1, v}
}

func field(key, value string) string { return quote(key) + ":" + value }

func quote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func splice(raw []byte, edits []edit) []byte {
	slices.SortFunc(edits, func(a, b edit) int { return cmp.Compare(a.start, b.start) })
	var out bytes.Buffer
	out.Grow(len(raw) + 64)
	prev := 0
	for _, e := range edits {
		out.Write(raw[prev:e.start])
		out.WriteString(e.value)
		prev = e.end
	}
	out.Write(raw[prev:])
	return out.Bytes()
}

// Body returns the raw body Parse read, which the caller must not modify.
func (r *Request) Body() []byte { return r.raw }

// Fingerprint identifies one request of a turn. Retries of a request resend
// the same body, so they share it; each later step of the turn appends items,
// so it changes. It hashes the original model, not a rewrite.
func (r *Request) Fingerprint(m Meta) string {
	body := r.raw
	if len(body) == 0 {
		body, _ = json.Marshal([]any{r.Model, r.Effort, r.Input, r.Bytes})
	}
	h := sha256.New()
	hashPart(h, []byte(m.TurnID))
	hashPart(h, body)
	return hex.EncodeToString(h.Sum(nil))
}
