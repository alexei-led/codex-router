package control

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/alexei-led/codex-router/internal/route"
)

var commandOps = map[route.Op]bool{
	route.OpPin: true, route.OpUnpin: true, route.OpPause: true,
	route.OpResume: true, route.OpMode: true,
}

// Server is the control socket. After a client's hello it pushes the
// sessions list on change and the snapshots of the one session the client
// subscribed to, and applies commands through the Handler. One publisher
// goroutine computes pushes; each client has its own writer, so a slow
// client delays only itself.
type Server struct {
	path  string
	h     Handler
	hello Hello
	ln    net.Listener
	wake  chan struct{}
	done  chan struct{}
	wg    sync.WaitGroup

	mu      sync.Mutex
	clients map[*serverConn]struct{}
	pending map[string]route.Snapshot // published snapshots of subscribed threads
	all     bool                      // every subscription needs a fresh snapshot
	closed  bool

	// Owned by the publisher goroutine.
	listSeq  uint64
	listLast []byte
}

// serverConn is one client. Fields below once are guarded by Server.mu.
type serverConn struct {
	conn   net.Conn
	wmu    sync.Mutex
	notify chan struct{}
	done   chan struct{}
	once   sync.Once

	ready   bool   // hello done
	session string // subscribed session id, "" for none
	thread  string // its thread id
	listSeq uint64 // newest sessions list queued
	outList *Message
	outSnap *Message
}

// Listen binds the socket at path with mode 0600, removing a stale socket
// file and refusing a live one or a non-socket file. The parent dir must not
// be accessible to group or others: the mode is set right after bind, not at
// bind, so the dir closes that window.
func Listen(path string, h Handler) (*Server, error) {
	if c, err := net.DialTimeout("unix", path, time.Second); err == nil {
		_ = c.Close()
		return nil, fmt.Errorf("control socket %s is already in use", path)
	}
	if err := checkParent(path); err != nil {
		return nil, err
	}
	if err := removeStale(path); err != nil {
		return nil, err
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, fmt.Errorf("listen control socket: %w", err)
	}
	if err := os.Chmod(path, SocketMode); err != nil {
		_ = ln.Close()
		return nil, fmt.Errorf("chmod control socket: %w", err)
	}
	s := &Server{
		path: path, h: h, hello: h.Hello(), ln: ln,
		wake: make(chan struct{}, 1), done: make(chan struct{}),
		clients: map[*serverConn]struct{}{}, pending: map[string]route.Snapshot{},
	}
	s.wg.Go(s.accept)
	s.wg.Go(s.publish)
	return s, nil
}

func checkParent(path string) error {
	dir := filepath.Dir(path)
	fi, err := os.Stat(dir)
	if err != nil {
		return fmt.Errorf("control socket dir: %w", err)
	}
	if fi.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("control socket dir %s must not be accessible to group or others", dir)
	}
	return nil
}

func removeStale(path string) error {
	fi, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("stat control socket: %w", err)
	}
	if fi.Mode().Type() != os.ModeSocket {
		return fmt.Errorf("%s exists and is not a socket", path)
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("remove stale control socket: %w", err)
	}
	return nil
}

// Publish records a changed thread's snapshot for its subscribers and
// schedules a sessions list check. Bursts coalesce: a client gets the newest
// state at most once per PublishInterval. A snapshot older than the one held
// lost a publish race and is dropped.
func (s *Server) Publish(snap route.Snapshot) {
	s.mu.Lock()
	if snap.ThreadID != "" && s.subscribed(snap.ThreadID) {
		if old, ok := s.pending[snap.ThreadID]; !ok || snap.Seq >= old.Seq {
			s.pending[snap.ThreadID] = snap
		}
	}
	s.mu.Unlock()
	s.poke()
}

func (s *Server) subscribed(thread string) bool {
	for c := range s.clients {
		if c.thread == thread {
			return true
		}
	}
	return false
}

