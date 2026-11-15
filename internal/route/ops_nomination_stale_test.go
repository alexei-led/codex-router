package route

import (
	"encoding/json"
	"testing"

	"github.com/alexei-led/codex-router/internal/classifier"
	"github.com/alexei-led/codex-router/internal/config"
	"github.com/alexei-led/codex-router/internal/responses"
)

func TestStaleOpsReviewCannotNominateAnExplicitFloor(t *testing.T) {
	c := mixedOpsConfig()
	c.Policy.ReviewEveryRequests = 1
	a := &typedAsker{adapter: classifier.SystemOne(), answers: []string{
		opsAnswer("high", "execute", "0.99", "0"),
		opsAnswer("high", "reason", "0.99", "0.99"),
	}}
	h := mixedOpsHarness(t, c, a)
	items := []map[string]string{{"type": "message", "role": "user", "content": "Operate the service"}}
	request := func() *responses.Request {
		body, err := json.Marshal(map[string]any{"model": "glm-flash", "reasoning": map[string]string{"effort": "low"}, "input": items})
		if err != nil {
			t.Fatal(err)
		}
		r, err := responses.Parse(body)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	m := meta("th", "t1")
	h.decide(request(), m)
	want := c.RouteFor(config.TierHigh, config.ActivityOps)
	h.reply(m, want.Model, 10000, 0)
	items = append(items, map[string]string{"type": "message", "role": "assistant", "content": "Investigate the unexpected result."})
	h.decide(request(), m)
	h.engine.reviews.Wait()
	items = append(items, map[string]string{"type": "message", "role": "assistant", "content": "Investigation is finished. Only verify the release now."})
	h.decide(request(), m)
	if d := h.last(); d.Route != want || d.EffectiveActivity != config.ActivityOps || d.Reading.Phase != "" {
		t.Fatalf("stale phase nominated an unordered floor: %+v", d)
	}
}
