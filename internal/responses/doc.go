// Package responses models the Codex Responses request as the proxy sees it:
// the body fields the router reads or rewrites (model, reasoning.effort, input
// items), the turn metadata header, the request kind, and the usage reported
// in the response.completed SSE event. Every other byte passes through.
package responses
