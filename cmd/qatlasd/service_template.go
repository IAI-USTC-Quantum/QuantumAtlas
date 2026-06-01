package main

import (
	"bytes"
	"strings"
	"text/template"

	"github.com/kardianos/service"
)

// serviceUnitTemplate is the systemd unit body installed by `qatlasd
// service install`. It is passed to kardianos/service via
// Config.Option["SystemdScript"], replacing the library's default template
// in full. The library's default template lacks fields we need (After=,
// Type=, KillSignal=, TimeoutStopSec=, the entire hardening block, and a
// configurable RestartSec — default 120s is too long), and ships
// EnvironmentFile=/etc/sysconfig/<name> which doesn't match our deployment
// convention. Replacing the whole script is simpler than threading several
// dozen Option["..."] overrides, and the library author documents
// Option["SystemdScript"] as the supported customisation seam.
//
// Field origins (all rendered by kardianos/service or
// renderSystemdUnit below — keep this list in sync):
//
//   {{.Description}}       -> service.Config.Description
//   {{.UserName}}          -> service.Config.UserName (empty in user mode)
//   {{.WorkingDirectory}}  -> service.Config.WorkingDirectory
//   {{.Path|cmdEscape}}    -> os.Executable() of the running qatlasd
//   {{range .Arguments}}   -> service.Config.Arguments ("serve" "--http=...")
//   {{range .EnvVars}}     -> service.Config.EnvVars (we inject QATLAS_DOTENV)
//   {{index .Option "X"}}  -> service.Config.Option[X] (we inject
//                              ReadWritePaths and WantedBy)
//
// The hardening block is unconditional — operators who want a minimal
// unit can `systemctl edit qatlasd` and override individual
// directives via a drop-in. We don't expose a --no-hardening flag
// because production should always run hardened.
const serviceUnitTemplate = `[Unit]
Description={{.Description}}
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
{{- if .UserName}}
User={{.UserName}}
{{- end}}
WorkingDirectory={{.WorkingDirectory}}
{{range $k, $v := .EnvVars -}}
Environment={{$k}}={{$v}}
{{end -}}
ExecStart={{.Path|cmdEscape}}{{range .Arguments}} {{.|cmd}}{{end}}
Restart=on-failure
RestartSec=5
KillSignal=SIGINT
TimeoutStopSec=15

# systemd sandboxing — defense-in-depth hardening; see systemd.exec(5).
# ReadWritePaths must cover every directory the server writes to
# (PB_DATA_DIR, DATA_DIR, the wiki checkout, and the .env directory).
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=full
ProtectHome=no
ReadWritePaths={{index .Option "ReadWritePaths"}}
LockPersonality=true
RestrictRealtime=true

[Install]
WantedBy={{index .Option "WantedBy"}}
`

// templateFuncs mirrors the helper map kardianos/service registers when
// it parses Option["SystemdScript"]. We duplicate them so renderSystemdUnit
// (used by --dry-run preview and tests) produces byte-identical output to
// what the library writes during Install().
//
// Source of truth: kardianos/service service.go `tf` var.
var templateFuncs = template.FuncMap{
	"cmd": func(s string) string {
		return `"` + strings.ReplaceAll(s, `"`, `\"`) + `"`
	},
	"cmdEscape": func(s string) string {
		return strings.ReplaceAll(s, " ", `\x20`)
	},
}

// renderSystemdUnit renders serviceUnitTemplate against the same struct
// kardianos/service's systemd backend uses internally. The output is
// byte-identical to the file the library would write during svc.Install()
// for the same Config + execPath.
//
// Used by:
//   - `service install --dry-run` (print rendered unit, do not write)
//   - `service install` interactive preview before [Y/n] write prompt
//   - service_cmd_test.go template snapshot tests
//
// execPath is the resolved binary path (what os.Executable() returns at
// install time). Tests pass a fixed string so snapshots are stable.
func renderSystemdUnit(cfg *service.Config, execPath string) (string, error) {
	tmpl, err := template.New("qatlasd.service").Funcs(templateFuncs).Parse(serviceUnitTemplate)
	if err != nil {
		return "", err
	}

	// Mirror the anonymous struct kardianos/service builds in its
	// systemd.Install — just the fields our template references.
	data := struct {
		*service.Config
		Path string
	}{
		Config: cfg,
		Path:   execPath,
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", err
	}
	return buf.String(), nil
}
