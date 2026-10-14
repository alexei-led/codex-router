package listen

import (
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"syscall"
)

// systemd socket activation variables (sd_listen_fds(3)).
const (
	envPID     = "LISTEN_PID"
	envFDs     = "LISTEN_FDS"
	envFDNames = "LISTEN_FDNAMES"
	firstFD    = 3
)

// systemd returns the socket systemd passed under name. Passed fds start at
// 3; LISTEN_FDNAMES, when set, names them in order, else the first is
// taken. Variables meant for another pid are ignored. All three are
// cleared, and every passed fd is close-on-exec.
func systemd(name string) (net.Listener, error) {
	pid, count, names := os.Getenv(envPID), os.Getenv(envFDs), os.Getenv(envFDNames)
	for _, k := range []string{envPID, envFDs, envFDNames} {
		_ = os.Unsetenv(k)
	}
	if pid == "" && count == "" {
		return nil, ErrNotActivated
	}
	if p, err := strconv.Atoi(pid); err != nil || p != os.Getpid() {
		return nil, fmt.Errorf("%w: %s=%q is not this process", ErrNotActivated, envPID, pid)
	}
	n, err := strconv.Atoi(count)
	if err != nil || n < 1 {
		return nil, fmt.Errorf("%w: %s=%q", ErrNotActivated, envFDs, count)
	}
	for i := range n {
		syscall.CloseOnExec(firstFD + i)
	}
	if names == "" {
		return fileListener(firstFD, name)
	}
	for i, got := range strings.Split(names, ":") {
		if got == name && i < n {
			return fileListener(firstFD+i, name)
		}
	}
	return nil, fmt.Errorf("listen: systemd passed no socket named %q (%s=%q)", name, envFDNames, names)
}
