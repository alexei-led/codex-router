package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/alexei-led/codex-router/internal/home"
	"github.com/alexei-led/codex-router/internal/service"
	"github.com/alexei-led/codex-router/internal/setup"
)

const envTestBinary = "CODEX_ROUTER_TEST_AS_BINARY"

const noRaceExitSleep = "GORACE=atexit_sleep_ms=0"

func TestMain(m *testing.M) {
	if os.Getenv(envTestBinary) == "1" {
		os.Exit(run(context.Background(), os.Args[1:], os.Environ(), os.Stdin, os.Stdout, os.Stderr))
	}
	os.Exit(m.Run())
}

const (
	marketplacePath = "../../.agents/plugins/marketplace.json"
	pluginDir       = "../../plugins/codex-router"
)

type marketplace struct {
	Name      string `json:"name"`
	Interface struct {
		DisplayName string `json:"displayName"`
	} `json:"interface"`
	Plugins []struct {
		Name   string `json:"name"`
		Source struct {
			Source string `json:"source"`
			Path   string `json:"path"`
		} `json:"source"`
		Category string `json:"category"`
	} `json:"plugins"`
}

type pluginManifest struct {
	Name        string   `json:"name"`
	Version     string   `json:"version"`
	Description string   `json:"description"`
	Keywords    []string `json:"keywords"`
	Skills      string   `json:"skills"`
	Hooks       string   `json:"hooks"`
	Interface   struct {
		DisplayName      string `json:"displayName"`
		ShortDescription string `json:"shortDescription"`
		DeveloperName    string `json:"developerName"`
		Category         string `json:"category"`
		WebsiteURL       string `json:"websiteURL"`
	} `json:"interface"`
}

type hookHandler struct {
	Type    string `json:"type"`
	Command string `json:"command"`
	Timeout int    `json:"timeout"`
	Async   bool   `json:"async"`
}

type hooksFile struct {
	Description string `json:"description"`
	Hooks       map[string][]struct {
		Matcher *string       `json:"matcher"`
		Hooks   []hookHandler `json:"hooks"`
	} `json:"hooks"`
}

func decodeStrict(t *testing.T, path string, v any) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
}

func pluginPath(t *testing.T, root, rel string) string {
	t.Helper()
	clean, ok := strings.CutPrefix(rel, "./")
	if !ok || clean == "" || !filepath.IsLocal(clean) || filepath.Clean(clean) != clean {
		t.Fatalf("path %q must be ./-relative and inside %s", rel, root)
	}
	p := filepath.Join(root, clean)
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("path %q: %v", rel, err)
	}
	return p
}

var codexHookEvents = []string{
	"PreToolUse", "PermissionRequest", "PostToolUse", "PreCompact", "PostCompact", "SessionStart",
	"SessionEnd", "UserPromptSubmit", "SubagentStart", "SubagentStop", "Stop", "Interrupt",
}

var hookCommand = regexp.MustCompile(`^sh "\$PLUGIN_ROOT/([^"]+)"((?: [a-z]+)*)$`)

func readHooks(t *testing.T) (hooksFile, map[string][]hookHandler) {
	t.Helper()
	var p pluginManifest
	decodeStrict(t, filepath.Join(pluginDir, ".codex-plugin", "plugin.json"), &p)
	var hf hooksFile
	decodeStrict(t, pluginPath(t, pluginDir, p.Hooks), &hf)
	byEvent := map[string][]hookHandler{}
	for event, groups := range hf.Hooks {
		for _, g := range groups {
			byEvent[event] = append(byEvent[event], g.Hooks...)
		}
	}
	return hf, byEvent
}

func TestPluginManifests(t *testing.T) {
	var mp marketplace
	decodeStrict(t, marketplacePath, &mp)
	repo := filepath.Dir(filepath.Dir(filepath.Dir(marketplacePath)))
	if mp.Name == "" || len(mp.Plugins) != 1 {
		t.Fatalf("marketplace %+v, want a name and one plugin", mp)
	}
	entry := mp.Plugins[0]
	if entry.Source.Source != "local" {
		t.Errorf("plugin source %q, want local", entry.Source.Source)
	}
	if got, want := pluginPath(t, repo, entry.Source.Path), filepath.Clean(pluginDir); got != want {
		t.Errorf("marketplace points at %s, want %s", got, want)
	}

	var p pluginManifest
	decodeStrict(t, filepath.Join(pluginDir, ".codex-plugin", "plugin.json"), &p)
	if p.Name != entry.Name || p.Version == "" || p.Description == "" {
		t.Errorf("plugin.json name %q version %q description %q; want the marketplace's name %q, a version and a description", p.Name, p.Version, p.Description, entry.Name)
	}
	skills := pluginPath(t, pluginDir, p.Skills)

	hf, byEvent := readHooks(t)
	if hf.Description == "" {
		t.Error("hooks.json has no description")
	}
	for event, handlers := range byEvent {
		if !slices.Contains(codexHookEvents, event) {
			t.Errorf("hooks.json: unknown event %q", event)
		}
		for _, h := range handlers {
			m := hookCommand.FindStringSubmatch(h.Command)
			if h.Type != "command" || m == nil || h.Timeout <= 0 {
				t.Errorf("%s hook %+v: want type command, `sh \"$PLUGIN_ROOT/<script>\" args`, a timeout", event, h)
				continue
			}
			pluginPath(t, pluginDir, "./"+m[1])
		}
	}
	for _, event := range []string{"SessionStart", "UserPromptSubmit", "Stop"} {
		if len(byEvent[event]) != 1 {
			t.Errorf("%s has %d hooks, want 1", event, len(byEvent[event]))
		}
	}
	if start := byEvent["SessionStart"]; len(start) == 1 && !start[0].Async {
		t.Error("the SessionStart hook must be async: Codex must not wait for service ensure")
	}

	dirs, err := os.ReadDir(skills)
	if err != nil || len(dirs) == 0 {
		t.Fatalf("skills dir: %v, %d entries", err, len(dirs))
	}
	for _, d := range dirs {
		checkSkill(t, filepath.Join(skills, d.Name()))
	}
}

