// Package launcher runs one routed Codex session: it makes the runtime dir,
// starts and probes the proxy, starts the private app-server with -c
// overrides only, connects the router client, wires the engine to the
// app-server and control socket, then runs `codex --remote`. If any startup
// step fails it runs plain codex and says why.
package launcher
