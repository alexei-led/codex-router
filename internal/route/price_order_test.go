package route

import (
	"testing"

	"github.com/alexei-led/codex-router/internal/config"
	"github.com/alexei-led/codex-router/internal/cost"
	"github.com/alexei-led/codex-router/internal/responses"
)

func TestTariffsAffectEconomicsNotCapability(t *testing.T) {
	c := testConfig()
	for _, price := range []float64{0.01, 100} {
		c.Prices = map[string]config.Price{
			low.Model:       {Input: price, CachedInput: price / 10, Output: price},
			medium.Model:    {Input: 1, CachedInput: 0.1, Output: 1},
			"activity-only": {Input: 1000, CachedInput: 100, Output: 1000},
		}
		for name, order := range map[string]func(config.Config, config.Route, config.Route) (int, bool){
			"activity": compareRoutes, "mode gate": modeOrder,
		} {
			if o, ok := order(c, medium, low); !ok || o != 1 {
				t.Fatalf("%s inverted capability at price %g: %d %v", name, price, o, ok)
			}
			if _, ok := order(c, config.Route{Model: "activity-only", Effort: config.EffortHigh}, medium); ok {
				t.Fatalf("%s inferred capability from a tariff", name)
			}
		}
		if got := cheaperInput(c, low.Model, medium.Model); got != (price < 1) {
			t.Fatalf("cost lever ignores tariff %g", price)
		}
		if got := cost.USD(c.Prices[low.Model], responses.Usage{InputTokens: 1_000_000}); got != price {
			t.Fatalf("USD=%g, want %g", got, price)
		}
	}
}
