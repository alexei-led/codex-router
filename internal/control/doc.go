// Package control is the router's local control channel: JSON lines over a
// 0600 unix socket. The server pushes a full route.Snapshot on connect and
// after every change; clients send route.Command values. The TUI and the
// hook are clients. Nothing from Codex requests (headers, text) is on it.
package control