func checkSkill(t *testing.T, dir string) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(b)
	front, ok := strings.CutPrefix(text, "---\n")
	front, _, closed := strings.Cut(front, "\n---\n")
	if !ok || !closed {
		t.Fatalf("%s: no --- frontmatter", dir)
	}
	fields := map[string]string{}
	sc := bufio.NewScanner(strings.NewReader(front))
	for sc.Scan() {
		if k, v, ok := strings.Cut(sc.Text(), ": "); ok {
			fields[k] = v
		}
	}
	if name := fields["name"]; name != filepath.Base(dir) || len(name) > 64 {
		t.Errorf("%s: name %q, want the dir name, at most 64 chars", dir, name)
	}
	if fields["description"] == "" {
		t.Errorf("%s: no description", dir)
	}
	if !strings.Contains(text, "scripts/codex-router.sh") {
		t.Errorf("%s: does not name the wrapper", dir)
	}
	pluginPath(t, pluginDir, "./scripts/codex-router.sh")
	subs := regexp.MustCompile("`ctl ([a-z]+)").FindAllStringSubmatch(text, -1)
	if len(subs) == 0 {
		t.Errorf("%s: names no ctl command", dir)
	}
	for _, m := range subs {
		if _, ok := ctlOps[m[1]]; !ok {
			t.Errorf("%s: ctl %s is not a ctl subcommand", dir, m[1])
		}
	}
}

