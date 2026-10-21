// Package control is the router's local control channel, protocol v2: JSON
// lines over a 0600 unix socket. A client opens with a hello naming the
// protocol version and gets the endpoint's identity back. The server then
// pushes the sessions list on change and the snapshots of the one session
// the client subscribed to; commands name a session and an id, and the ack
// or error echoes the id. The TUI and the hook are clients. Nothing from
// Codex requests (headers, text) is on it. The names in a payload are the
// protocol, so every wire type spells its JSON names out and the fixtures in
// testdata/protocol-v2 pin them byte for byte, in both directions.
package control