func (s *Server) poke() {
	select {
	case s.wake <- struct{}{}:
	default:
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
	close(s.done)
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
	sc := bufio.NewScanner(c.conn)
	sc.Buffer(make([]byte, 0, 64<<10), maxLine)
	_ = c.conn.SetReadDeadline(time.Now().Add(helloTimeout))
	if !sc.Scan() {
		return
	}
	if err := greet(sc.Bytes()); err != nil {
		_ = c.write(Message{Type: MessageError, Error: err.Error()})
		return
	}
	_ = c.conn.SetReadDeadline(time.Time{})
	hello := s.hello
	if c.write(Message{Type: MessageHello, Hello: &hello}) != nil {
		return
	}
	go s.push(c)
	s.mu.Lock()
	c.ready = true
	s.mu.Unlock()
	s.poke() // the first sessions list

	for sc.Scan() {
		if c.write(s.handle(c, sc.Bytes())...) != nil {
			return
		}
	}
}

// greet checks a connection's first line: a hello naming this Protocol.
func greet(line []byte) error {
	var r Request
	if json.Unmarshal(line, &r) != nil || r.Op != OpHello {
		return errors.New("expected hello")
	}
	if r.Protocol != Protocol {
		return fmt.Errorf("unsupported protocol %d, want %d", r.Protocol, Protocol)
	}
	return nil
}

// publish runs flush on every wake, at most once per PublishInterval.
func (s *Server) publish() {
	for {
		select {
		case <-s.wake:
		case <-s.done:
			return
		}
		s.flush()
		select {
		case <-time.After(PublishInterval):
		case <-s.done:
			return
		}
	}
}

// flush queues on every ready client the sessions list when it changed
// since the client's last one, and its session's snapshot when that thread
// changed. A queued message replaces an unsent one of the same kind.
func (s *Server) flush() {
	s.mu.Lock()
	snaps, all := s.pending, s.all
	s.pending, s.all = map[string]route.Snapshot{}, false
	var threads []string
	ready := false
	for c := range s.clients {
		ready = ready || c.ready
		if all && c.ready && c.thread != "" {
			threads = append(threads, c.thread)
		}
	}
	s.mu.Unlock()
	if !ready {
		return
	}
	fresh := map[string]bool{}
	for _, th := range threads {
		if !fresh[th] {
			fresh[th] = true
			snaps[th] = s.h.SnapshotFor(th)
		}
	}
	list := s.list()

	s.mu.Lock()
	defer s.mu.Unlock()
	for c := range s.clients {
		if !c.ready {
			continue
		}
		queued := false
		if c.listSeq < list.Seq {
			m := list
			c.outList, c.listSeq, queued = &m, list.Seq, true
		}
		if snap, ok := snaps[c.thread]; ok && c.thread != "" {
			c.outSnap, queued = &Message{Type: MessageSnapshot, Session: c.session, Snapshot: &snap}, true
		}
		if queued {
			select {
			case c.notify <- struct{}{}:
			default:
			}
		}
	}
}

// list returns the sessions list; Seq moves only when the list changed.
func (s *Server) list() Message {
	items := s.h.Sessions()
	b, err := json.Marshal(items)
	if err != nil || !bytes.Equal(b, s.listLast) {
		s.listSeq++
		s.listLast = b
	}
	return Message{Type: MessageSessions, Seq: s.listSeq, Items: items}
}

// push writes c's queued messages. A failed write drops the client.
func (s *Server) push(c *serverConn) {
	for {
		select {
		case <-c.notify:
		case <-c.done:
			return
		}
		s.mu.Lock()
		var msgs []Message
		if c.outList != nil {
			msgs = append(msgs, *c.outList)
		}
		if c.outSnap != nil {
			msgs = append(msgs, *c.outSnap)
		}
		c.outList, c.outSnap = nil, nil
		s.mu.Unlock()
		if c.write(msgs...) != nil {
			c.close()
			return
		}
	}
}

func (s *Server) handle(c *serverConn, line []byte) []Message {
	var r Request
	if err := json.Unmarshal(line, &r); err != nil {
		return []Message{{Type: MessageError, Error: "malformed command"}}
	}
	switch {
	case r.Op == OpSubscribe:
		return s.subscribe(c, r)
	case r.Op == route.OpSnapshot:
		s.mu.Lock()
		session, thread := c.session, c.thread
		s.mu.Unlock()
		if thread == "" {
			return []Message{ack(r.ID)}
		}
		return []Message{s.snapshot(session, thread), ack(r.ID)}
	case !commandOps[r.Op]:
		return []Message{fail(r.ID, fmt.Sprintf("unknown op %q", r.Op))}
	}
	cmd := r.Command
	if cmd.Session != "" {
		thread, ok := splitSession(s.hello.Home.ID, cmd.Session)
		if !ok {
			return []Message{fail(r.ID, fmt.Sprintf("unknown session %q", cmd.Session))}
		}
		cmd.Session = thread
	}
	if err := s.h.Apply(cmd); err != nil {
		return []Message{fail(r.ID, err.Error())}
	}
	// A home-scoped command changes every session; commands are rare, so
	// refresh every subscription after any.
	s.mu.Lock()
	s.all = true
	s.mu.Unlock()
	s.poke()
	return []Message{ack(r.ID)}
}

// subscribe switches c to one session ("" for none) and answers with that
// session's snapshot before the ack. A snapshot of the old session still
// queued may follow; it carries the old session id.
func (s *Server) subscribe(c *serverConn, r Request) []Message {
	var thread string
	if r.Session != "" {
		var ok bool
		if thread, ok = splitSession(s.hello.Home.ID, r.Session); !ok {
			return []Message{fail(r.ID, fmt.Sprintf("unknown session %q", r.Session))}
		}
	}
	s.mu.Lock()
	c.session, c.thread, c.outSnap = r.Session, thread, nil
	s.mu.Unlock()
	if thread == "" {
		return []Message{ack(r.ID)}
	}
	return []Message{s.snapshot(r.Session, thread), ack(r.ID)}
}

func (s *Server) snapshot(session, thread string) Message {
	snap := s.h.SnapshotFor(thread)
	return Message{Type: MessageSnapshot, Session: session, Snapshot: &snap}
}

func ack(id string) Message { return Message{Type: MessageAck, ID: id} }

func fail(id, err string) Message { return Message{Type: MessageError, ID: id, Error: err} }

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
