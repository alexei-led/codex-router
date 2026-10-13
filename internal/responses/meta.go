package responses

import (
	"encoding/json"
	"net/http"
	"strings"
	"unicode"
)

// ParseMeta reads the turn metadata, subagent and window headers. A missing
// or unparseable turn metadata header leaves the IDs empty.
func ParseMeta(h http.Header) Meta {
	m := Meta{Subagent: h.Get(HeaderSubagent), WindowID: h.Get(HeaderWindowID)}
	raw := h.Get(HeaderTurnMetadata)
	if raw == "" {
		return m
	}
	var tm struct {
		SessionID string `json:"session_id"`
		ThreadID  string `json:"thread_id"`
		TurnID    string `json:"turn_id"`
		AgentName string `json:"agent_name"`
	}
	if json.Unmarshal([]byte(raw), &tm) != nil {
		return m
	}
	m.SessionID, m.ThreadID, m.TurnID, m.AgentName = tm.SessionID, tm.ThreadID, tm.TurnID, tm.AgentName
	return m
}

// Classify applies plan decision D5: a request is a user step unless it comes
// from a subagent, has no turn ID, or its last user text starts with one of
// the passthrough prefixes (compaction and similar internal prompts).
func Classify(r *Request, m Meta, passthrough []string) Kind {
	if r == nil || m.Subagent != "" || m.TurnID == "" {
		return KindPassthrough
	}
	text := strings.TrimLeftFunc(lastUserText(r.Input), unicode.IsSpace)
	for _, p := range passthrough {
		if p != "" && strings.HasPrefix(text, p) {
			return KindPassthrough
		}
	}
	return KindUserStep
}

func lastUserText(items []Item) string {
	for i := len(items) - 1; i >= 0; i-- {
		if items[i].Type == TypeMessage && items[i].Role == "user" {
			return items[i].Text
		}
	}
	return ""
}
