package control

import (
	"os"
	"time"

	"github.com/alexei-led/codex-router/internal/route"
)

// SocketName is the control socket file inside the runtime dir.
const SocketName = "control.sock"

// SocketMode is the permission the socket is created with.
const SocketMode os.FileMode = 0o600

// EnvSocket names the environment variable the launcher sets for the hook
// and the TUI.
const EnvSocket = "CODEX_ROUTER_CONTROL"

// MessageType tags a server-to-client line.
type MessageType string

const (
	MessageSnapshot MessageType = "snapshot"
	MessageAck      MessageType = "ack"
	MessageError    MessageType = "error"
)

// PublishInterval is the minimum gap between pushed snapshots per client
// (20 per second).
const PublishInterval = 50 * time.Millisecond

const (
	writeTimeout = 2 * time.Second
	sendTimeout  = 2 * time.Second
	maxLine      = 4 << 20
)

// Message is one server-to-client line.
type Message struct {
	Type     MessageType     `json:"type"`
	Snapshot *route.Snapshot `json:"snapshot,omitempty"`
	Error    string          `json:"error,omitempty"`
}

// Handler is what the server drives; the route engine implements it.
type Handler interface {
	Apply(c route.Command) error
	Snapshot() route.Snapshot
}
