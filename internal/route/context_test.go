package route

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/alexei-led/codex-router/internal/classifier"
	"github.com/alexei-led/codex-router/internal/config"
	"github.com/alexei-led/codex-router/internal/responses"
)

func parsedContextRequest(t *testing.T, suffix string) *responses.Request {
	t.Helper()
	items := []any{map[string]any{"type": "reasoning", "encrypted_content": strings.Repeat("x", 1_600_000)}}
	items = append(items, map[string]any{"type": "message", "role": "user", "content": "continue"})
	if suffix != "" {
		items = append(items, map[string]any{"type": "function_call_output", "call_id": "call-1", "output": suffix})
	}
	body, err := json.Marshal(map[string]any{"model": high.Model, "reasoning": map[string]string{"effort": string(high.Effort)}, "input": items})
	if err != nil {
		t.Fatal(err)
	}
	r, err := responses.Parse(body)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestMeasuredPrefixDoesNotChargeSerializedHistoryAgain(t *testing.T) {
	h := newHarness(t, testConfig())
	h.asker.script(answer(tier(config.TierHigh, 1)))
	m := meta("th", "t1")
	m.RequestID = "request-1"
	h.decide(parsedContextRequest(t, ""), m)
	h.engine.Observe(m, high.Model, responses.Usage{InputTokens: 140_000, OutputTokens: 100, TotalTokens: 140_100}, 200)
	h.asker.script(answer(tier(config.TierMicro, 1)))
	m = meta("th", "t2")
	m.RequestID = "request-2"
	if err := h.engine.Apply(Command{Op: OpPin, Session: "th", Tier: config.TierMicro}); err != nil {
		t.Fatal(err)
	}
	h.decide(parsedContextRequest(t, "ok"), m)
	d := h.last()
	if d.Route.Model != low.Model {
		t.Fatalf("measured 140k prefix with small suffix: got %+v", d)
	}
}

func TestContextMeasurementsAreFenced(t *testing.T) {
	for _, name := range []string{"late reply", "missing request id", "http error", "no usage", "unexpected model", "compaction", "account change", "changed prefix", "large suffix", "large measurement"} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t, testConfig())
			h.asker.script(answer(tier(config.TierHigh, 1)))
			m := meta("th", "t1")
			m.RequestID, m.Account = "first", "account-a"
			h.decide(parsedContextRequest(t, ""), m)
			reply := m
			status, served, input := 200, high.Model, 140_000
			switch name {
			case "late reply":
				reply.RequestID = "old-request"
			case "missing request id":
				reply.RequestID = ""
			case "http error":
				status = 500
			case "no usage":
				input = 0
			case "unexpected model":
				served = "unknown-model"
			case "large measurement":
				input = 260_000
			}
			h.engine.Observe(reply, served, responses.Usage{InputTokens: input, TotalTokens: input}, status)
			if name == "compaction" {
				h.engine.PassThrough(compaction("th", "t1"))
			}
			m.TurnID, m.RequestID = "t2", "second"
			if name == "account change" {
				m.Account = "account-b"
			}
			r := parsedContextRequest(t, "ok")
			if name == "large suffix" {
				r = parsedContextRequest(t, strings.Repeat("y", 400_000))
			}
			if name == "changed prefix" {
				var err error
				r, err = responses.Parse([]byte(strings.Replace(string(r.Body()), "xxxxx", "yyyyy", 1)))
				if err != nil {
					t.Fatal(err)
				}
			}
			if err := h.engine.Apply(Command{Op: OpPin, Session: "th", Tier: config.TierMicro}); err != nil {
				t.Fatal(err)
			}
			h.decide(r, m)
			if h.last().Route.Model == low.Model {
				t.Fatal("unsafe context measurement admitted Luna")
			}
		})
	}
}

