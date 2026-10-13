// Package appserver is a second client of the session's private Codex
// app-server: JSON-RPC 2.0 over a Dialer (WebSocket over the unix socket per
// research §3.6, with a newline JSON-RPC fallback). It initializes with
// experimentalApi, writes thread/settings/update so the Codex footer shows
// the routed model, and surfaces thread/settings/updated notifications so the
// router can tell its own echo from a manual /model pin.
package appserver
