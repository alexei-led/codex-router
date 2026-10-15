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
			header("x-codex-turn-metadata", `{"session_id":"s1","thread_id":"t1","turn_id":"u1","request_kind":"turn","thread_source":"user","agent_name":"/root","extra":{"k":1}}`,
				"x-openai-subagent", "review", "chatgpt-account-id", "acct-raw-123"),
			responses.Meta{SessionID: "s1", ThreadID: "t1", TurnID: "u1", RequestKind: "turn", ThreadSource: "user", Subagent: "review",
				Account: responses.AccountHash("acct-raw-123")}},
		{"no headers", http.Header{}, responses.Meta{}},
		{"unparseable metadata keeps other headers",
			header("x-codex-turn-metadata", `{"turn_id":`, "x-openai-subagent", "review"),
			responses.Meta{Subagent: "review"}},
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

func turnMetadata(kind, turn string) string {
	return sourcedMetadata(kind, turn, "user")
}

func sourcedMetadata(kind, turn, source string) string {
	return `{"request_kind":"` + kind + `","turn_trigger":"user","thread_source":"` + source + `","agent_name":"/root",` +
		`"root_turn_id":"` + turn + `","turn_id":"` + turn + `","thread_id":"th1","model":"gpt-6-sol","reasoning_effort":"medium"}`
}

func TestAccountHash(t *testing.T) {
	tests := []struct {
		name, id, want string
	}{
		{"empty id", "", ""},
		{"first 12 hex of sha256", "acct-raw-123", "e35932f46677"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := responses.AccountHash(tt.id); got != tt.want {
				t.Fatalf("AccountHash(%q) = %q, want %q", tt.id, got, tt.want)
			}
		})
	}
	if responses.AccountHash("acct-1") == responses.AccountHash("acct-2") {
		t.Fatal("two accounts share a hash")
	}
}

func TestClassify(t *testing.T) {
	tests := []struct {
		name string
		h    http.Header
		want responses.Kind
	}{
		{"user turn", header("x-codex-turn-metadata", turnMetadata("turn", "u1")), responses.KindUserStep},
		{"compaction kind", header("x-codex-turn-metadata", turnMetadata("compaction", "u1")), responses.KindPassthrough},
		{"other request kind", header("x-codex-turn-metadata", turnMetadata("memory", "u1")), responses.KindPassthrough},
		{"subagent header", header("x-codex-turn-metadata", turnMetadata("turn", "u1"), "x-openai-subagent", "worker"), responses.KindPassthrough},
		{"missing metadata", http.Header{}, responses.KindPassthrough},
		{"unparseable metadata", header("x-codex-turn-metadata", `{"request_kind":"turn",`), responses.KindPassthrough},
		{"missing request kind", header("x-codex-turn-metadata", `{"thread_id":"th1","turn_id":"u1"}`), responses.KindPassthrough},
		{"missing turn id", header("x-codex-turn-metadata", turnMetadata("turn", "")), responses.KindPassthrough},
		{"thread title thread", header("x-codex-turn-metadata", sourcedMetadata("turn", "u1", "thread_title")), responses.KindPassthrough},
		{"guardian review thread", header("x-codex-turn-metadata", sourcedMetadata("turn", "u1", "guardian_review")), responses.KindPassthrough},
		{"memory consolidation thread", header("x-codex-turn-metadata", sourcedMetadata("turn", "u1", "memory_consolidation")), responses.KindPassthrough},
		{"missing thread source", header("x-codex-turn-metadata", `{"request_kind":"turn","thread_id":"th1","turn_id":"u1"}`), responses.KindUserStep},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := responses.Classify(responses.ParseMeta(tt.h)); got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestClassifyRecordedRequests(t *testing.T) {
	tests := []struct {
		name, kind, body string
		want             responses.Kind
	}{
		{"local compaction", "compaction",
			`{"model":"gpt-6-sol","input":[{"type":"message","role":"user","content":"fix the test"},` +
				`{"type":"message","role":"user","content":"Summarize the conversation so far"}]}`,
			responses.KindPassthrough},
		{"remote v2 compaction", "compaction",
			`{"model":"gpt-6-sol","input":[{"type":"message","role":"user","content":"fix"},{"type":"compaction_trigger"}]}`,
			responses.KindPassthrough},
		{"turn after remote compaction", "turn",
			`{"model":"gpt-6-sol","input":[{"type":"compaction","encrypted_content":"e"},{"type":"message","role":"user","content":"go on"}]}`,
			responses.KindUserStep},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := mustParse(t, tt.body)
			if r.Model != "gpt-6-sol" || len(r.Input) != 2 {
				t.Fatalf("parsed %+v", r)
			}
			if got := responses.Classify(responses.ParseMeta(header("x-codex-turn-metadata", turnMetadata(tt.kind, "u1")))); got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
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