func TestNativeModelEffortChangeDoesNotNeedSmallerWindow(t *testing.T) {
	cfg := testConfig()
	cfg.Routes[config.TierMedium] = config.Route{Model: high.Model, Effort: config.EffortMedium}
	h := newHarness(t, cfg)
	if err := h.engine.Apply(Command{Op: OpPin, Session: "th", Tier: config.TierMedium}); err != nil {
		t.Fatal(err)
	}
	h.decide(parsedContextRequest(t, ""), meta("th", "t1"))
	if got := h.last().Route; got != cfg.Routes[config.TierMedium] {
		t.Fatalf("same-model effort change blocked: %+v", got)
	}
}

func TestLateResponseCannotMeasureNewerRequest(t *testing.T) {
	h := newHarness(t, testConfig())
	h.asker.script(answer(tier(config.TierHigh, 1)))
	first := meta("th", "t1")
	first.RequestID = "first"
	h.decide(parsedContextRequest(t, ""), first)
	second := first
	second.RequestID = "second"
	h.decide(parsedContextRequest(t, "new data"), second)
	h.engine.Observe(first, high.Model, responses.Usage{InputTokens: 100, TotalTokens: 100}, 200)
	if h.engine.thread("th").contextTotal != 0 {
		t.Fatal("late reply measured the newer request")
	}
	h.engine.Observe(second, high.Model, responses.Usage{InputTokens: 250_000, TotalTokens: 250_000}, 200)
	if got := h.engine.thread("th").contextTotal; got != 250_000 {
		t.Fatalf("current response measurement = %d", got)
	}
}

func TestCompactionWhileClassifyingCannotMeasureOldPrefix(t *testing.T) {
	h := newHarness(t, testConfig())
	entered, release := make(chan struct{}), make(chan struct{})
	h.asker.script(func() (classifier.Advice, error) {
		close(entered)
		<-release
		return tier(config.TierHigh, 1), nil
	})
	m := meta("th", "t1")
	m.RequestID = "before-compaction"
	r := parsedContextRequest(t, "")
	done := make(chan struct{})
	go func() {
		h.engine.Decide(context.Background(), r, m)
		close(done)
	}()
	<-entered
	compact := compaction("th", "t1")
	compact.RequestID = "compaction"
	h.engine.PassThrough(compact)
	close(release)
	<-done
	h.engine.Observe(compact, high.Model, responses.Usage{InputTokens: 100, TotalTokens: 100}, 200)
	if len(h.sink.Decisions()) != 0 {
		t.Fatal("superseded request committed a routing decision")
	}
	if err := h.engine.Apply(Command{Op: OpPin, Session: "th", Tier: config.TierMicro}); err != nil {
		t.Fatal(err)
	}
	next := meta("th", "t2")
	next.RequestID = "after-compaction"
	h.decide(parsedContextRequest(t, "ok"), next)
	if h.last().Route.Model == low.Model {
		t.Fatal("compaction measured the old unmodified prefix")
	}
}

func TestUnroutedReplyStillBoundsTheMeasuredEstimate(t *testing.T) {
	h := newHarness(t, testConfig())
	h.asker.script(answer(tier(config.TierHigh, 1)), fail(classifier.ErrTimeout))
	m := meta("th", "t1")
	m.RequestID = "first"
	h.decide(parsedContextRequest(t, ""), m)
	h.reply(m, high.Model, 140_000, 0)
	m.TurnID, m.RequestID = "t2", "unrouted"
	r := parsedContextRequest(t, strings.Repeat("y", 100_000))
	h.decide(r, m)
	h.reply(m, high.Model, 240_000, 0)
	if err := h.engine.Apply(Command{Op: OpPin, Session: "th", Tier: config.TierMicro}); err != nil {
		t.Fatal(err)
	}
	m.TurnID, m.RequestID = "t3", "third"
	h.decide(r, m)
	if h.last().Route.Model == low.Model {
		t.Fatal("older prefix estimate ignored newer 240k usage")
	}
}
