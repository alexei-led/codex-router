package launcher

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/alexei-led/codex-router/internal/catalog"
	"github.com/alexei-led/codex-router/internal/classifier"
	"github.com/alexei-led/codex-router/internal/home"
	"github.com/alexei-led/codex-router/internal/proxy"
	"github.com/alexei-led/codex-router/internal/route"
)

const (
	probeTimeout  = 2 * time.Second
	relistenDelay = 200 * time.Millisecond
)

// newRouter builds the engine for the Codex home with the launcher's cwd for
// every thread. sink may be nil.
func newRouter(o Options, log *slog.Logger, sink route.Sink) (*route.Engine, *catalog.Store, home.Home) {
	o = o.withDefaults()
	store := &catalog.Store{}
	h, err := home.Resolve(o.getenv)
	if err != nil {
		log.Warn("catalog-cache-unreadable", "err", err)
	} else if c, err := catalog.LoadCache(filepath.Join(h.Path, catalog.CacheFile)); err != nil {
		log.Warn("catalog-cache-unreadable", "err", err)
	} else {
		store.Set(c)
		log.Info("catalog-seeded", "models", len(c.Models))
	}
	asker, why := classifier.Configured(o.Config.Classifier, o.Config.Classifiers, o.getenv)
	if asker == nil {
		log.Warn("classifier-disabled", "reason", why)
	}
	d := route.Deps{
		Config: o.Config, Catalog: store, Classifier: asker, ClassifierOffReason: why,
		Logger: log, Home: h, Sink: sink, Cwd: launcherCwd(),
	}
	return route.New(d), store, h
}

// launcherCwd gives every thread the launcher's cwd: the app-server and
// codex inherit it, and one launcher serves one Codex. Nil when unknown.
func launcherCwd() func(string) string {
	wd, err := os.Getwd()
	if err != nil {
		return nil
	}
	return func(string) string { return wd }
}

// launcherLabel names the launcher's working directory like a session label,
// "home · repo · branch". Empty when the directory is unknown.
func launcherLabel(h home.Home) string {
	wd, err := os.Getwd()
	if err != nil {
		return ""
	}
	return home.Label(h.Name, wd)
}

type serveFunc func(ctx context.Context) error

// startProxy binds the proxy, serves it under runCtx with re-listen on the
// same port, and probes it under ctx. An empty token leaves it open.
func startProxy(ctx, runCtx context.Context, wg *sync.WaitGroup, o Options, token string, eng *route.Engine, store *catalog.Store, log *slog.Logger) (int, error) {
	bind := func(port int) (int, serveFunc, error) {
		srv := proxy.New(proxy.Options{
			Addr:          net.JoinHostPort(proxy.LoopbackHost, strconv.Itoa(port)),
			Upstream:      o.Upstream,
			Token:         token,
			Decider:       eng,
			Observer:      eng,
			OnCatalog:     store.Set,
			Logger:        log,
			DecideTimeout: o.Config.DecideTimeout(),
		})
		p, err := srv.Listen()
		return p, srv.Serve, err
	}
	port, serve, err := bind(o.Config.Proxy.Port)
	if err != nil {
		return 0, err
	}
	wg.Go(func() {
		keepServing(runCtx, serve, func() (serveFunc, error) {
			_, s, err := bind(port)
			return s, err
		}, log)
	})
	if err := o.Probe(ctx, port); err != nil {
		return 0, fmt.Errorf("proxy health probe: %w", err)
	}
	log.Info("proxy-ready", "port", port)
	return port, nil
}

// keepServing serves until ctx ends. When Serve stops early it binds the same
// port again: Codex reconnects to it and a running turn recovers (research
// §9, spike 1b), while a new port would leave Codex on a dead listener.
func keepServing(ctx context.Context, serve serveFunc, relisten func() (serveFunc, error), log *slog.Logger) {
	for {
		err := serve(ctx)
		if ctx.Err() != nil {
			return
		}
		log.Error("proxy-serve-stopped", "err", err)
		for {
			select {
			case <-ctx.Done():
				return
			case <-time.After(relistenDelay):
			}
			next, err := relisten()
			if err == nil {
				serve = next
				log.Info("proxy-relistened")
				break
			}
			log.Warn("proxy-relisten-failed", "err", err)
		}
	}
}

func probeHealth(ctx context.Context, port int) error {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	url := "http://" + net.JoinHostPort(proxy.LoopbackHost, strconv.Itoa(port)) + proxy.PathHealth
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("status %d", resp.StatusCode)
	}
	return nil
}

// Serve runs the proxy and engine alone, without app-server or Codex, until
// ctx ends: for debugging and `codex exec` runs pointed at it with
// ProviderOverrides. ready gets the port once the health probe passes. The
// port is Config.Proxy.Port (0 = ephemeral). It has no token, so any local
// uid can use it: a debugging tool, not for shared hosts.
func Serve(ctx context.Context, o Options, ready func(port int)) error {
	o = o.withDefaults()
	log := o.Logger
	if log == nil {
		log = slog.New(slog.NewJSONHandler(o.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
	}
	eng, store, _ := newRouter(o, log, nil)
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	var wg sync.WaitGroup
	defer wg.Wait()
	port, err := startProxy(ctx, runCtx, &wg, o, "", eng, store, log)
	if err != nil {
		cancel()
		return err
	}
	ready(port)
	<-ctx.Done()
	return nil
}
