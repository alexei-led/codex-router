//go:build !darwin

package listen

import "net"

// launchd exists only on macOS.
func launchd(string) (net.Listener, error) { return nil, ErrNotActivated }
