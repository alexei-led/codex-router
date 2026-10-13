// Package facts derives what the policy needs from one Responses request:
// the prompt and recent dialogue for the classifier, a repeated tool-failure
// signature, the activity the tools show, whether a verification passed, and
// a context size estimate. Pure: request in, Facts out; no text is kept
// beyond the clipped dialogue. Ported from claude-model-router lib/facts.mjs.
package facts
