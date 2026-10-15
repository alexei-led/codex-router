package listen

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

const (
	envChild = "LISTEN_TEST_CHILD"
	reply    = "served by the child"
)

func TestActivatedChild(t *testing.T) {
	name := os.Getenv(envChild)
	if name == "" {
		t.Skip("child of TestSystemdActivation")
	}
	ln, err := Activated(name)
	if err != nil {
		fmt.Printf("error=%v\n", err)
		return
	}
	defer func() { _ = ln.Close() }()
	left := os.Getenv(envPID) + os.Getenv(envFDs) + os.Getenv(envFDNames)
	fmt.Printf("port=%d left=%q\n", ln.Addr().(*net.TCPAddr).Port, left)
	c, err := ln.Accept()
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.WriteString(c, reply)
	_ = c.Close()
}

func tcpListener(t *testing.T) (*net.TCPListener, *os.File) {
	t.Helper()
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	f, err := ln.(*net.TCPListener).File()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close() })
	return ln.(*net.TCPListener), f
}

func port(ln net.Listener) int { return ln.Addr().(*net.TCPAddr).Port }

func TestSystemdActivation(t *testing.T) {
	cases := []struct {
		name    string
		sockets int
		fdNames string
		want    int
	}{
		{"one socket, no names", 1, "", 0},
		{"named socket among two", 2, "other:edge", 1},
		{"first named socket", 2, "edge:other", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var lns []*net.TCPListener
			var files []*os.File
			for range tc.sockets {
				ln, f := tcpListener(t)
				lns, files = append(lns, ln), append(files, f)
			}
			cmd := exec.Command("/bin/sh", "-c", `export LISTEN_PID=$$; exec "$0" "$@"`,
				os.Args[0], "-test.run=^TestActivatedChild$", "-test.v")
			cmd.ExtraFiles = files
			cmd.Env = append(os.Environ(), envChild+"=edge", envFDs+"="+strconv.Itoa(tc.sockets))
			if tc.fdNames != "" {
				cmd.Env = append(cmd.Env, envFDNames+"="+tc.fdNames)
			}
			out, err := cmd.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })

			line := readLine(t, out, "port=", "error=")
			want := fmt.Sprintf("port=%d left=%q", port(lns[tc.want]), "")
			if line != want {
				t.Fatalf("child said %q, want %q", line, want)
			}
			c, err := net.DialTimeout("tcp4", lns[tc.want].Addr().String(), 5*time.Second)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = c.Close() }()
			_ = c.SetDeadline(time.Now().Add(5 * time.Second))
			got, _ := io.ReadAll(c)
			if string(got) != reply {
				t.Fatalf("connection got %q, want %q", got, reply)
			}
		})
	}
}

func readLine(t *testing.T, r io.Reader, prefixes ...string) string {
	t.Helper()
	got := make(chan string, 1)
	go func() {
		defer close(got)
		s := bufio.NewScanner(r)
		for s.Scan() {
			for _, p := range prefixes {
				if strings.HasPrefix(s.Text(), p) {
					got <- s.Text()
					_, _ = io.Copy(io.Discard, r)
					return
				}
			}
		}
		if err := s.Err(); err != nil {
			t.Errorf("reading the child's output: %v", err)
		}
	}()
	select {
	case l := <-got:
		return l
	case <-time.After(10 * time.Second):
		t.Fatal("child printed no result")
		return ""
	}
}

func TestNotActivated(t *testing.T) {
	cases := []struct {
		name string
		env  map[string]string
	}{
		{"no variables", nil},
		{"LISTEN_PID of another process", map[string]string{envPID: strconv.Itoa(os.Getpid() + 1), envFDs: "1"}},
		{"LISTEN_PID not a number", map[string]string{envPID: "self", envFDs: "1"}},
		{"no fds", map[string]string{envPID: strconv.Itoa(os.Getpid()), envFDs: "0"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, k := range []string{envPID, envFDs, envFDNames} {
				t.Setenv(k, tc.env[k])
				if tc.env[k] == "" {
					_ = os.Unsetenv(k)
				}
			}
			ln, err := Activated("edge")
			if ln != nil {
				_ = ln.Close()
			}
			if !errors.Is(err, ErrNotActivated) {
				t.Fatalf("err = %v, want ErrNotActivated", err)
			}
			for _, k := range []string{envPID, envFDs, envFDNames} {
				if v, ok := os.LookupEnv(k); ok {
					t.Errorf("%s=%q left in the env", k, v)
				}
			}
		})
	}
}

func TestSystemdWithoutTheNamedSocket(t *testing.T) {
	t.Setenv(envPID, strconv.Itoa(os.Getpid()))
	t.Setenv(envFDs, "1")
	t.Setenv(envFDNames, "other")
	ln, err := Activated("edge")
	if ln != nil {
		_ = ln.Close()
	}
	if err == nil || errors.Is(err, ErrNotActivated) || !strings.Contains(err.Error(), `no socket named "edge"`) {
		t.Fatalf("err = %v, want a misconfiguration error", err)
	}
}
