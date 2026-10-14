package responses

import (
	"encoding/json"
	"net/http"
)

// RequestKindTurn is the x-codex-turn-metadata request_kind of a user step.
// Compaction (local and remote v2) sends "compaction" (research §9, spike 1c).
const RequestKindTurn = "turn"

// ThreadSourceUser is the x-codex-turn-metadata thread_source of a thread the
// user started. Codex 0.162.1 runs side work in its own threads with another
// source: subagent, guardian_review, memory_consolidation, or a feature name
// such as thread_title (the footer's title, generated after the first
// prompt).
const ThreadSourceUser = "user"

// ParseMeta reads the turn metadata, and subagent headers. A missing
// or unparseable turn metadata header leaves the metadata fields empty.
func ParseMeta(h http.Header) Meta {
	m := Meta{Subagent: h.Get(HeaderSubagent)}
	raw := h.Get(HeaderTurnMetadata)
	if raw == "" {
		return m
	}
	var tm struct {
		SessionID    string `json:"session_id"`
		ThreadID     string `json:"thread_id"`
		TurnID       string `json:"turn_id"`
		RequestKind  string `json:"request_kind"`
		ThreadSource string `json:"thread_source"`
	}
	if json.Unmarshal([]byte(raw), &tm) != nil {
		return m
	}
	m.SessionID, m.ThreadID, m.TurnID = tm.SessionID, tm.ThreadID, tm.TurnID
	m.RequestKind, m.ThreadSource = tm.RequestKind, tm.ThreadSource
	return m
}

// Classify applies plan decision D5 as spike 1c settled it: only a main-thread
// turn request is a user step. Compaction, any other request kind, missing
// metadata, a missing turn ID, every subagent request and a thread of any
// source but the user's pass through. A missing thread source counts as the
// user's.
func Classify(m Meta) Kind {
	if m.RequestKind != RequestKindTurn || m.TurnID == "" || m.Subagent != "" ||
		m.ThreadSource != "" && m.ThreadSource != ThreadSourceUser {
		return KindPassthrough
	}
	return KindUserStep
}
