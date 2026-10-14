// Package home resolves the Codex home a router serves ($CODEX_HOME, else
// ~/.codex) into a stable identity: a short ID hashed from the canonical path
// and a display name. Sessions are keyed by (home ID, thread ID), so two
// homes never share state even when their thread IDs collide.
package home
