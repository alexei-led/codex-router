// Package appserver is a second client of a Codex app-server: the session's
// private one in stage 1, or a home's shared daemon in stage 2. It speaks
// JSON-RPC 2.0 over a Dialer (WebSocket over the unix socket per research
// §3.6), initializes with experimentalApi, resumes each thread (thread/resume
// with excludeTurns) so it receives that thread's notifications, writes
// thread/settings/update so the Codex footer shows the routed model, and
// surfaces thread/settings/updated notifications so the router can tell its
// own echo from a manual /model pin. For the daemon it also lists and reads
// loaded threads, unsubscribes, and surfaces thread and turn notifications.
package appserver
