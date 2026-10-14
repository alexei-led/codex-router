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

func TestWarm(t *testing.T) {
	l := NewLedger(fiveMin)
	l.Observe("th", sol, input(20_000), t0)
	l.Observe("th", "empty", input(0), t0)

	cases := []struct {
		name   string
		thread string
		model  string
		at     time.Time
		want   bool
	}{
		{"right after the response", "th", sol, t0, true},
		{"just inside TTL minus margin", "th", sol, t0.Add(4*time.Minute + 29*time.Second), true},
		{"at TTL minus margin", "th", sol, t0.Add(4*time.Minute + 30*time.Second), false},
		{"after TTL", "th", sol, t0.Add(6 * time.Minute), false},
		{"before the response", "th", sol, t0.Add(-time.Second), false},
		{"a model the thread never ran", "th", luna, t0, false},
		{"another thread", "other", sol, t0, false},
		{"a response with no input tokens", "th", "empty", t0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := l.Warm(tc.thread, tc.model, tc.at); got != tc.want {
				t.Fatalf("Warm = %v, want %v", got, tc.want)
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
	l.Observe("th", sol, input(20_000), t0)
	l.Forget("th")

	if l.Warm("th", sol, t0) {
		t.Fatal("forgotten thread is still warm")
	}
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
				l.Warm(thread, sol, t0)
			}
		})
	}
	wg.Wait()
	if !l.Warm("th0", sol, t0) || !l.Warm("th1", sol, t0) {
		t.Fatal("threads not warm after concurrent observes")
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
