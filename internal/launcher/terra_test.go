package launcher

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/alexei-led/codex-router/internal/catalog"
	"github.com/alexei-led/codex-router/internal/config"
	"github.com/alexei-led/codex-router/internal/control"
	"github.com/alexei-led/codex-router/internal/policy"
	"github.com/alexei-led/codex-router/internal/route"
)

func terraE2E(t *testing.T, mode config.RoutingMode, tier string) (*e2e, *atomic.Int32) {
	t.Helper()
	calls := &atomic.Int32{}
	cls := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		_, _ = fmt.Fprintf(w, `{"answers":{"route":{"type":"choice","choice":%q,"probabilities":{%q:1}},"activity":{"type":"choice","choice":"debug","probabilities":{"debug":1}},"continuation":{"type":"noul","noul":0}}}`, tier, tier)
	}))
	t.Cleanup(cls.Close)
	e := newE2E(t, func(h *harness) {
		h.opts.Config.Mode = mode
		cc := h.opts.Config.Classifiers[h.opts.Config.Classifier]
		cc.Endpoint = cls.URL
		h.opts.Config.Classifiers[h.opts.Config.Classifier] = cc
		path := filepath.Join(h.opts.getenv("CODEX_HOME"), catalog.CacheFile)
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var c struct {
			Models []catalog.Model `json:"models"`
		}
		if err := json.Unmarshal(data, &c); err != nil {
			t.Fatal(err)
		}
		terra := c.Models[0]
		terra.Slug = "gpt-6.1-terra"
		c.Models = append(c.Models, terra)
		data, err = json.Marshal(c)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
	})
	return e, calls
}

func terraRequest(turn string, failures, continuation bool) request {
	items := []string{`{"type":"message","role":"user","content":[{"type":"input_text","text":"fix the retry test"}]}`}
	if failures {
		items = append(items,
			`{"type":"function_call","name":"exec_command","call_id":"a","arguments":"{\"cmd\":\"go test ./...\"}"}`,
			`{"type":"function_call_output","call_id":"a","output":"Process exited with code 1\nOutput:\nFAIL fixture"}`,
			`{"type":"custom_tool_call","name":"apply_patch","call_id":"edit","input":"*** Begin Patch\n*** Update File: retry.go\n@@\n-a\n+b\n*** End Patch\n"}`,
			`{"type":"custom_tool_call_output","call_id":"edit","output":"Success."}`,
			`{"type":"function_call","name":"exec_command","call_id":"b","arguments":"{\"cmd\":\"go test ./...\"}"}`,
			`{"type":"function_call_output","call_id":"b","output":"Process exited with code 1\nOutput:\nFAIL fixture"}`)
	}
	if continuation {
		items = append(items, `{"type":"function_call","name":"exec_command","call_id":"read","arguments":"{\"cmd\":\"ls\"}"}`, `{"type":"function_call_output","call_id":"read","output":"Process exited with code 0\nOutput:\nretry.go"}`)
	}
	return request{turn: turn, kind: "turn", raw: []byte(`{"model":"gpt-6.1-sol","reasoning":{"effort":"high"},"stream":true,"input":[` + strings.Join(items, ",") + `]}`)}
}

func TestE2ETerraRecoveryReachesUpstreamAndFooter(t *testing.T) {
	e, calls := terraE2E(t, config.ModeCost, "high")
	first := terraRequest("turn-1", false, false)
	recovery := terraRequest("turn-1", true, false)
	later := terraRequest("turn-1", true, true)
	e.mustPost(t, first)
	e.mustPost(t, recovery)
	e.mustPost(t, recovery)
	e.mustPost(t, later)
	terra := config.Route{Model: "gpt-6.1-terra", Effort: config.EffortXHigh}
	sol := config.Route{Model: "gpt-6.1-sol", Effort: config.EffortXHigh}
	e.wantRoutes(t, terra, sol, sol, sol)
	snap := e.eventually(t, "recovery footer synced", func(s route.Snapshot) bool {
		return s.FooterSynced && s.Footer == sol && s.Last != nil && s.Last.Route == sol
	})
	if snap.RoutingMode != config.ModeCost || snap.Last.Tier != config.TierHigh {
		t.Fatalf("recovery lost mode/tier: %+v", snap.Last)
	}
	if calls.Load() != 1 {
		t.Fatalf("classifier calls=%d, want1", calls.Load())
	}
	decisions := snap.Recent
	found := false
	for _, d := range decisions {
		if d.Reason == policy.ReasonEscalation && d.Route == sol {
			found = true
		}
	}
	if !found {
		t.Fatal("actual upstream recovery absent from inspector")
	}
	t.Logf("controlled classifier + fake upstream: cost/high/debug %v -> recovery %v -> retry %v -> continuation %v; footer %v", terra, sol, sol, sol, snap.Footer)
}

func TestE2ETerraModeChangeReachesUpstreamAndFooter(t *testing.T) {
	e, _ := terraE2E(t, config.ModeBalanced, "medium")
	e.mustPost(t, terraRequest("turn-1", false, false))
	terra := config.Route{Model: "gpt-6.1-terra", Effort: config.EffortXHigh}
	e.eventually(t, "Terra footer synced", func(s route.Snapshot) bool { return s.FooterSynced && s.Footer == terra })
	e.command(t, route.Command{Op: route.OpMode, Session: control.SessionID(e.ctl.Hello().Home.ID, fakeThread), Mode: config.ModeQuality})
	e.mustPost(t, terraRequest("turn-2", false, false))
	e.mustPost(t, terraRequest("turn-2", false, true))
	sol := config.Route{Model: "gpt-6.1-sol", Effort: config.EffortHigh}
	e.wantRoutes(t, terra, sol, sol)
	snap := e.eventually(t, "quality footer synced", func(s route.Snapshot) bool { return s.FooterSynced && s.Footer == sol })
	if snap.Last == nil || snap.Last.Route != sol || snap.RoutingMode != config.ModeQuality {
		t.Fatalf("inspector=%+v", snap.Last)
	}
	t.Logf("controlled classifier + fake upstream: balanced/medium/debug %v -> quality %v -> continuation %v; footer %v", terra, sol, sol, snap.Footer)
}
