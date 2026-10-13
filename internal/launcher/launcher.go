package launcher

import (
	"io"

	"github.com/alexei-led/codex-router/internal/config"
)

// Names inside the runtime dir and the provider the app-server is started with.
const (
	RuntimeDirName = "codex-router"
	AppSocketName  = "app.sock"
	PortFileName   = "proxy.port"
	ProviderID     = "router"
	ProviderName   = "router"
)

// Options configure Run.
type Options struct {
	Config     config.Config
	RuntimeDir string
	CodexBin   string
	CodexArgs  []string
	Env        []string
	Stderr     io.Writer
}
