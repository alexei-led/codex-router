package main

import (
	"errors"
	"fmt"
	"net"
	"syscall"
	"unsafe"

	"github.com/ebitengine/purego"
)

// activated asks launchd for the sockets it opened for this job under name
// (launch_activate_socket(3)), through purego so the binary needs no cgo.
func activated(name string) (net.Listener, error) {
	lib, err := purego.Dlopen("/usr/lib/libSystem.B.dylib", purego.RTLD_NOW|purego.RTLD_GLOBAL)
	if err != nil {
		return nil, fmt.Errorf("dlopen libSystem: %w", err)
	}
	var activate func(name string, fds *unsafe.Pointer, cnt *uint) int32
	var free func(p unsafe.Pointer)
	purego.RegisterLibFunc(&activate, lib, "launch_activate_socket")
	purego.RegisterLibFunc(&free, lib, "free")

	var fds unsafe.Pointer
	var n uint
	if rc := activate(name, &fds, &n); rc != 0 {
		// ESRCH: not started by launchd; ENOENT: no socket with that name.
		return nil, fmt.Errorf("launch_activate_socket %q: %w", name, syscall.Errno(rc))
	}
	defer free(fds)
	if n == 0 {
		return nil, errors.New("launch_activate_socket: no fds")
	}
	// The plist asks for one IPv4 socket; any extra fd stays open unused.
	return fileListener(int(unsafe.Slice((*int32)(fds), n)[0]), name)
}
