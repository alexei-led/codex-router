package responses

import (
	"strings"
	"testing"
)

func TestContextRequiresExactPrefixAndEnvelope(t *testing.T) {
	base := `{"model":"gpt-6.1-sol","reasoning":{"effort":"high","summary":"auto"},"tools":[{"name":"read"}],"input":[{"type":"reasoning","encrypted_content":"opaque"},{"type":"message","role":"user","content":"hi"}]}`
	parse := func(body string) *Request {
		t.Helper()
		r, err := Parse([]byte(body))
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	anchor := parse(base).Context()
	appendItem := strings.TrimSuffix(base, "]}") + `,{"type":"function_call_output","output":"done"}]}`
	for _, tt := range []struct {
		name, body string
		ok         bool
	}{
		{"identical", base, true},
		{"appended", appendItem, true},
		{"model and effort", strings.ReplaceAll(strings.ReplaceAll(appendItem, "gpt-6.1-sol", "gpt-6-luna"), `"high"`, `"low"`), true},
		{"tools", strings.ReplaceAll(appendItem, `"read"`, `"edit"`), false},
		{"reasoning setting", strings.ReplaceAll(appendItem, `"auto"`, `"none"`), false},
		{"opaque prefix changed", strings.ReplaceAll(appendItem, `"opaque"`, `"edited"`), false},
		{"same count edit", strings.ReplaceAll(base, `"hi"`, `"no"`), false},
		{"compaction", `{"model":"gpt-6.1-sol","input":[{"type":"compaction","encrypted_content":"summary"}]}`, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r := parse(tt.body)
			added, ok := r.AddedTokens(anchor)
			if ok != tt.ok {
				t.Fatalf("AddedTokens = %d, %v; want valid=%v", added, ok, tt.ok)
			}
			if tt.name == "identical" && added != 0 {
				t.Fatalf("identical prefix adds %d tokens", added)
			}
			if tt.name == "appended" && added != 12 {
				t.Fatalf("suffix estimate = %d, want 12", added)
			}
		})
	}
	if _, ok := parse(base).AddedTokens(Context{}); ok {
		t.Fatal("unknown anchor accepted")
	}
	if parse(base).Fingerprint(Meta{TurnID: "t"}) == parse(strings.ReplaceAll(base, `"hi"`, `"no"`)).Fingerprint(Meta{TurnID: "t"}) {
		t.Fatal("equal-length edit mistaken for retry")
	}
}
