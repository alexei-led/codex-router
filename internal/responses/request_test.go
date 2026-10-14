package responses_test

import (
	"bytes"
	"encoding/json"
	"os"
	"slices"
	"testing"

	"github.com/alexei-led/codex-router/internal/config"
	"github.com/alexei-led/codex-router/internal/responses"
)

func readFixture(t testing.TB, name string) []byte {
	t.Helper()
	body, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func mustParse(t *testing.T, body string) *responses.Request {
	t.Helper()
	r, err := responses.Parse([]byte(body))
	if err != nil {
		t.Fatalf("Parse(%s): %v", body, err)
	}
	return r
}

func TestParseFixture(t *testing.T) {
	body := readFixture(t, "request.json")
	r, err := responses.Parse(body)
	if err != nil {
		t.Fatal(err)
	}
	if r.Model != "gpt-6.1-sol" || r.Effort != config.EffortHigh || r.Bytes != len(body) {
		t.Fatalf("got model=%q effort=%q bytes=%d", r.Model, r.Effort, r.Bytes)
	}
	want := []responses.Item{
		{Type: "message", Role: "developer", Text: "You are Codex, a coding agent.\nQuote: \"be precise\" — café <tags> & {braces} [brackets]\\path"},
		{Type: "message", Role: "developer", Text: `<additional_tools>[{"name":"exec_command","model":"not-this-one"}]</additional_tools>`},
		{Type: "message", Role: "user", Text: "<environment_context>\n  <cwd>/work/repo</cwd>\n</environment_context>"},
		{Type: "message", Role: "user", Text: "Fix the flaky retry test."},
		{Type: "reasoning", Text: "Looking at the test."},
		{Type: "function_call", Name: "exec_command", CallID: "call_1", Arguments: `{"cmd":"go test ./...","model":"x"}`},
		{Type: "function_call_output", CallID: "call_1", Output: "--- FAIL: TestRetry (0.01s)\nexit status 1"},
		{Type: "custom_tool_call", Name: "apply_patch", CallID: "call_2", Arguments: "*** Begin Patch\n*** Update File: retry.go\n*** End Patch"},
		{Type: "custom_tool_call_output", CallID: "call_2", Output: "Success."},
		{Type: "local_shell_call", CallID: "call_3", Arguments: `{"type":"exec","command":["go","test","./..."]}`},
		{Type: "web_search_call"},
		{Type: "compaction_summary"},
		{Type: "message", Role: "assistant", Text: "Fixed."},
	}
	if !slices.Equal(r.Input, want) {
		for i := range max(len(r.Input), len(want)) {
			var got, exp responses.Item
			if i < len(r.Input) {
				got = r.Input[i]
			}
			if i < len(want) {
				exp = want[i]
			}
			if got != exp {
				t.Errorf("item %d:\n got %+v\nwant %+v", i, got, exp)
			}
		}
	}
}

func TestParseItemShapes(t *testing.T) {
	tests := []struct {
		name string
		item string
		want responses.Item
	}{
		{"message with string content", `{"type":"message","role":"user","content":"hi"}`,
			responses.Item{Type: "message", Role: "user", Text: "hi"}},
		{"message with several text parts", `{"type":"message","role":"user","content":[{"type":"input_text","text":"a"},{"type":"input_text","text":"b"}]}`,
			responses.Item{Type: "message", Role: "user", Text: "a\nb"}},
		{"tool output as content parts", `{"type":"function_call_output","call_id":"c","output":[{"type":"input_text","text":"ok"},{"type":"input_image","image_url":"x"}]}`,
			responses.Item{Type: "function_call_output", CallID: "c", Output: "ok"}},
		{"known type in unexpected shape stays opaque", `{"type":"function_call","arguments":{"cmd":"ls"}}`,
			responses.Item{Type: "function_call"}},
		{"unknown type keeps only type", `{"type":"tool_search_call","name":"n","call_id":"c"}`,
			responses.Item{Type: "tool_search_call"}},
		{"non-object item is kept empty", `"text"`, responses.Item{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := mustParse(t, `{"model":"m","input":[`+tt.item+`]}`)
			if len(r.Input) != 1 || r.Input[0] != tt.want {
				t.Fatalf("got %+v, want %+v", r.Input, tt.want)
			}
		})
	}
}

func TestParseEffort(t *testing.T) {
	tests := []struct {
		name string
		body string
		want config.Effort
	}{
		{"absent reasoning", `{"model":"m"}`, ""},
		{"null reasoning", `{"model":"m","reasoning":null}`, ""},
		{"reasoning without effort", `{"model":"m","reasoning":{"summary":"auto"}}`, ""},
		{"null effort", `{"model":"m","reasoning":{"effort":null}}`, ""},
		{"effort", `{"model":"m","reasoning":{"effort":"xhigh"}}`, config.EffortXHigh},
		{"last duplicate wins", `{"model":"m","reasoning":{"effort":"low","effort":"high"}}`, config.EffortHigh},
		{"key match is exact", `{"model":"m","reasoning":{"Effort":"high"}}`, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := mustParse(t, tt.body).Effort; got != tt.want {
				t.Fatalf("effort = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestParseRejects(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{"empty", ``},
		{"truncated", `{"model":"m"`},
		{"array root", `[{"model":"m"}]`},
		{"null root", `null`},
		{"no model", `{"input":[]}`},
		{"model key in other case", `{"Model":"m"}`},
		{"empty model", `{"model":""}`},
		{"model not a string", `{"model":5}`},
		{"reasoning not an object", `{"model":"m","reasoning":"high"}`},
		{"effort not a string", `{"model":"m","reasoning":{"effort":3}}`},
		{"input not an array", `{"model":"m","input":"hello"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if r, err := responses.Parse([]byte(tt.body)); err == nil {
				t.Fatalf("Parse succeeded: %+v", r)
			}
		})
	}
}

func TestApplyFixtureChangesOnlyModelAndEffort(t *testing.T) {
	body := readFixture(t, "request.json")
	oldModel, newModel := []byte(`"model": "gpt-6.1-sol"`), []byte(`"model": "gpt-6-luna"`)
	oldEffort, newEffort := []byte(`"effort" : "high"`), []byte(`"effort" : "low"`)
	for _, s := range [][]byte{oldModel, oldEffort} {
		if n := bytes.Count(body, s); n != 1 {
			t.Fatalf("fixture has %d copies of %s, want 1", n, s)
		}
	}
	want := bytes.Replace(bytes.Replace(body, oldModel, newModel, 1), oldEffort, newEffort, 1)

	r, err := responses.Parse(body)
	if err != nil {
		t.Fatal(err)
	}
	got, err := r.Apply(responses.Rewrite{Model: "gpt-6-luna", Effort: config.EffortLow})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("rewritten body differs beyond model and effort:\n%s", got)
	}

	back, err := responses.Parse(got)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := back.Apply(responses.Rewrite{Model: "gpt-6.1-sol", Effort: config.EffortHigh})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(restored, body) {
		t.Fatalf("round trip changed the body:\n%s", restored)
	}
}

func TestApplyLargeBodyMatchesGolden(t *testing.T) {
	body := readFixture(t, "request_large.json")
	golden := readFixture(t, "request_large.golden.json")
	if len(body) < 95_000 {
		t.Fatalf("fixture is %d bytes, want the ~99 KB shape of a first Codex request", len(body))
	}

	r, err := responses.Parse(body)
	if err != nil {
		t.Fatal(err)
	}
	if r.Model != "gpt-6.1-sol" || r.Effort != config.EffortHigh || len(r.Input) != 80 {
		t.Fatalf("got model=%q effort=%q items=%d", r.Model, r.Effort, len(r.Input))
	}
	got, err := r.Apply(responses.Rewrite{Model: "gpt-6-luna", Effort: config.EffortLow})
	if err != nil {
		t.Fatal(err)
	}
	if i := firstDiff(got, golden); i >= 0 {
		t.Fatalf("rewritten body differs from golden at byte %d of %d (golden %d)", i, len(got), len(golden))
	}

	back, err := responses.Parse(golden)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := back.Apply(responses.Rewrite{Model: "gpt-6.1-sol", Effort: config.EffortHigh})
	if err != nil {
		t.Fatal(err)
	}
	if i := firstDiff(restored, body); i >= 0 {
		t.Fatalf("round trip differs from the original at byte %d", i)
	}
}

func firstDiff(a, b []byte) int {
	for i := range min(len(a), len(b)) {
		if a[i] != b[i] {
			return i
		}
	}
	if len(a) != len(b) {
		return min(len(a), len(b))
	}
	return -1
}

func TestApply(t *testing.T) {
	tests := []struct {
		name string
		body string
		rw   responses.Rewrite
		want string
	}{
		{"model only",
			`{"model":"a","reasoning":{"effort":"high"}}`, responses.Rewrite{Model: "b"},
			`{"model":"b","reasoning":{"effort":"high"}}`},
		{"effort only",
			`{"model":"a","reasoning":{"effort":"high","summary":"concise"}}`, responses.Rewrite{Effort: config.EffortLow},
			`{"model":"a","reasoning":{"effort":"low","summary":"concise"}}`},
		{"reasoning absent is added with effort only",
			`{"model":"a","stream":true}`, responses.Rewrite{Effort: config.EffortLow},
			`{"reasoning":{"effort":"low"},"model":"a","stream":true}`},
		{"null reasoning becomes an object",
			`{"model":"a","reasoning":null}`, responses.Rewrite{Effort: config.EffortLow},
			`{"model":"a","reasoning":{"effort":"low"}}`},
		{"empty reasoning gets effort",
			`{"model":"a","reasoning":{ }}`, responses.Rewrite{Effort: config.EffortLow},
			`{"model":"a","reasoning":{"effort":"low" }}`},
		{"reasoning without effort keeps its fields",
			`{"model":"a","reasoning":{"summary":"auto"}}`, responses.Rewrite{Effort: config.EffortLow},
			`{"model":"a","reasoning":{"effort":"low","summary":"auto"}}`},
		{"null effort is replaced",
			`{"model":"a","reasoning":{"effort":null}}`, responses.Rewrite{Effort: config.EffortLow},
			`{"model":"a","reasoning":{"effort":"low"}}`},
		{"whitespace and layout are kept",
			"{\n  \"model\" :\t\"a\" ,\n  \"reasoning\": { \"effort\":  \"high\" }\n}\n", responses.Rewrite{Model: "bb", Effort: config.EffortXHigh},
			"{\n  \"model\" :\t\"bb\" ,\n  \"reasoning\": { \"effort\":  \"xhigh\" }\n}\n"},
		{"nested model and effort keys are untouched",
			`{"input":[{"type":"x","model":"a","reasoning":{"effort":"high"}}],"model":"a","reasoning":{"effort":"high"}}`, responses.Rewrite{Model: "b", Effort: config.EffortLow},
			`{"input":[{"type":"x","model":"a","reasoning":{"effort":"high"}}],"model":"b","reasoning":{"effort":"low"}}`},
		{"escaped key is matched",
			`{"mod\u0065l":"a"}`, responses.Rewrite{Model: "b"},
			`{"mod\u0065l":"b"}`},
		{"every duplicate is rewritten",
			`{"model":"x","model":"a","reasoning":{"effort":"low"},"reasoning":{"effort":"high"}}`, responses.Rewrite{Model: "b", Effort: config.EffortMedium},
			`{"model":"b","model":"b","reasoning":{"effort":"medium"},"reasoning":{"effort":"medium"}}`},
		{"model with characters that need escaping",
			`{"model":"a"}`, responses.Rewrite{Model: `q"<`},
			`{"model":"q\"\u003c"}`},
		{"empty rewrite keeps the body",
			`{"model":"a","reasoning":{"effort":"high"}}`, responses.Rewrite{},
			`{"model":"a","reasoning":{"effort":"high"}}`},
		{"same values keep the body",
			`{"model":"a","reasoning":{"effort":"high"}}`, responses.Rewrite{Model: "a", Effort: config.EffortHigh},
			`{"model":"a","reasoning":{"effort":"high"}}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := mustParse(t, tt.body).Apply(tt.rw)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tt.want {
				t.Fatalf("got  %s\nwant %s", got, tt.want)
			}
			if !json.Valid(got) {
				t.Fatalf("output is not valid JSON: %s", got)
			}
		})
	}
}

func FuzzApply(f *testing.F) {
	f.Add(readFixture(f, "request.json"))
	f.Add(readFixture(f, "request_large.json"))
	f.Add([]byte(`{"model":"a"}`))
	f.Add([]byte(`{"model":"a","reasoning":null}`))
	f.Add([]byte(`{ "reasoning" : { } , "model" : "a\"b" }`))
	f.Fuzz(func(t *testing.T, body []byte) {
		r, err := responses.Parse(body)
		if err != nil {
			return
		}
		out, err := r.Apply(responses.Rewrite{Model: "routed", Effort: config.EffortLow})
		if err != nil {
			t.Fatalf("Apply after a successful Parse: %v", err)
		}
		got, err := responses.Parse(out)
		if err != nil {
			t.Fatalf("Parse of rewritten body: %v\n%s", err, out)
		}
		if got.Model != "routed" || got.Effort != config.EffortLow || len(got.Input) != len(r.Input) {
			t.Fatalf("rewritten body reads model=%q effort=%q items=%d", got.Model, got.Effort, len(got.Input))
		}
	})
}

func TestApplyWithoutParseFails(t *testing.T) {
	var r responses.Request
	if _, err := r.Apply(responses.Rewrite{Model: "b"}); err == nil {
		t.Fatal("Apply on an unparsed request succeeded")
	}
}
