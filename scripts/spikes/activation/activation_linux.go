package main

import (
	"errors"
	"net"
	"os"
	"strconv"
)

// activated takes the first socket systemd passed (sd_listen_fds(3): fds
// start at 3) and clears the variables so children do not inherit them.
// name is unused: the spike socket unit has one socket.
func activated(name string) (net.Listener, error) {
	pid, _ := strconv.Atoi(os.Getenv("LISTEN_PID"))
	n, _ := strconv.Atoi(os.Getenv("LISTEN_FDS"))
	for _, k := range []string{"LISTEN_PID", "LISTEN_FDS", "LISTEN_FDNAMES"} {
		_ = os.Unsetenv(k)
	}
	if pid != os.Getpid() || n < 1 {
		return nil, errors.New("not socket-activated by systemd")
	}
	return fileListener(3, name)
}
