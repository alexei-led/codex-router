package route

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/alexei-led/codex-router/internal/config"
)

func TestReplyDiagnosticsSeparateSelectionResponseAndPriceCoverage(t *testing.T) {
	for _, tc := range []struct {
		name, response, costModel, source string
		priced                            bool
	}{
		{"response alias priced", low.Model + "-snapshot", low.Model + "-snapshot", "configured-price-estimate", true},
		{"absent response falls back to selection", "", low.Model, "configured-price-estimate", true},
		{"unknown backend has no coverage", "unknown-backend", "unknown-backend", "unpriced", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var logs bytes.Buffer
			c := testConfig()
			price := config.Price{Input: 1, CachedInput: 0.1, Output: 5}
			c.Prices = map[string]config.Price{medium.Model: price, low.Model: price}
			if tc.priced && tc.response != "" {
				c.Prices[tc.response] = price
			}
			h := newHarness(t, c, func(d *Deps) {
				d.Logger = slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
			})
			h.asker.script(answer(tier("low", 0.99)))
			m := meta("th", "t1")
			h.decide(req(medium, userMsg(secretPrompt)), m)
			h.reply(m, tc.response, 1000, 400)
			var reply map[string]any
			for line := range strings.SplitSeq(logs.String(), "\n") {
				var entry map[string]any
				if json.Unmarshal([]byte(line), &entry) == nil && entry["msg"] == "route reply" {
					reply = entry
				}
			}
			for k, want := range map[string]string{
				"requestedModel": medium.Model, "requestedEffort": string(medium.Effort),
				"selectedModel": low.Model, "selectedEffort": string(low.Effort),
				"responseModel": tc.response, "costModel": tc.costModel,
				"costBaselineModel": medium.Model, "priceSource": tc.source,
			} {
				if reply[k] != want {
					t.Errorf("%s=%v, want %q: %s", k, reply[k], want, logs.String())
				}
			}
			if _, ok := reply["servedEffort"]; ok {
				t.Fatal("invented served effort")
			}
			if reply["cachedInputTokens"] != float64(400) {
				t.Fatalf("usage missing: %v", reply)
			}
			s := h.engine.SnapshotFor("th")
			if (s.Stats.PricedReplies == 1) != tc.priced || s.Stats.EligibleReplies != 1 {
				t.Fatalf("coverage disagrees with diagnostics: %+v", s.Stats)
			}
			if strings.Contains(logs.String(), secretPrompt) {
				t.Fatal("prompt logged")
			}
		})
	}
}
