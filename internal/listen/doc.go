// Package listen takes a listening socket from the service manager (plan
// decision M8): systemd socket activation on Linux, launchd Sockets on
// macOS. The service manager keeps the port bound while the process is down,
// so a crash or an upgrade never frees it.
package listen
