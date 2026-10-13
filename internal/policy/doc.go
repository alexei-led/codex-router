// Package policy decides the tier for a step: escalation on a repeated tool
// failure, holds, votes with hysteresis, cost-gated upgrade and downgrade
// bars, and the context-fit tier search. Pure: Input in, Result (with the
// next State) out. Ported from claude-model-router lib/policy.mjs with taxes
// in uncached tokens.
package policy
