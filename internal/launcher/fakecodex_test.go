package launcher

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/alexei-led/codex-router/internal/config"
	"github.com/alexei-led/codex-router/internal/control"
	"github.com/alexei-led/codex-router/internal/proxy"
	"github.com/alexei-led/codex-router/internal/responses"
	"github.com/alexei-led/codex-router/internal/route"
)

// The test binary doubles as a fake `codex` when envFake is set: the
// launcher runs it as the app-server and as `codex --remote`.
const (
	envFake          = "CODEX_ROUTER_FAKE_CODEX"
	envRecord        = "CODEX_ROUTER_FAKE_RECORD"
	envExit          = "CODEX_ROUTER_FAKE_EXIT"
	envResumeFail    = "CODEX_ROUTER_FAKE_RESUME_FAIL"
	envAppServerDies = "CODEX_ROUTER_FAKE_APPSERVER_DIES"

	fakeThread     = "thread-1"
	fakeToken      = "Bearer SECRET-TOKEN-123"
	fakeSignalsArg = "signals"
	exitFailed     = 3
	exitOnTerm     = 9
)

// fakeSignals prints "ready", then the name of every signal it gets, and
// exits with exitOnTerm on SIGTERM.
func fakeSignals() int {
	sigs := make(chan os.Signal, 4)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGQUIT, syscall.SIGHUP, syscall.SIGTERM)
	fmt.Println("ready")
	for sig := range sigs {
		fmt.Println(sig)
		if sig == syscall.SIGTERM {
			return exitOnTerm
		}
	}
	return exitFailed
}

var pinnedRoute = config.Route{Model: "gpt-6-luna", Effort: config.EffortMedium}

func TestMain(m *testing.M) {
	if os.Getenv(envFake) != "" {
		os.Exit(fakeCodex(os.Args[1:]))
	}
	os.Exit(m.Run())
}

type record struct {
	Kind string          `json:"kind"`
	Data json.RawMessage `json:"data"`
}

func writeRecord(kind string, data any) {
	raw, _ := json.Marshal(data)
	line, _ := json.Marshal(record{Kind: kind, Data: raw})
	f, err := os.OpenFile(os.Getenv(envRecord), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer func() { _ = f.Close() }()
	_, _ = f.Write(append(line, '\n'))
}

func fakeCodex(args []string) int {
	if len(args) > 0 && args[0] == "--no-daemon" {
		args = args[1:]
	}
	switch {
	case len(args) > 0 && args[0] == "app-server":
		return fakeAppServer(args[1:])
	case len(args) > 1 && args[0] == "--remote":
		return fakeRemote(args[1], args[2:])
	case len(args) > 0 && args[0] == fakeSignalsArg:
		return fakeSignals()
	}
	fmt.Fprintln(os.Stderr, "fake codex: unexpected args", args)
	return exitFailed
}

func fakeAppServer(args []string) int {
	var sock string
	var overrides []string
	for i := 0; i+1 < len(args); i++ {
		switch args[i] {
		case "--listen":
			sock = strings.TrimPrefix(args[i+1], "unix://")
			i++
		case "-c":
			overrides = append(overrides, args[i+1])
			i++
		}
	}
	cwd, _ := os.Getwd()
	writeRecord("start", map[string]any{
		"overrides": overrides, "cwd": cwd, "control": os.Getenv(control.EnvSocket), "token": os.Getenv(EnvToken),
	})
	if os.Getenv(envAppServerDies) != "" {
		return exitFailed
	}
	ln, err := net.Listen("unix", sock)
	if err != nil {
		return exitFailed
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		serveRPC(r.Context(), ws)
	})}
	_ = srv.Serve(ln)
	return 0
}

