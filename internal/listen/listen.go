package listen

import (
	"errors"
	"fmt"
	"net"
	"os"
)

// ErrNotActivated means no service manager passed a socket: the caller binds
// one itself.
var ErrNotActivated = errors.New("listen: not socket-activated")

// Activated returns the socket the service manager opened under name: the
// systemd FileDescriptorName or the launchd Sockets key. It tries systemd
// first (its variables are cleared either way, so children do not inherit
// them), then launchd on macOS. Neither gives an error wrapping
// ErrNotActivated.
func Activated(name string) (net.Listener, error) {
	ln, err := systemd(name)
	if !errors.Is(err, ErrNotActivated) {
		return ln, err
	}
	return launchd(name)
}

// fileListener turns an inherited fd into a listener. net.FileListener dups
// it, so the original is closed here.
func fileListener(fd int, name string) (net.Listener, error) {
	f := os.NewFile(uintptr(fd), name)
	if f == nil {
		return nil, fmt.Errorf("listen: fd %d is not valid", fd)
	}
	defer func() { _ = f.Close() }()
	ln, err := net.FileListener(f)
	if err != nil {
		return nil, fmt.Errorf("listen: listener from fd %d: %w", fd, err)
	}
	return ln, nil
}
