package service

import (
	"context"
	"path/filepath"
	"strings"
	"text/template"

	"github.com/alexei-led/codex-router/internal/proxy"
)

// systemd unit name prefixes: <prefix>@<home id>.<type>. The files are
// concrete instances, not templates: each home has its own port and paths.
const (
	edgeUnit  = "codex-router-edge"
	brainUnit = "codex-router-brain"
)

var systemdFuncs = template.FuncMap{"exec": execLine}

// edgeSocketTmpl holds the provider port for the edge service, also while
// the edge is down.
var edgeSocketTmpl = template.Must(template.New("edge.socket").Funcs(systemdFuncs).Parse(
	`[Unit]
Description=codex-router edge socket ({{.HomeID}})

[Socket]
ListenStream={{.Host}}:{{.Port}}
FileDescriptorName={{.Socket}}
Service={{.EdgeService}}

[Install]
WantedBy=sockets.target
`))

// edgeServiceTmpl is started by its socket on the first connection.
var edgeServiceTmpl = template.Must(template.New("edge.service").Funcs(systemdFuncs).Parse(
	`[Unit]
Description=codex-router edge ({{.HomeID}})
Requires={{.EdgeSocket}}
After={{.EdgeSocket}}

[Service]
ExecStart={{exec .Args}}
Restart=on-failure
`))

var brainServiceTmpl = template.Must(template.New("brain.service").Funcs(systemdFuncs).Parse(
	`[Unit]
Description=codex-router brain ({{.HomeID}})

[Service]
ExecStart={{exec .Args}}
Restart=always
RestartSec=1

[Install]
WantedBy=default.target
`))

// execLine quotes args for ExecStart: each in double quotes, with
// backslash and quote escaped, and % and $ doubled so systemd expands no
// specifier or variable.
func execLine(args []string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, `%`, `%%`, `$`, `$$`)
	q := make([]string, len(args))
	for i, a := range args {
		q[i] = `"` + r.Replace(a) + `"`
	}
	return strings.Join(q, " ")
}

type systemd struct {
	dir string
	run Runner
}

func (d systemd) units(homeID string) []Unit {
	u := func(role, name string, watch bool) Unit {
		return Unit{Role: role, Name: name, Path: filepath.Join(d.dir, name), watch: watch}
	}
	return []Unit{
		u(RoleEdge, edgeUnit+"@"+homeID+".socket", true),
		u(RoleEdge, edgeUnit+"@"+homeID+".service", false),
		u(RoleBrain, brainUnit+"@"+homeID+".service", true),
	}
}

func (d systemd) render(s Spec, u Unit) ([]byte, error) {
	data := map[string]any{
		"HomeID": s.HomeID, "Host": proxy.LoopbackHost, "Port": s.Port, "Socket": EdgeSocket,
		"EdgeSocket": edgeUnit + "@" + s.HomeID + ".socket", "EdgeService": edgeUnit + "@" + s.HomeID + ".service",
		"Args": s.args(u.Role),
	}
	switch {
	case strings.HasSuffix(u.Name, ".socket"):
		return renderTemplate(edgeSocketTmpl, data)
	case u.Role == RoleEdge:
		return renderTemplate(edgeServiceTmpl, data)
	}
	return renderTemplate(brainServiceTmpl, data)
}

func (d systemd) systemctl(ctx context.Context, args ...string) (string, error) {
	return d.run(ctx, "systemctl", append([]string{"--user"}, args...)...)
}

func (d systemd) reload(ctx context.Context) error {
	_, err := d.systemctl(ctx, "daemon-reload")
	return err
}

// load enables and starts the watched units: the socket, which starts the
// edge service, and the brain.
func (d systemd) load(ctx context.Context, us []Unit) error {
	_, err := d.systemctl(ctx, append([]string{"enable", "--now"}, watched(us)...)...)
	return err
}

func (d systemd) unload(ctx context.Context, us []Unit) {
	names := make([]string, len(us))
	for i, u := range us {
		names[i] = u.Name
	}
	_, _ = d.systemctl(ctx, append([]string{"disable", "--now"}, names...)...)
}

func (d systemd) state(ctx context.Context, u *Unit) {
	u.State = Down
	if out, _ := d.systemctl(ctx, "is-active", u.Name); strings.TrimSpace(out) == "active" {
		u.State = Up
	}
}

func (d systemd) start(ctx context.Context, us []Unit) error {
	_, err := d.systemctl(ctx, append([]string{"start", "--no-block"}, watched(us)...)...)
	return err
}

func watched(us []Unit) []string {
	var names []string
	for _, u := range us {
		if u.watch {
			names = append(names, u.Name)
		}
	}
	return names
}
