package control

import (
	"os"
	"strings"
	"time"

	"github.com/alexei-led/codex-router/internal/home"
	"github.com/alexei-led/codex-router/internal/route"
)

// SocketName is the control socket file inside the runtime dir.
const SocketName = "control.sock"

// SocketMode is the permission the socket is created with.
const SocketMode os.FileMode = 0o600

// EnvSocket names the environment variable the launcher sets for the hook
// and the TUI.
const EnvSocket = "CODEX_ROUTER_CONTROL"

// Protocol is the control protocol version a client names in its hello.
const Protocol = 2

// Ops a client sends besides route.Command ops. Hello must be the first
// line; subscribe picks the one session whose snapshots the connection gets.
const (
	OpHello     route.Op = "hello"
	OpSubscribe route.Op = "subscribe"
)

// MessageType tags a server-to-client line.
type MessageType string

const (
	MessageHello    MessageType = "hello"
	MessageSessions MessageType = "sessions"
	MessageSnapshot MessageType = "snapshot"
	MessageAck      MessageType = "ack"
	MessageError    MessageType = "error"
)

// PublishInterval is the minimum gap between pushes per client (20 per
// second).
const PublishInterval = 50 * time.Millisecond

const (
	writeTimeout = 2 * time.Second
	sendTimeout  = 2 * time.Second
	helloTimeout = 2 * time.Second
	maxLine      = 4 << 20
)

// Request is one client-to-server line: a route.Command, a subscribe, or
// the hello. Session is a session id (SessionID), not a bare thread id.
type Request struct {
	route.Command
	Protocol int `json:"protocol,omitempty"`
}

// Hello identifies the endpoint behind a socket.
type Hello struct {
	EndpointID string    `json:"endpoint_id"`
	Kind       string    `json:"kind"`
	Home       home.Home `json:"home"`
}

// Message is one server-to-client line. ID echoes the request's id on ack
// and error. Seq orders sessions lists; a client drops one not newer than
// the last it got.
type Message struct {
	Type     MessageType         `json:"type"`
	ID       string              `json:"id,omitempty"`
	Hello    *Hello              `json:"hello,omitempty"`
	Seq      uint64              `json:"seq,omitempty"`
	Items    []route.SessionInfo `json:"items,omitempty"`
	Session  string              `json:"session,omitempty"`
	Snapshot *route.Snapshot     `json:"snapshot,omitempty"`
	Error    string              `json:"error,omitempty"`
}

// Handler is what the server drives: the route engine plus the endpoint's
// identity. SnapshotFor takes a bare thread id.
type Handler interface {
	Apply(c route.Command) error
	Sessions() []route.SessionInfo
	SnapshotFor(threadID string) route.Snapshot
	Hello() Hello
}

// SessionID is the wire id of a session: "<home id>/<thread id>", the same
// as route.SessionInfo.ID.
func SessionID(homeID, threadID string) string { return homeID + "/" + threadID }

// splitSession returns the thread id of session when it belongs to homeID.
func splitSession(homeID, session string) (string, bool) {
	h, thread, ok := strings.Cut(session, "/")
	return thread, ok && h == homeID && thread != ""
}
