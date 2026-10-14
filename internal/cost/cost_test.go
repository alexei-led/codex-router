package cost

import (
	"fmt"
	"math"
	"sync"
	"testing"
	"time"

	"github.com/alexei-led/codex-router/internal/config"
	"github.com/alexei-led/codex-router/internal/responses"
)

var (
	t0        = time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	fiveMin   = Cache{TTL: 5 * time.Minute, WarmMargin: 30 * time.Second}
	sol, luna = "gpt-6-sol", "gpt-6-luna"
)

func input(tokens int) responses.Usage {
	return responses.Usage{InputTokens: tokens, TotalTokens: tokens}
}

func TestSwitchTaxByCandidateWarmth(t *testing.T) {
	cases := []struct {
		name string
		age  time.Duration
		ran  bool
		toks int
		want int
	}{
		{"right after the response", 0, true, 20_000, 0},
		{"just inside TTL minus margin", 4*time.Minute + 29*time.Second, true, 20_000, 0},
		{"at TTL minus margin", 4*time.Minute + 30*time.Second, true, 20_000, 20_000},
		{"after TTL", 6 * time.Minute, true, 20_000, 20_000},
		{"before the response", -time.Second, true, 20_000, 20_000},
		{"a model the thread never ran", 0, false, 0, 20_000},
		{"a response with no input tokens", 0, true, 0, 20_000},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			l := NewLedger(fiveMin)
			if tc.ran {
				l.Observe("th", luna, input(tc.toks), t0.Add(-tc.age))
			}
			l.Observe("th", sol, input(20_000), t0)
			if got := l.SwitchTax("th", luna, 20_000, t0); got != tc.want {
				t.Fatalf("SwitchTax = %d, want %d", got, tc.want)
			}
			if got := l.SwitchTax("other", luna, 20_000, t0); got != 0 {
				t.Fatalf("SwitchTax on another thread = %d, want 0", got)
			}
		})
	}
}

func TestSwitchTax(t *testing.T) {
	type obs struct {
		model  string
		tokens int
		ago    time.Duration
	}
	cases := []struct {
		name      string
		history   []obs
		candidate string
		tokens    int
		want      int
	}{
		{"no history: nothing to protect", nil, luna, 30_000, 0},
		{"staying on the current model", []obs{{sol, 20_000, time.Minute}}, sol, 30_000, 0},
		{"a cold candidate rewrites the warm prefix", []obs{{sol, 20_000, time.Minute}}, luna, 30_000, 20_000},
		{"the prefix counts only up to the request", []obs{{sol, 20_000, time.Minute}}, luna, 15_000, 15_000},
		{
			"a candidate warm on an older prefix pays the difference",
			[]obs{{luna, 12_000, 3 * time.Minute}, {sol, 20_000, time.Minute}},
			luna, 30_000, 8_000,
		},
		{
			"a detour back to a warm model is free",
			[]obs{{luna, 20_000, 3 * time.Minute}, {sol, 20_000, time.Minute}},
			luna, 30_000, 0,
		},
		{
			"a candidate whose cache expired is cold",
			[]obs{{luna, 20_000, 10 * time.Minute}, {sol, 20_000, time.Minute}},
			luna, 30_000, 20_000,
		},
		{"a cold current model protects nothing", []obs{{sol, 20_000, 10 * time.Minute}}, luna, 30_000, 0},
		{"a negative size costs nothing", []obs{{sol, 20_000, time.Minute}}, luna, -1, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			l := NewLedger(fiveMin)
			for _, o := range tc.history {
				l.Observe("th", o.model, input(o.tokens), t0.Add(-o.ago))
			}
			if got := l.SwitchTax("th", tc.candidate, tc.tokens, t0); got != tc.want {
				t.Fatalf("SwitchTax = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestForget(t *testing.T) {
	l := NewLedger(fiveMin)
	l.Observe("th", luna, input(12_000), t0.Add(-time.Minute))
	l.Observe("th", sol, input(20_000), t0)
	if got := l.SwitchTax("th", luna, 30_000, t0); got != 8_000 {
		t.Fatalf("SwitchTax before Forget = %d, want 8000", got)
	}
	l.Forget("th")

	if got := l.SwitchTax("th", luna, 30_000, t0); got != 0 {
		t.Fatalf("SwitchTax after Forget = %d, want 0", got)
	}
}

func TestLedgerConcurrentUse(t *testing.T) {
	l := NewLedger(fiveMin)
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Go(func() {
			thread := fmt.Sprint("th", i%2)
			for j := range 100 {
				l.Observe(thread, sol, input(j+1), t0)
				l.SwitchTax(thread, luna, j, t0)
			}
		})
	}
	wg.Wait()
	for _, thread := range []string{"th0", "th1"} {
		if got := l.SwitchTax(thread, luna, 1000, t0); got != 100 {
			t.Fatalf("SwitchTax(%s) = %d, want 100", thread, got)
		}
	}
}

func TestUSD(t *testing.T) {
	priced := config.Price{Input: 2, CachedInput: 0.5, Output: 10}
	cases := []struct {
		name  string
		price config.Price
		usage responses.Usage
		want  float64
	}{
		{"unpriced", config.Price{}, responses.Usage{InputTokens: 1_000_000, OutputTokens: 1_000_000}, 0},
		{"uncached input only", priced, responses.Usage{InputTokens: 1_000_000}, 2},
		{
			"cached and uncached input with output",
			priced,
			responses.Usage{InputTokens: 1_000_000, CachedInputTokens: 600_000, OutputTokens: 100_000},
			0.8 + 0.3 + 1,
		},
		{"cached above input is capped", priced, responses.Usage{InputTokens: 1_000, CachedInputTokens: 5_000}, 0.0005},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := USD(tc.price, tc.usage); math.Abs(got-tc.want) > 1e-9 {
				t.Fatalf("USD = %v, want %v", got, tc.want)
			}
		})
	}
}
