package proxy

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/alexei-led/codex-router/internal/catalog"
	"github.com/alexei-led/codex-router/internal/responses"
)

// Upstream is the only backend the proxy talks to.
const Upstream = "https://chatgpt.com/backend-api/codex"

// Paths the listener serves; anything else is 404.
const (
	PathResponses = "/v1/responses"
	PathModels    = "/v1/models"
	PathHealth    = "/healthz"
)

// MaxBodyBytes caps a request body; larger bodies get 413.
const MaxBodyBytes = 32 << 20

// Decider returns the rewrite for a user step, or false to forward the
// original. It must respect ctx's deadline.
type Decider interface {
	Decide(ctx context.Context, req *responses.Request, meta responses.Meta) (responses.Rewrite, bool)
}

// Observer learns the served model, usage and status of each response.
type Observer interface {
	Observe(meta responses.Meta, served string, usage responses.Usage, status int)
}

// Options configure a Server. Upstream overrides the backend in tests only.
type Options struct {
	Addr      string
	Upstream  string
	Client    *http.Client
	Decider   Decider
	Observer  Observer
	OnCatalog func(catalog.Catalog)
	Logger    *slog.Logger
}
