// Package registry is the shared list of running routers:
// $XDG_STATE_HOME/codex-router/registry (else ~/.local/state/...), one
// <endpoint_id>.json file per control endpoint. Every shell sees the same
// dir whatever its TMPDIR. An entry is only a hint: clients confirm it with a
// hello handshake, and readers prune entries whose endpoint is gone.
package registry
