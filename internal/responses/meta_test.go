package responses_test

import (
	"net/http"
	"testing"

	"github.com/alexei-led/codex-router/internal/responses"
)

func header(kv ...string) http.Header {
	h := http.Header{}
	for i := 0; i < len(kv); i += 2 {
		h.Set(kv[i], kv[i+1])
	}
	return h
}

func TestParseMeta(t *testing.T) {
	tests := []struct {
		name string
		h    http.Header
		want responses.Meta
	}{
		{"full metadata",
			header("x-codex-turn-metadata", `{"session_id":"s1","thread_id":"t1","turn_id":"u1","agent_name":"main","extra":{"k":1}}`,
				"x-openai-subagent", "review", "x-codex-window-id", "w1"),
			responses.Meta{SessionID: "s1", ThreadID: "t1", TurnID: "u1", AgentName: "main", Subagent: "review", WindowID: "w1"}},
		{"no headers", http.Header{}, responses.Meta{}},
		{"unparseable metadata keeps other headers",
			header("x-codex-turn-metadata", `{"turn_id":`, "x-codex-window-id", "w1"),
			responses.Meta{WindowID: "w1"}},
		{"metadata without turn id",
			header("x-codex-turn-metadata", `{"session_id":"s1","thread_id":"t1"}`),
			responses.Meta{SessionID: "s1", ThreadID: "t1"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := responses.ParseMeta(tt.h); got != tt.want {
				t.Fatalf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestClassify(t *testing.T) {
	const summarize = "You have exceeded the maximum number of tokens"
	userStep := `{"model":"m","input":[{"type":"message","role":"user","content":"fix the test"},{"type":"function_call_output","call_id":"c","output":"ok"}]}`
	compaction := `{"model":"m","input":[{"type":"message","role":"user","content":"fix the test"},{"type":"message","role":"user","content":"\n ` + summarize + `, summarize"}]}`
	turn := responses.Meta{ThreadID: "t1", TurnID: "u1"}
	prefixes := []string{"", summarize}

	tests := []struct {
		name string
		body string
		meta responses.Meta
		want responses.Kind
	}{
		{"user step", userStep, turn, responses.KindUserStep},
		{"subagent header", userStep, responses.Meta{ThreadID: "t1", TurnID: "u1", Subagent: "review"}, responses.KindPassthrough},
		{"missing turn id", userStep, responses.Meta{ThreadID: "t1"}, responses.KindPassthrough},
		{"last user text matches a prefix", compaction, turn, responses.KindPassthrough},
		{"no user message", `{"model":"m","input":[]}`, turn, responses.KindUserStep},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := responses.Classify(mustParse(t, tt.body), tt.meta, prefixes); got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
	if got := responses.Classify(nil, turn, nil); got != responses.KindPassthrough {
		t.Fatalf("nil request: got %q", got)
	}
}

func TestFingerprint(t *testing.T) {
	const (
		step1 = `{"model":"gpt-6-sol","input":[{"type":"message","role":"user","content":"run tests"}]}`
		step2 = `{"model":"gpt-6-sol","input":[{"type":"message","role":"user","content":"run tests"},` +
			`{"type":"function_call","call_id":"c1","name":"exec_command","arguments":"{}"},{"type":"function_call_output","call_id":"c1","output":"ok"}]}`
		step3 = `{"model":"gpt-6-sol","input":[{"type":"message","role":"user","content":"run tests"},` +
			`{"type":"function_call","call_id":"c1","name":"exec_command","arguments":"{}"},{"type":"function_call_output","call_id":"c1","output":"ok"},` +
			`{"type":"function_call","call_id":"c2","name":"exec_command","arguments":"{}"},{"type":"function_call_output","call_id":"c2","output":"ok"}]}`
	)
	turn := responses.Meta{ThreadID: "t1", TurnID: "u1"}
	fp := func(body string, m responses.Meta) string { return mustParse(t, body).Fingerprint(m) }

	first, retry := mustParse(t, step2), mustParse(t, step2)
	if first.Fingerprint(turn) != retry.Fingerprint(turn) {
		t.Fatal("a retry of the same request changed the fingerprint")
	}
	seen := map[string]string{}
	for name, body := range map[string]string{"step1": step1, "step2": step2, "step3": step3} {
		f := fp(body, turn)
		if prev, ok := seen[f]; ok {
			t.Fatalf("%s and %s share a fingerprint", prev, name)
		}
		seen[f] = name
	}
	if fp(step1, turn) == fp(step1, responses.Meta{ThreadID: "t1", TurnID: "u2"}) {
		t.Fatal("different turns share a fingerprint")
	}
}
