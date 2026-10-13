package responses_test

import (
	"testing"

	"github.com/alexei-led/codex-router/internal/responses"
)

func TestUsageFromSSE(t *testing.T) {
	const completed = `{"type":"response.completed","response":{"id":"resp_1","model":"gpt-6-luna","usage":` +
		`{"input_tokens":20480,"input_tokens_details":{"cached_tokens":20224},"output_tokens":310,` +
		`"output_tokens_details":{"reasoning_tokens":128},"total_tokens":20790}}}`
	full := responses.Usage{InputTokens: 20480, CachedInputTokens: 20224, OutputTokens: 310, ReasoningTokens: 128, TotalTokens: 20790}

	tests := []struct {
		name      string
		event     string
		wantUsage responses.Usage
		wantModel string
		wantOK    bool
	}{
		{"event block", "event: response.completed\ndata: " + completed + "\n\n", full, "gpt-6-luna", true},
		{"crlf event block", "event: response.completed\r\ndata: " + completed + "\r\n\r\n", full, "gpt-6-luna", true},
		{"data payload only", completed, full, "gpt-6-luna", true},
		{"completed without usage", `data: {"type":"response.completed","response":{"model":"m"}}`, responses.Usage{}, "m", true},
		{"other event", `data: {"type":"response.created","response":{"model":"m"}}`, responses.Usage{}, "", false},
		{"output item mentioning completed",
			`data: {"type":"response.output_item.done","item":{"type":"message","content":[{"type":"output_text","text":"response.completed"}]}}`,
			responses.Usage{}, "", false},
		{"malformed data", "data: {\"type\":\"response.completed\"", responses.Usage{}, "", false},
		{"empty", "", responses.Usage{}, "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			u, model, ok := responses.UsageFromSSE([]byte(tt.event))
			if u != tt.wantUsage || model != tt.wantModel || ok != tt.wantOK {
				t.Fatalf("got (%+v, %q, %v), want (%+v, %q, %v)", u, model, ok, tt.wantUsage, tt.wantModel, tt.wantOK)
			}
		})
	}
}
