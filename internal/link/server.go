package link

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/alexei-led/codex-router/internal/proxy"
	"github.com/alexei-led/codex-router/internal/registry"
	"github.com/alexei-led/codex-router/internal/responses"
)

const (
	helloTimeout = 2 * time.Second
	writeTimeout = 2 * time.Second
)

// Handler decides and observes for the brain; *route.Engine is one.
type Handler interface {
	proxy.Decider
	proxy.Observer
}

// Server is the brain's end of the link. It accepts edges of its own home
// only, runs each decide in its own goroutine (a slow classifier call holds
// up only its own request) and applies observes in arrival order.
type Server struct {
	path   string
	homeID string
	h      Handler
	log    *slog.Logger
	ln     net.Listener
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	mu     sync.Mutex
	conns  map[net.Conn]struct{}
	closed bool
}

// Listen binds the socket at path with mode 0600 and starts serving. The
// parent dir must be private (registry.CheckPrivate): the mode is set right
// after bind, and the dir closes that window. A stale socket file is
// replaced; a live one or a non-socket file is refused.
func Listen(path, homeID string, h Handler, log *slog.Logger) (*Server, error) {
	if log == nil {
		log = slog.Default()
	}
	if err := registry.CheckPrivate(filepath.Dir(path)); err != nil {
		return nil, fmt.Errorf("link socket dir: %w", err)
	}
	if c, err := net.DialTimeout("unix", path, time.Second); err == nil {
		_ = c.Close()
		return nil, fmt.Errorf("link socket %s is already in use", path)
	}
	if err := removeStale(path); err != nil {
		return nil, err
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, fmt.Errorf("listen link socket: %w", err)
	}
	if err := os.Chmod(path, SocketMode); err != nil {
		_ = ln.Close()
		return nil, fmt.Errorf("chmod link socket: %w", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := &Server{
		path: path, homeID: homeID, h: h, log: log, ln: ln,
		ctx: ctx, cancel: cancel, conns: map[net.Conn]struct{}{},
	}
	s.wg.Go(s.accept)
	return s, nil
}

func removeStale(path string) error {
	fi, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("stat link socket: %w", err)
	}
	if fi.Mode().Type() != os.ModeSocket {
		return fmt.Errorf("%s exists and is not a socket", path)
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("remove stale link socket: %w", err)
	}
	return nil
}

// Close stops accepting, cancels in-flight decides, drops every edge and
// removes the socket file.
func (s *Server) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	conns := make([]net.Conn, 0, len(s.conns))
	for c := range s.conns {
		conns = append(conns, c)
	}
	s.mu.Unlock()
	err := s.ln.Close()
	s.cancel()
	for _, c := range conns {
		_ = c.Close()
	}
	s.wg.Wait()
	_ = os.Remove(s.path)
	return err
}

func (s *Server) accept() {
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			return
		}
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			_ = conn.Close()
			return
		}
		s.conns[conn] = struct{}{}
		s.wg.Add(1)
		s.mu.Unlock()
		go s.serve(conn)
	}
}

func (s *Server) serve(conn net.Conn) {
	defer s.wg.Done()
	ctx, cancel := context.WithCancel(s.ctx)
	var decides sync.WaitGroup
	defer func() {
		cancel()
		_ = conn.Close()
		decides.Wait()
		s.mu.Lock()
		delete(s.conns, conn)
		s.mu.Unlock()
	}()
	w := &serverWriter{conn: conn}
	br := bufio.NewReader(conn)
	if !s.greet(conn, br, w) {
		return
	}
	for {
		h, body, err := ReadFrame(br)
		if err != nil {
			return
		}
		switch h.Type {
		case TypeDecide:
			decides.Go(func() { s.decide(ctx, w, h, body) })
		case TypeObserve:
			s.observe(h)
		}
	}
}

// greet reads the edge's hello and answers it, or refuses an edge of
// another home.
func (s *Server) greet(conn net.Conn, br *bufio.Reader, w *serverWriter) bool {
	_ = conn.SetReadDeadline(time.Now().Add(helloTimeout))
	h, _, err := ReadFrame(br)
	switch {
	case err != nil || h.Type != TypeHello:
		_ = w.write(Header{Type: TypeError, Error: "expected hello"})
		return false
	case h.HomeID != s.homeID:
		s.log.Warn("link-refused", "home", h.HomeID, "want", s.homeID)
		_ = w.write(Header{Type: TypeError, Error: fmt.Sprintf("home %q is not served here", h.HomeID)})
		return false
	}
	_ = conn.SetReadDeadline(time.Time{})
	return w.write(Header{Type: TypeHello, HomeID: s.homeID}) == nil
}

// decide answers one decide with rewrite or none. The edge's remaining wait
// bounds the Handler, as the proxy's deadline does in process. An unparseable
// body or a panic is none.
func (s *Server) decide(ctx context.Context, w *serverWriter, h Header, body []byte) {
	reply := Header{Type: TypeNone, ID: h.ID}
	defer func() {
		if p := recover(); p != nil {
			s.log.Error("link-decide-panic", "type", fmt.Sprintf("%T", p))
		}
		_ = w.write(reply)
	}()
	if h.Meta == nil {
		return
	}
	req, err := responses.Parse(body)
	if err != nil {
		s.log.Warn("parse-error", "thread", h.Meta.ThreadID, "turn", h.Meta.TurnID, "bytes", len(body))
		return
	}
	if h.TimeoutMs > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(h.TimeoutMs)*time.Millisecond)
		defer cancel()
	}
	if rw, ok := s.h.Decide(ctx, req, *h.Meta); ok {
		reply = Header{Type: TypeRewrite, ID: h.ID, Rewrite: &rw}
	}
}

func (s *Server) observe(h Header) {
	if h.Meta == nil || h.Usage == nil {
		return
	}
	defer func() {
		if p := recover(); p != nil {
			s.log.Error("link-observe-panic", "type", fmt.Sprintf("%T", p))
		}
	}()
	s.h.Observe(*h.Meta, h.Served, *h.Usage, h.Status)
}

// serverWriter serializes the replies of one edge's concurrent decides.
type serverWriter struct {
	mu   sync.Mutex
	conn net.Conn
}

func (w *serverWriter) write(h Header) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	_ = w.conn.SetWriteDeadline(time.Now().Add(writeTimeout))
	return WriteFrame(w.conn, h, nil)
}
