package launcher

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os/exec"
	"syscall"
	"time"

	"github.com/alexei-led/codex-router/internal/appserver"
	"github.com/alexei-led/codex-router/internal/proxy"
)

const (
	appServerTimeout = 10 * time.Second
	dialRetry        = 50 * time.Millisecond
	stopGrace        = 3 * time.Second
)

var errAppServerExited = errors.New("app-server exited during startup")

// appServer is the private `codex app-server` child.
type appServer struct {
	cmd  *exec.Cmd
	done chan struct{} // closed when it has exited
}

// startAppServer starts the app-server on sock with the router provider.
// It keeps the launcher's cwd, which is the user's: the remote TUI's thread
// uses the app-server's cwd (research §9, 1a). --no-daemon keeps it off the
// shared daemon. It gets its own process group so the terminal's Ctrl-C,
// meant for the TUI, does not kill it; its stdio is discarded because the
// TUI owns the terminal.
func startAppServer(bin, sock string, port int, env []string) (*appServer, error) {
	args := []string{"--no-daemon", "app-server", "--listen", "unix://" + sock}
	for _, c := range ProviderOverrides(port) {
		args = append(args, "-c", c)
	}
	cmd := exec.Command(bin, args...)
	cmd.Env = env
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start app-server: %w", err)
	}
	a := &appServer{cmd: cmd, done: make(chan struct{})}
	go func() {
		_ = cmd.Wait()
		close(a.done)
	}()
	return a, nil
}

// stop sends SIGTERM to the app-server's group, then SIGKILL after
// stopGrace, and waits for it to exit.
func (a *appServer) stop() {
	pgid := -a.cmd.Process.Pid
	select {
	case <-a.done:
		return
	default:
	}
	_ = syscall.Kill(pgid, syscall.SIGTERM)
	select {
	case <-a.done:
		return
	case <-time.After(stopGrace):
	}
	_ = syscall.Kill(pgid, syscall.SIGKILL)
	<-a.done
}

// dialAppServer connects to sock once the app-server listens and runs the
// initialize handshake. The client logs to log.
func dialAppServer(ctx context.Context, sock string, exited <-chan struct{}, log *slog.Logger) (appserver.API, error) {
	ctx, cancel := context.WithTimeout(ctx, appServerTimeout)
	defer cancel()
	for {
		api, err := appserver.Dial(ctx, sock, appserver.WebSocket(), log)
		if err == nil {
			if err := api.Initialize(ctx); err != nil {
				_ = api.Close()
				return nil, fmt.Errorf("app-server initialize: %w", err)
			}
			provider, err := api.Provider(ctx)
			if err != nil {
				_ = api.Close()
				return nil, fmt.Errorf("verify app-server provider: %w", err)
			}
			if provider != proxy.ProviderID {
				_ = api.Close()
				return nil, errors.New("codex did not select the router provider; check native provider settings and managed configuration")
			}
			return api, nil
		}
		select {
		case <-exited:
			return nil, errAppServerExited
		case <-ctx.Done():
			return nil, fmt.Errorf("app-server not ready after %s: %w", appServerTimeout, err)
		case <-time.After(dialRetry):
		}
	}
}
