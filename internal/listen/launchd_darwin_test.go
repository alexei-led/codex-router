package listen

import (
	"errors"
	"net"
	"os"
	"strings"
	"syscall"
	"testing"
	"unsafe"
)

func stubLibSystem(t *testing.T, l *libSystem, err error) {
	t.Helper()
	old := loadLibSystem
	loadLibSystem = func() (*libSystem, error) { return l, err }
	t.Cleanup(func() { loadLibSystem = old })
}

func activateRC(rc syscall.Errno) *libSystem {
	return &libSystem{
		activate: func(string, *unsafe.Pointer, *uint) int32 { return int32(rc) },
		free:     func(unsafe.Pointer) {},
	}
}

func dupFD(t *testing.T, f *os.File) int32 {
	t.Helper()
	fd, err := syscall.Dup(int(f.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	return int32(fd)
}

func TestLaunchdFailures(t *testing.T) {
	noFDs := &libSystem{
		activate: func(_ string, fds *unsafe.Pointer, n *uint) int32 { *fds, *n = nil, 0; return 0 },
		free:     func(unsafe.Pointer) {},
	}
	cases := []struct {
		name         string
		lib          *libSystem
		loadErr      error
		notActivated bool
		want         string
	}{
		{"not a launchd job", activateRC(syscall.ESRCH), nil, true, "no such process"},
		{"no socket by that name", activateRC(syscall.ENOENT), nil, true, "no such file"},
		{"launchd refuses", activateRC(syscall.EPERM), nil, false, "not permitted"},
		{"no fds passed", noFDs, nil, true, "no fds"},
		{"libSystem unavailable", nil, errors.New("dlopen failed"), false, "dlopen failed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stubLibSystem(t, tc.lib, tc.loadErr)
			ln, err := launchd("edge")
			if ln != nil {
				_ = ln.Close()
				t.Fatal("launchd returned a listener")
			}
			if got := errors.Is(err, ErrNotActivated); got != tc.notActivated {
				t.Errorf("errors.Is(%v, ErrNotActivated) = %v, want %v", err, got, tc.notActivated)
			}
			if err == nil || !strings.Contains(strings.ToLower(err.Error()), tc.want) {
				t.Errorf("err = %v, want it to mention %q", err, tc.want)
			}
		})
	}
}

func TestActivatedFallsBackToLaunchdAndClosesExtraSockets(t *testing.T) {
	t.Setenv(envPID, "")
	t.Setenv(envFDs, "")
	first, firstFile := tcpListener(t)
	extraRead, extraWrite, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = extraWrite.Close() })
	fds := []int32{dupFD(t, firstFile), dupFD(t, extraRead)}
	_ = extraRead.Close()
	stubLibSystem(t, &libSystem{
		activate: func(name string, out *unsafe.Pointer, n *uint) int32 {
			if name != "edge" {
				t.Errorf("activated %q, want edge", name)
			}
			*out, *n = unsafe.Pointer(&fds[0]), uint(len(fds))
			return 0
		},
		free: func(unsafe.Pointer) {},
	}, nil)

	ln, err := Activated("edge")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	if got, want := port(ln), port(first); got != want {
		t.Errorf("port = %d, want %d", got, want)
	}
	if _, ok := ln.(*net.TCPListener); !ok {
		t.Errorf("listener is %T, want *net.TCPListener", ln)
	}
	if _, err := extraWrite.Write([]byte("x")); !errors.Is(err, syscall.EPIPE) {
		t.Errorf("write to the extra fd's pipe = %v, want EPIPE: the fd is still open", err)
	}
}
