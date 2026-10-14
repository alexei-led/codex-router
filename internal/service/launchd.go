package service

import (
	"context"
	"encoding/xml"
	"path/filepath"
	"strconv"
	"strings"
	"text/template"

	"github.com/alexei-led/codex-router/internal/proxy"
)

// labelPrefix starts every launchd label: <prefix><home id>.<role>.
const labelPrefix = "com.github.alexei-led.codex-router."

// plistTmpl is a home's edge or brain agent. The edge starts on the first
// connection to the socket launchd holds; after a crash the next connection
// waits in the backlog until launchd starts it again. ThrottleInterval 1
// shortens launchd's respawn delay (default 10 s). The brain runs at load
// and is kept alive.
//
// Logs are appended without rotation; upgrade trigger: a log grows to a size
// users notice.
var plistTmpl = template.Must(template.New("plist").Funcs(template.FuncMap{"xml": xmlText}).Parse(
	`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>{{xml .Label}}</string>
	<key>ProgramArguments</key>
	<array>
{{- range .Args}}
		<string>{{xml .}}</string>
{{- end}}
	</array>
{{- if .Edge}}
	<key>Sockets</key>
	<dict>
		<key>{{xml .Socket}}</key>
		<dict>
			<key>SockNodeName</key>
			<string>{{.Host}}</string>
			<key>SockServiceName</key>
			<string>{{.Port}}</string>
			<key>SockType</key>
			<string>stream</string>
			<key>SockFamily</key>
			<string>IPv4</string>
		</dict>
	</dict>
	<key>ThrottleInterval</key>
	<integer>1</integer>
{{- else}}
	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<true/>
{{- end}}
	<key>StandardOutPath</key>
	<string>{{xml .Log}}</string>
	<key>StandardErrorPath</key>
	<string>{{xml .Log}}</string>
</dict>
</plist>
`))

func xmlText(s string) (string, error) {
	var b strings.Builder
	err := xml.EscapeText(&b, []byte(s))
	return b.String(), err
}

type launchd struct {
	dir string
	uid int
	run Runner
}

func (l launchd) units(homeID string) []Unit {
	out := make([]Unit, 0, 2)
	for _, role := range []string{RoleEdge, RoleBrain} {
		label := labelPrefix + homeID + "." + role
		out = append(out, Unit{Role: role, Name: label, Path: filepath.Join(l.dir, label+".plist"), watch: true})
	}
	return out
}

func (l launchd) render(s Spec, u Unit) ([]byte, error) {
	return renderTemplate(plistTmpl, map[string]any{
		"Label": u.Name, "Args": s.args(u.Role), "Edge": u.Role == RoleEdge,
		"Socket": EdgeSocket, "Host": proxy.LoopbackHost, "Port": s.Port,
		"Log": filepath.Join(s.LogDir, u.Role+".log"),
	})
}

func (l launchd) domain() string { return "gui/" + strconv.Itoa(l.uid) }

func (l launchd) reload(context.Context) error { return nil }

func (l launchd) load(ctx context.Context, us []Unit) error {
	for _, u := range us {
		if _, err := l.run(ctx, "launchctl", "bootstrap", l.domain(), u.Path); err != nil {
			return err
		}
	}
	return nil
}

func (l launchd) unload(ctx context.Context, us []Unit) {
	for _, u := range us {
		_, _ = l.run(ctx, "launchctl", "bootout", l.domain()+"/"+u.Name)
	}
}

// state: a loaded edge is up (launchd holds its port and starts it on
// demand); a brain is up only while it runs.
func (l launchd) state(ctx context.Context, u *Unit) {
	u.State = Down
	out, err := l.run(ctx, "launchctl", "print", l.domain()+"/"+u.Name)
	if err != nil {
		return
	}
	u.loaded = true
	if u.Role == RoleEdge || strings.Contains(out, "state = running") {
		u.State = Up
	}
}

// start bootstraps a unit launchd does not know and kickstarts one it knows
// but does not run.
func (l launchd) start(ctx context.Context, us []Unit) error {
	for _, u := range us {
		var err error
		if u.loaded {
			_, err = l.run(ctx, "launchctl", "kickstart", l.domain()+"/"+u.Name)
		} else {
			_, err = l.run(ctx, "launchctl", "bootstrap", l.domain(), u.Path)
		}
		if err != nil {
			return err
		}
	}
	return nil
}
