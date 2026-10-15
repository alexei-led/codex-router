// Package sessions finds the running sessions of every router in the
// registry, or of one control socket, and picks one: by id or label for the
// panel, by thread id or working dir for a shell command. It also sends a
// command to the endpoint that listed a session, and tells a router a
// sandbox hides from a dead one.
package sessions
