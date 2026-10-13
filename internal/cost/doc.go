// Package cost keeps the prompt-cache ledger per thread and model and prices
// a switch in uncached tokens, with USD only when a model has list prices.
// Codex's cache is per model and effort-neutral (research spikes 3 and 5).
package cost
