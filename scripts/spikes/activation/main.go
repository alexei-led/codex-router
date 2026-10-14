// Command edge is the stage 2 socket activation spike (plan Task 13, gate G3
// and G4). It takes its listening socket from launchd or systemd, so the
// port stays bound while it is down, checks the provider token, and relays
// to an upstream codex-router proxy. /spike answers with its pid after an
// optional ?delay=MS, to time a request across a kill -9.
//
// It lives in its own module so purego stays out of the main go.mod until
// plan Task 16.
package main

import (
	"crypto/subtle"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// headerToken matches internal/proxy.HeaderToken.
const headerToken = "X-Codex-Router-Token"

func main() {
	name := flag.String("name", "Listeners", "socket `name` in the plist Sockets dict")
	upstream := flag.String("upstream", "", "codex-router proxy `URL` to relay to (empty: /spike only)")
	tokenFile := flag.String("token-file", "", "`file` with the token every relayed request must carry")
	flag.Parse()

	if err := run(*name, *upstream, *tokenFile); err != nil {
		slog.Error("edge-failed", "err", err)
		os.Exit(1)
	}
}

func run(name, upstream, tokenFile string) error {
	token, err := readToken(tokenFile)
	if err != nil {
		return err
	}
	ln, err := activated(name)
	if err != nil {
		return err
	}
	slog.Info("edge-start", "pid", os.Getpid(), "addr", ln.Addr().String(), "upstream", upstream, "token", token != "")

	mux := http.NewServeMux()
	mux.HandleFunc("/spike", spike)
	if upstream != "" {
		u, err := url.Parse(upstream)
		if err != nil {
			return fmt.Errorf("upstream: %w", err)
		}
		relay := &httputil.ReverseProxy{
			Rewrite:       func(r *httputil.ProxyRequest) { r.SetURL(u) },
			FlushInterval: -1, // stream SSE replies as they arrive
		}
		mux.Handle("/", checkToken(token, relay))
	}
	return http.Serve(ln, mux)
}

func readToken(path string) (string, error) {
	if path == "" {
		return "", nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("token: %w", err)
	}
	return strings.TrimSpace(string(b)), nil
}

// checkToken logs whether each request carried the token (never its value)
// and rejects it when the token is set and does not match.
func checkToken(token string, next http.Handler) http.Handler {
	want := []byte(token)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := r.Header.Get(headerToken)
		verdict := "ok"
		switch {
		case token == "":
			verdict = "unchecked"
		case got == "":
			verdict = "missing"
		case subtle.ConstantTimeCompare([]byte(got), want) != 1:
			verdict = "wrong"
		}
		slog.Info("edge-request", "path", r.URL.Path, "token", verdict)
		if verdict == "missing" || verdict == "wrong" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func spike(w http.ResponseWriter, r *http.Request) {
	if ms, err := strconv.Atoi(r.URL.Query().Get("delay")); err == nil && ms > 0 {
		time.Sleep(time.Duration(ms) * time.Millisecond)
	}
	_, _ = fmt.Fprintf(w, "pid %d\n", os.Getpid())
}

// fileListener turns an inherited fd into a listener; net.FileListener dups
// it, so the original is closed here.
func fileListener(fd int, name string) (net.Listener, error) {
	f := os.NewFile(uintptr(fd), name)
	defer func() { _ = f.Close() }()
	ln, err := net.FileListener(f)
	if err != nil {
		return nil, fmt.Errorf("listener from fd %d: %w", fd, err)
	}
	return ln, nil
}