func TestPluginShipsScriptsOnly(t *testing.T) {
	var scripts []string
	err := filepath.WalkDir(pluginDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		switch filepath.Ext(p) {
		case ".json", ".md":
		case ".sh":
			scripts = append(scripts, p)
		default:
			t.Errorf("%s: the plugin ships no binary or other file", p)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(scripts) == 0 {
		t.Fatal("the plugin ships no script")
	}
	var all bytes.Buffer
	for _, p := range scripts {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		all.Write(b)
		all.WriteByte('\n')
	}
	check := exec.Command("sh", "-n")
	check.Stdin = &all
	if out, err := check.CombinedOutput(); err != nil {
		for _, p := range scripts {
			if out, err := exec.Command("sh", "-n", p).CombinedOutput(); err != nil {
				t.Errorf("%s: sh -n: %v %s", p, err, out)
			}
		}
		t.Fatalf("sh -n: %v %s", err, out)
	}
}

func hookEnv(t *testing.T, codexHome, fakeBin string) []string {
	t.Helper()
	bin, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	user := t.TempDir()
	return []string{
		"PATH=" + fakeBin + ":/usr/bin:/bin", "HOME=" + user, "TMPDIR=" + user,
		"CODEX_HOME=" + codexHome, "XDG_STATE_HOME=" + t.TempDir(),
		"CODEX_ROUTER_BIN=" + bin, envTestBinary + "=1", noRaceExitSleep,
	}
}

func TestPluginHooksReturnFastWithServicesDown(t *testing.T) {
	abs, err := filepath.Abs(pluginDir)
	if err != nil {
		t.Fatal(err)
	}
	codexHome := t.TempDir()
	h, err := home.Resolve(func(k string) string { return map[string]string{"CODEX_HOME": codexHome}[k] })
	if err != nil {
		t.Fatal(err)
	}
	fakeBin := t.TempDir()
	fifo := filepath.Join(t.TempDir(), "release")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	hold, err := os.OpenFile(fifo, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	release := sync.OnceFunc(func() { _, _ = hold.WriteString(strings.Repeat("\n", 64)) })
	t.Cleanup(func() {
		release()
		_ = hold.Close()
	})
	smLog := filepath.Join(t.TempDir(), "service-manager.log")
	fake := "#!/bin/sh\necho \"$0 $*\" >>" + smLog + "\ncase \"$*\" in\n*print*|*is-active*) echo inactive; exit 3 ;;\nesac\nread _ <>" + fifo + "\necho " + doneMark + " >>" + smLog + "\n"
	for _, name := range []string{"launchctl", "systemctl"} {
		if err := os.WriteFile(filepath.Join(fakeBin, name), []byte(fake), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	env := hookEnv(t, codexHome, fakeBin)
	user := strings.TrimPrefix(env[1], "HOME=")
	mgr, err := service.New(service.Options{GOOS: runtime.GOOS, Home: user, UID: os.Getuid()})
	if err != nil {
		t.Skip(err)
	}
	for _, u := range mgr.Units(h.ID) {
		if err := os.MkdirAll(filepath.Dir(u.Path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(u.Path, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	units := len(mgr.Status(t.Context(), h.ID))

	_, byEvent := readHooks(t)
	t.Run("hooks", func(t *testing.T) {
		for _, event := range []string{"SessionStart", "UserPromptSubmit", "Stop"} {
			t.Run(event, func(t *testing.T) {
				t.Parallel()
				ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
				defer cancel()
				cmd := exec.CommandContext(ctx, "sh", "-c", byEvent[event][0].Command)
				cmd.Env = append(slices.Clone(env), "PLUGIN_ROOT="+abs)
				cmd.Stdin = strings.NewReader(`{"session_id":"t-1","hook_event_name":"` + event + `","cwd":"/"}`)
				var stdout bytes.Buffer
				cmd.Stdout = &stdout
				if err := cmd.Run(); err != nil {
					t.Fatalf("hook: %v", err)
				}
				if event != "SessionStart" && strings.TrimSpace(stdout.String()) != "{}" {
					t.Errorf("hook stdout %q, want {}", stdout.String())
				}
			})
		}
	})

	start := "bootstrap"
	if runtime.GOOS == "linux" {
		start = "start --no-block"
	}
	waitForLog(t, smLog, "the units started", func(log string) bool { return strings.Contains(log, start) })
	release()
	waitForLog(t, smLog, "every start to finish", func(log string) bool { return strings.Count(log, doneMark) >= units })
}

const doneMark = "start-finished"

func waitForLog(t *testing.T, path, what string, ok func(string) bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Minute)
	for {
		b, _ := os.ReadFile(path)
		if ok(string(b)) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s; service manager log:\n%s", what, b)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestPluginWrapperFindsTheRouter(t *testing.T) {
	bin, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	codexHome := t.TempDir()
	installed := filepath.Join(codexHome, "plugins", "cache", "codex-router", "codex-router", "0.1.0")
	if err := os.CopyFS(installed, os.DirFS(pluginDir)); err != nil {
		t.Fatal(err)
	}
	canonHome, err := filepath.EvalSymlinks(codexHome)
	if err != nil {
		t.Fatal(err)
	}
	state := t.TempDir()
	record := "# setup wrote this\nCODEX_ROUTER_BIN=" + bin + "\nXDG_STATE_HOME=" + state + "\n"
	user := t.TempDir()
	base := []string{"PATH=/usr/bin:/bin", "HOME=" + user, "TMPDIR=" + user, envTestBinary + "=1", noRaceExitSleep, "PLUGIN_ROOT=" + installed}

	cases := []struct {
		name     string
		record   bool
		args     []string
		wantCode int
		wantOut  string
		wantErr  string
	}{
		{"record names the binary; home from PLUGIN_ROOT", true, []string{"service", "status"}, 0, "Codex home " + filepath.Base(codexHome) + ": " + canonHome, ""},
		{"record names the state dir", true, []string{"sessions", "--json"}, 0, "[]", ""},
		{"no binary: the hook prints its empty answer", false, []string{"hook"}, 0, "{}", ""},
		{"no binary: ctl fails with a hint", false, []string{"ctl", "status"}, 127, "", "codex-router not found"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := filepath.Join(codexHome, setup.PluginEnvFile)
			_ = os.Remove(p)
			if tc.record {
				if err := os.WriteFile(p, []byte(record), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			cmd := exec.Command("sh", append([]string{filepath.Join(installed, "scripts", "codex-router.sh")}, tc.args...)...)
			cmd.Env = base
			cmd.Stdin = strings.NewReader("{}")
			var stdout, stderr bytes.Buffer
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			err := cmd.Run()
			code := 0
			var exitErr *exec.ExitError
			switch {
			case errors.As(err, &exitErr):
				code = exitErr.ExitCode()
			case err != nil:
				t.Fatal(err)
			}
			if code != tc.wantCode || !strings.Contains(stdout.String(), tc.wantOut) || !strings.Contains(stderr.String(), tc.wantErr) {
				t.Errorf("exit %d, stdout %q, stderr %q; want %d, %q, %q", code, stdout.String(), stderr.String(), tc.wantCode, tc.wantOut, tc.wantErr)
			}
		})
	}
	if _, err := os.Stat(filepath.Join(state, "codex-router", "registry")); err != nil {
		t.Errorf("the recorded state dir was not used: %v", err)
	}
}
