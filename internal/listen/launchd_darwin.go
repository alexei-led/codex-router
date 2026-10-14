package listen

import (
	"errors"
	"fmt"
	"net"
	"sync"
	"syscall"
	"unsafe"

	"github.com/ebitengine/purego"
)

const libSystemPath = "/usr/lib/libSystem.B.dylib"

type libSystem struct {
	activate func(name string, fds *unsafe.Pointer, cnt *uint) int32
	free     func(p unsafe.Pointer)
}

// loadLibSystem binds launch_activate_socket through purego, so the binary
// needs no cgo and still cross-builds with CGO_ENABLED=0.
var loadLibSystem = sync.OnceValues(func() (*libSystem, error) {
	lib, err := purego.Dlopen(libSystemPath, purego.RTLD_NOW|purego.RTLD_GLOBAL)
	if err != nil {
		return nil, fmt.Errorf("listen: dlopen libSystem: %w", err)
	}
	var l libSystem
	purego.RegisterLibFunc(&l.activate, lib, "launch_activate_socket")
	purego.RegisterLibFunc(&l.free, lib, "free")
	return &l, nil
})

// launchd returns the socket launchd opened for this job under name
// (launch_activate_socket(3)). ESRCH (not a launchd job) and ENOENT (no
// socket by that name) wrap ErrNotActivated. The units ask for one IPv4
// socket; any extra fd is closed.
func launchd(name string) (net.Listener, error) {
	l, err := loadLibSystem()
	if err != nil {
		return nil, err
	}
	var fds unsafe.Pointer
	var n uint
	if rc := l.activate(name, &fds, &n); rc != 0 {
		errno := syscall.Errno(rc)
		err := fmt.Errorf("launch_activate_socket %q: %w", name, errno)
		if errors.Is(errno, syscall.ESRCH) || errors.Is(errno, syscall.ENOENT) {
			return nil, fmt.Errorf("%w: %w", ErrNotActivated, err)
		}
		return nil, fmt.Errorf("listen: %w", err)
	}
	defer l.free(fds)
	if n == 0 {
		return nil, fmt.Errorf("%w: launchd passed no fds for %q", ErrNotActivated, name)
	}
	all := unsafe.Slice((*int32)(fds), n)
	for _, fd := range all[1:] {
		_ = syscall.Close(int(fd))
	}
	return fileListener(int(all[0]), name)
}
