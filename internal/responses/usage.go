package responses

import (
	"bytes"
	"encoding/json"
)

const eventCompleted = "response.completed"

// UsageFromSSE reads one SSE event, either the whole event block
// ("event: …\ndata: {…}") or just its data payload. For response.completed it
// returns the usage, the served model and true; for any other event, false.
func UsageFromSSE(event []byte) (Usage, string, bool) {
	data := sseData(event)
	// Most events are output items; skip decoding them.
	if !bytes.Contains(data, []byte(eventCompleted)) {
		return Usage{}, "", false
	}
	var ev struct {
		Type     string `json:"type"`
		Response struct {
			Model string `json:"model"`
			Usage struct {
				InputTokens        int `json:"input_tokens"`
				InputTokensDetails struct {
					CachedTokens int `json:"cached_tokens"`
				} `json:"input_tokens_details"`
				OutputTokens        int `json:"output_tokens"`
				OutputTokensDetails struct {
					ReasoningTokens int `json:"reasoning_tokens"`
				} `json:"output_tokens_details"`
				TotalTokens int `json:"total_tokens"`
			} `json:"usage"`
		} `json:"response"`
	}
	if json.Unmarshal(data, &ev) != nil || ev.Type != eventCompleted {
		return Usage{}, "", false
	}
	u := ev.Response.Usage
	return Usage{
		InputTokens:       u.InputTokens,
		CachedInputTokens: u.InputTokensDetails.CachedTokens,
		OutputTokens:      u.OutputTokens,
		ReasoningTokens:   u.OutputTokensDetails.ReasoningTokens,
		TotalTokens:       u.TotalTokens,
	}, ev.Response.Model, true
}

// sseData joins the data: lines of an event, or returns the input unchanged
// when it has none.
func sseData(event []byte) []byte {
	var lines [][]byte
	for line := range bytes.Lines(event) {
		line = bytes.TrimRight(line, "\r\n")
		if v, ok := bytes.CutPrefix(line, []byte("data:")); ok {
			lines = append(lines, bytes.TrimPrefix(v, []byte(" ")))
		}
	}
	if len(lines) == 0 {
		return event
	}
	return bytes.Join(lines, []byte("\n"))
}
