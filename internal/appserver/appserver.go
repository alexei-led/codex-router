package appserver

import (
	"context"

	"github.com/alexei-led/codex-router/internal/config"
)

// Settings are the thread settings the router writes and reads.
type Settings struct {
	Model  string        `json:"model"`
	Effort config.Effort `json:"effort"`
}

// SettingsUpdated is a thread/settings/updated notification.
type SettingsUpdated struct {
	ThreadID string
	Settings Settings
}

// Conn is one message-oriented connection to the app-server.
type Conn interface {
	Read(ctx context.Context) ([]byte, error)
	Write(ctx context.Context, msg []byte) error
	Close() error
}

// Dialer opens a Conn to the app-server socket.
type Dialer interface {
	Dial(ctx context.Context, socketPath string) (Conn, error)
}

// API is what the launcher uses; Client implements it.
type API interface {
	Initialize(ctx context.Context) error
	UpdateSettings(ctx context.Context, threadID string, s Settings) error
	Updates() <-chan SettingsUpdated
	Close() error
}

// JSON-RPC method names used.
const (
	MethodInitialize      = "initialize"
	MethodSettingsUpdate  = "thread/settings/update"
	MethodSettingsUpdated = "thread/settings/updated"
	MethodTurnStarted     = "turn/started"
	MethodTurnCompleted   = "turn/completed"
)
