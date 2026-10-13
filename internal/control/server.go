package control

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"sync"
	"time"

	"github.com/alexei-led/codex-router/internal/route"
)

var knownOps = map[route.Op]bool{
	route.OpPin: true, route.OpUnpin: true, route.OpPause: true,
	route.OpResume: true, route.OpPreset: true, route.OpSnapshot: true,
}

// Server is the control socket. It pushes snapshots to every client and
// applies their commands through the Handler.
type Server struct {
	path string
	h    Handler
	ln   net.Listener

	mu      sync.Mutex
	latest  *route.Snapshot
	clients map[*serverConn]struct{}
	closed  bool
	wg      sync.WaitGroup
}

type serverConn struct {
	conn   net.Conn
	wmu    sync.Mutex
	notify chan struct{}
	done   chan struct{}
	once   sync.Once
}

// Listen binds the socket at path with mode 0600, removing a stale socket
// file and refusing a live one. The parent dir should be 0700: the mode is
// set right after bind, not at bind.
func Listen(path string, h Handler) (*Server, error) {
	if c, err := net.DialTimeout("unix", path, time.Second); err == nil {
		_ = c.Close()
		return nil, fmt.Errorf("control socket %s is already in use", path)
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("remove stale control socket: %w", err)
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, fmt.Errorf("listen control socket: %w", err)
	}
	if err := os.Chmod(path, SocketMode); err != nil {
		_ = ln.Close()
		return nil, fmt.Errorf("chmod control socket: %w", err)
	}
	s := &Server{path: path, h: h, ln: ln, clients: map[*serverConn]struct{}{}}
	s.wg.Add(1)
	go s.accept()
	return s, nil
}

// Publish records the latest snapshot and wakes every client. Bursts
// coalesce: a client gets the newest snapshot at most once per PublishInterval.
func (s *Server) Publish(snap route.Snapshot) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.latest = &snap
	for c := range s.clients {
		select {
		case c.notify <- struct{}{}:
		default:
		}
	}
}

// Close stops accepting, drops all clients and removes the socket file.
func (s *Server) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	clients := make([]*serverConn, 0, len(s.clients))
	for c := range s.clients {
		clients = append(clients, c)
	}
	s.mu.Unlock()
	err := s.ln.Close()
	for _, c := range clients {
		c.close()
	}
	s.wg.Wait()
	_ = os.Remove(s.path)
	return err
}

func (s *Server) accept() {
	defer s.wg.Done()
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			return
		}
		c := &serverConn{conn: conn, notify: make(chan struct{}, 1), done: make(chan struct{})}
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			_ = conn.Close()
			return
		}
		s.clients[c] = struct{}{}
		s.wg.Add(1)
		s.mu.Unlock()
		go s.serve(c)
	}
}

func (s *Server) serve(c *serverConn) {
	defer s.wg.Done()
	defer func() {
		c.close()
		s.mu.Lock()
		delete(s.clients, c)
		s.mu.Unlock()
	}()
	snap := s.h.Snapshot()
	if c.write(Message{Type: MessageSnapshot, Snapshot: &snap}) != nil {
		return
	}
	go s.push(c)

	sc := bufio.NewScanner(c.conn)
	sc.Buffer(make([]byte, 0, 64<<10), maxLine)
	for sc.Scan() {
		if c.write(s.handle(sc.Bytes())...) != nil {
			return
		}
	}
}

func (s *Server) push(c *serverConn) {
	for {
		select {
		case <-c.notify:
		case <-c.done:
			return
		}
		s.mu.Lock()
		snap := *s.latest
		s.mu.Unlock()
		if c.write(Message{Type: MessageSnapshot, Snapshot: &snap}) != nil {
			c.close()
			return
		}
		select {
		case <-time.After(PublishInterval):
		case <-c.done:
			return
		}
	}
}

func (s *Server) handle(line []byte) []Message {
	var cmd route.Command
	if err := json.Unmarshal(line, &cmd); err != nil {
		return []Message{{Type: MessageError, Error: "malformed command"}}
	}
	if !knownOps[cmd.Op] {
		return []Message{{Type: MessageError, Error: fmt.Sprintf("unknown op %q", cmd.Op)}}
	}
	if cmd.Op == route.OpSnapshot {
		snap := s.h.Snapshot()
		return []Message{{Type: MessageSnapshot, Snapshot: &snap}, {Type: MessageAck}}
	}
	if err := s.h.Apply(cmd); err != nil {
		return []Message{{Type: MessageError, Error: err.Error()}}
	}
	return []Message{{Type: MessageAck}}
}

func (c *serverConn) write(msgs ...Message) error {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	for _, m := range msgs {
		b, err := json.Marshal(m)
		if err != nil {
			return err
		}
		_ = c.conn.SetWriteDeadline(time.Now().Add(writeTimeout))
		if _, err := c.conn.Write(append(b, '\n')); err != nil {
			return err
		}
	}
	return nil
}

func (c *serverConn) close() {
	c.once.Do(func() {
		close(c.done)
		_ = c.conn.Close()
	})
}
