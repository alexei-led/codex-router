package hook

// Input is the subset of the Codex hook stdin the hook reads.
type Input struct {
	SessionID string `json:"session_id"`
	TurnID    string `json:"turn_id"`
	Model     string `json:"model"`
	Event     string `json:"hook_event_name"`
}

// Output is the hook's stdout. Only systemMessage is ever set.
type Output struct {
	SystemMessage string `json:"systemMessage,omitempty"`
}

// MessagePrefix starts every line; Codex renders it as "↳ Hook · router: …".
const MessagePrefix = "router: "
