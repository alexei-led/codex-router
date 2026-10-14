// Package service installs, loads and checks the stage 2 service units of one
// Codex home: launchd agents on macOS, systemd user units on Linux. Each home
// gets an edge (socket-activated: the service manager holds the provider
// port, plan decision M8) and a brain. Unit names carry the home id, so homes
// never share a unit. launchctl and systemctl run through an injected Runner.
package service
