// Package statedir is codex-router's state dir, $XDG_STATE_HOME/codex-router
// (else ~/.local/state/codex-router): where it is, the privacy rule for its
// dirs, the layout of a home's stage 2 dir in it, and atomic file writes.
// Setup writes a home's dir, the edge and the brain read it, and all three
// must agree on its layout and its check.
package statedir