func serveRPC(ctx context.Context, ws *websocket.Conn) {
	send := func(v any) {
		b, _ := json.Marshal(v)
		_ = ws.Write(ctx, websocket.MessageText, b)
	}
	for {
		_, b, err := ws.Read(ctx)
		if err != nil {
			return
		}
		var m struct {
			ID     *int64          `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if json.Unmarshal(b, &m) != nil || m.ID == nil {
			continue
		}
		switch m.Method {
		case "initialize":
			send(map[string]any{"id": *m.ID, "result": map[string]any{}})
		case "thread/resume":
			writeRecord("resume", m.Params)
			if os.Getenv(envResumeFail) != "" {
				send(map[string]any{"id": *m.ID, "error": map[string]any{"code": -32600, "message": "no rollout found"}})
				continue
			}
			send(map[string]any{"id": *m.ID, "result": map[string]any{}})
		case "thread/settings/update":
			writeRecord("update", m.Params)
			var p struct {
				ThreadID string `json:"threadId"`
				Model    string `json:"model"`
				Effort   string `json:"effort"`
			}
			_ = json.Unmarshal(m.Params, &p)
			send(map[string]any{"id": *m.ID, "result": map[string]any{}})
			send(map[string]any{"method": "thread/settings/updated", "params": map[string]any{
				"threadId":       p.ThreadID,
				"threadSettings": map[string]any{"model": p.Model, "effort": p.Effort, "modelProvider": ProviderID, "cwd": "/w"},
			}})
		}
	}
}

func fakeRemote(addr string, args []string) int {
	writeRecord("remote", map[string]any{"args": args, "token": os.Getenv(EnvToken)})
	if err := driveTurn(filepath.Dir(strings.TrimPrefix(addr, "unix://"))); err != nil {
		fmt.Fprintln(os.Stderr, "fake codex:", err)
		return exitFailed
	}
	code, _ := strconv.Atoi(os.Getenv(envExit))
	return code
}

// recordedToken returns the proxy token the fake app-server was started with:
// in real Codex the app-server makes the proxy calls, here the remote does.
func recordedToken() string {
	f, err := os.Open(os.Getenv(envRecord))
	if err != nil {
		return ""
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var r record
		var start struct {
			Token string `json:"token"`
		}
		if json.Unmarshal(sc.Bytes(), &r) == nil && r.Kind == "start" && json.Unmarshal(r.Data, &start) == nil {
			return start.Token
		}
	}
	return ""
}

// driveTurn pins the next turn to low over the control socket, runs one
// three-step turn through the proxy, and waits until the footer shows the
// pinned route.
func driveTurn(rt string) error {
	ctl, err := control.Dial(os.Getenv(control.EnvSocket))
	if err != nil {
		return err
	}
	defer func() { _ = ctl.Close() }()
	if err := ctl.Send(route.Command{Op: route.OpPin, Tier: config.TierLow}); err != nil {
		return err
	}
	port, err := os.ReadFile(filepath.Join(rt, PortFileName))
	if err != nil {
		return err
	}
	base := "http://127.0.0.1:" + strings.TrimSpace(string(port)) + "/v1"
	token := recordedToken()
	if err := call(http.MethodGet, base+"/models", token, nil); err != nil {
		return err
	}
	for step := 1; step <= 3; step++ {
		if err := call(http.MethodPost, base+"/responses", token, stepBody(step)); err != nil {
			return err
		}
	}
	timeout := time.After(5 * time.Second)
	for {
		select {
		case s := <-ctl.Updates():
			if s.FooterSynced && s.Footer == pinnedRoute {
				return nil
			}
		case <-timeout:
			return errors.New("footer never showed the pinned route")
		}
	}
}

func call(method, url, token string, body []byte) error {
	req, err := http.NewRequest(method, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", fakeToken)
	req.Header.Set(proxy.HeaderToken, token)
	req.Header.Set(responses.HeaderTurnMetadata,
		`{"session_id":"s1","thread_id":"`+fakeThread+`","turn_id":"turn-1","request_kind":"turn","agent_name":"/root"}`)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s %s: status %d", method, url, resp.StatusCode)
	}
	return nil
}

func stepBody(step int) []byte {
	items := []string{`{"type":"message","role":"user","content":[{"type":"input_text","text":"fix the retry test"}]}`}
	for i := 1; i < step; i++ {
		items = append(items,
			fmt.Sprintf(`{"type":"function_call","call_id":"c%d","name":"shell","arguments":"{}"}`, i),
			fmt.Sprintf(`{"type":"function_call_output","call_id":"c%d","output":"ok"}`, i))
	}
	return []byte(`{"model":"gpt-6.1-sol","reasoning":{"effort":"high"},"stream":true,"input":[` + strings.Join(items, ",") + `]}`)
}
