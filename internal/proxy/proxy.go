package proxy

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"time"

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

// LoopbackHost is the only host the listener binds; DefaultAddr picks an
// ephemeral port on it.
const (
	LoopbackHost = "127.0.0.1"
	DefaultAddr  = LoopbackHost + ":0"
)

// DefaultDecideTimeout bounds Decide when Options.DecideTimeout is zero: the
// default classifier deadline (1500 ms) plus 200 ms.
const DefaultDecideTimeout = 1700 * time.Millisecond

const (
	readHeaderTimeout = 10 * time.Second
	shutdownGrace     = 5 * time.Second
)

// Decider returns the rewrite for a user step, or false to forward the
// original. It must respect ctx's deadline; the proxy forwards the original
// once the deadline passes, whatever Decide returns later.
type Decider interface {
	Decide(ctx context.Context, req *responses.Request, meta responses.Meta) (responses.Rewrite, bool)
}

// Observer learns the served model, usage and status of each response: once
// per response.completed event of a 2xx stream, or once with zero usage for a
// non-2xx upstream response. Requests that never got an upstream response are
// not observed.
type Observer interface {
	Observe(meta responses.Meta, served string, usage responses.Usage, status int)
}

// Options configure a Server. Upstream overrides the backend in tests only.
// Addr must be on LoopbackHost; empty means DefaultAddr. Client's
// CheckRedirect is always replaced with one that refuses redirects.
// OnCatalog receives every /models 200 body that parses (launcher passes
// catalog.Store.Set). DecideTimeout bounds each Decide call.
type Options struct {
	Addr          string
	Upstream      string
	Client        *http.Client
	Decider       Decider
	Observer      Observer
	OnCatalog     func(catalog.Catalog)
	Logger        *slog.Logger
	DecideTimeout time.Duration
}

// Server is the loopback proxy. Call Listen, then Serve.
type Server struct {
	opts     Options
	log      *slog.Logger
	client   *http.Client
	timeout  time.Duration
	upstream *url.URL
	ln       net.Listener
}

// New returns a Server; nothing is bound until Listen.
func New(o Options) *Server {
	log := o.Logger
	if log == nil {
		log = slog.Default()
	}
	timeout := o.DecideTimeout
	if timeout <= 0 {
		timeout = DefaultDecideTimeout
	}
	return &Server{opts: o, log: log, client: upstreamClient(o.Client), timeout: timeout}
}

var errRedirect = errors.New("proxy: upstream redirect refused")

func refuseRedirect(*http.Request, []*http.Request) error { return errRedirect }

func upstreamClient(c *http.Client) *http.Client {
	var out http.Client
	if c != nil {
		out = *c
	} else {
		t := http.DefaultTransport.(*http.Transport).Clone()
		// Plain bytes from upstream: no transparent gzip buffering in the
		// stream, and the SSE tee and catalog parse read the body as is.
		t.DisableCompression = true
		out.Transport = t
	}
	out.CheckRedirect = refuseRedirect
	return &out
}

// Listen validates the options and binds the loopback listener. It returns
// the bound port.
func (s *Server) Listen() (int, error) {
	if s.ln != nil {
		return 0, errors.New("proxy: already listening")
	}
	up, err := parseUpstream(s.opts.Upstream)
	if err != nil {
		return 0, err
	}
	addr := s.opts.Addr
	if addr == "" {
		addr = DefaultAddr
	}
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return 0, fmt.Errorf("proxy: addr %q: %w", addr, err)
	}
	if host != LoopbackHost {
		return 0, fmt.Errorf("proxy: addr %q: only %s may be bound", addr, LoopbackHost)
	}
	ln, err := net.Listen("tcp4", addr)
	if err != nil {
		return 0, fmt.Errorf("proxy: listen: %w", err)
	}
	s.upstream, s.ln = up, ln
	return ln.Addr().(*net.TCPAddr).Port, nil
}

func parseUpstream(raw string) (*url.URL, error) {
	if raw == "" {
		raw = Upstream
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("proxy: upstream: %w", err)
	}
	if (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.RawQuery != "" {
		return nil, fmt.Errorf("proxy: upstream %q: want scheme://host/path", raw)
	}
	return u, nil
}

// Serve serves until ctx is done, then shuts down: in-flight requests see
// ctx cancelled, which cancels their upstream calls.
func (s *Server) Serve(ctx context.Context) error {
	if s.ln == nil {
		return errors.New("proxy: Serve before Listen")
	}
	srv := &http.Server{
		Handler:           s.routes(),
		ReadHeaderTimeout: readHeaderTimeout,
		ErrorLog:          slog.NewLogLogger(s.log.Handler(), slog.LevelWarn),
		BaseContext:       func(net.Listener) context.Context { return ctx },
	}
	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(s.ln) }()
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	sctx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
	defer cancel()
	if err := srv.Shutdown(sctx); err != nil {
		_ = srv.Close()
	}
	if err := <-errc; !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc(http.MethodPost+" "+PathResponses, s.handleResponses)
	mux.HandleFunc(http.MethodGet+" "+PathModels, s.handleModels)
	mux.HandleFunc(http.MethodGet+" "+PathHealth, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok\n"))
	})
	return mux
}
