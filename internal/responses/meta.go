package responses

import (
	"encoding/json"
	"net/http"
)

// RequestKindTurn is the x-codex-turn-metadata request_kind of a user step.
// Compaction (local and remote v2) sends "compaction" (research §9, spike 1c).
const RequestKindTurn = "turn"

// ParseMeta reads the turn metadata, subagent and window headers. A missing
// or unparseable turn metadata header leaves the metadata fields empty.
func ParseMeta(h http.Header) Meta {
	m := Meta{Subagent: h.Get(HeaderSubagent), WindowID: h.Get(HeaderWindowID)}
	raw := h.Get(HeaderTurnMetadata)
	if raw == "" {
		return m
	}
	var tm struct {
		SessionID   string `json:"session_id"`
		ThreadID    string `json:"thread_id"`
		TurnID      string `json:"turn_id"`
		RequestKind string `json:"request_kind"`
		AgentName   string `json:"agent_name"`
	}
	if json.Unmarshal([]byte(raw), &tm) != nil {
		return m
	}
	m.SessionID, m.ThreadID, m.TurnID = tm.SessionID, tm.ThreadID, tm.TurnID
	m.RequestKind, m.AgentName = tm.RequestKind, tm.AgentName
	return m
}

// Classify applies plan decision D5 as spike 1c settled it: only a main-thread
// turn request is a user step. Compaction, any other request kind, missing
// metadata, a missing turn ID and every subagent request pass through.
func Classify(m Meta) Kind {
	if m.RequestKind != RequestKindTurn || m.TurnID == "" || m.Subagent != "" {
		return KindPassthrough
	}
	return KindUserStep
}
