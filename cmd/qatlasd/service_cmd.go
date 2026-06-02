package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/kardianos/service"
	"github.com/spf13/cobra"
)

// defaultServiceName is the systemd unit / launchd plist / SCM service
// name installed by `service install` when --name isn't passed.
const defaultServiceName = "qatlasd"

// noopProgram satisfies service.Interface. The library requires this
// interface to construct service.Service even for install/uninstall calls
// that never invoke Run(). The actual daemon process is the existing
// `qatlasd serve` subcommand (PocketBase's HTTP server), not anything
// the library spawns — so Start/Stop here are no-ops.
type noopProgram struct{}

func (noopProgram) Start(service.Service) error { return nil }
func (noopProgram) Stop(service.Service) error  { return nil }

// serviceInstallOpts captures the flags accepted by `service install`.
type serviceInstallOpts struct {
	Name       string
	Mode       string // "user" | "system" | "" (autodetect)
	DotenvPath string
	Bind       string
	DryRun     bool
	Force      bool
}

// NewServiceCommand wires the `service` subcommand tree onto the
// PocketBase root command. Registered from main.go::main() before
// app.Start(), same registration timing constraint as `pat` and `storage`.
func NewServiceCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "service",
		Short: "Manage qatlasd as a managed system service (systemd / launchd / SCM)",
		Long: `Manage qatlasd as a managed system service.

Wraps github.com/kardianos/service to install a sandboxed systemd unit
(or launchd plist / Windows SCM entry on other platforms). After install,
service management goes through either this command (qatlasd service
start/stop/status) or natively via systemctl — they are equivalent because
the library is just a thin systemctl wrapper that exits after Install().

On Linux the installed unit ships with defense-in-depth hardening
(NoNewPrivileges, PrivateTmp, ProtectSystem=full, ReadWritePaths,
LockPersonality, RestrictRealtime). On macOS / Windows the library
defaults are used and have not been production-tested — file an issue.`,
	}
	cmd.AddCommand(newServiceInstallCommand())
	cmd.AddCommand(newServiceUninstallCommand())
	cmd.AddCommand(newServiceStartCommand())
	cmd.AddCommand(newServiceStopCommand())
	cmd.AddCommand(newServiceRestartCommand())
	cmd.AddCommand(newServiceStatusCommand())
	return cmd
}

// install --------------------------------------------------------------------

func newServiceInstallCommand() *cobra.Command {
	opts := serviceInstallOpts{}
	cmd := &cobra.Command{
		Use:   "install",
		Short: "Install qatlasd as a managed service",
		Long: `Install qatlasd as a managed service.

Interactive mode (default, TTY): prompts for mode (user/system), confirms
the auto-detected .env path, renders the unit content for [Y/n] review,
then writes the unit and starts the service.

Non-interactive mode (no TTY, e.g. CI / piped stdin): --mode and --force
are required, no prompts are issued.

# CRITICAL — sudo invocation pattern (system mode)

System mode writes /etc/systemd/system/<name>.service, which requires
EUID=0. The rendered unit's User= field and ReadWritePaths= anchor are
ALSO inferred from $SUDO_USER (see resolveSystemUser / effectiveHomeDir
above). The only correct shapes are:

  RIGHT: sudo qatlasd service install --mode system ...
         (EUID=0, SUDO_USER=<you>; unit gets User=<you>)

  RIGHT: sudo bash deploy.sh        # script body runs qatlasd directly
         (script inherits EUID=0 + SUDO_USER=<you>; unit gets User=<you>)

  WRONG: sudo -u <user> qatlasd service install --mode system ...
         Two failure modes stacked:
           1. EUID becomes <user> (not 0), so writing the unit file fails
              with "open /etc/systemd/system/<name>.service: permission denied".
           2. Even if step 1 somehow succeeded, SUDO_USER would be "root"
              (the sudo invoker), so resolveSystemUser() would emit
              User=root into the unit — the daemon would run as root,
              not as <user>.

If you need the daemon to run as a user OTHER than the one invoking
sudo, set the user explicitly in the rendered unit's User= field by
running sudo from THAT user's shell (login as them, then sudo). The
"sudo -u" sandwich is never the right answer.

Examples:
  # Interactive — auto-detect everything, prompt at each step
  qatlasd service install

  # CI-style — fully explicit, no prompts
  qatlasd service install --mode user \
      --dotenv-path ~/QuantumAtlas/.env --force

  # CI-style system mode (run as your normal user, sudo to install)
  sudo qatlasd service install --mode system \
      --dotenv-path /etc/quantum-atlas/.env --force

  # Preview the rendered unit without writing
  qatlasd service install --dry-run --mode system \
      --dotenv-path /etc/quantum-atlas/.env`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runServiceInstall(opts)
		},
	}
	cmd.Flags().StringVar(&opts.Name, "name", defaultServiceName, "Service unit name (<name>.service on Linux)")
	cmd.Flags().StringVar(&opts.Mode, "mode", "", `"user" or "system" (default: auto-detected from uid; prompted in TTY)`)
	cmd.Flags().StringVar(&opts.DotenvPath, "dotenv-path", "",
		"Path to .env file (env: QATLAS_DOTENV; auto-detect order: $QATLAS_DOTENV, then ~/QuantumAtlas/.env, then ./.env)")
	cmd.Flags().StringVar(&opts.Bind, "bind", "127.0.0.1:4200",
		"HTTP bind address for `serve --http=...` (runtime env: QATLAS_HTTP_ADDR or QATLAS_SERVER_HOST+QATLAS_SERVER_PORT)")
	cmd.Flags().BoolVar(&opts.DryRun, "dry-run", false, "Render the unit to stdout without writing or reloading systemd")
	cmd.Flags().BoolVar(&opts.Force, "force", false, "Overwrite an existing unit without prompting; required in non-TTY contexts")
	return cmd
}

func runServiceInstall(opts serviceInstallOpts) error {
	tty := isTTY()
	if !tty && (!opts.Force || opts.Mode == "") {
		return errors.New("non-interactive install requires --mode and --force")
	}
	if err := resolveMode(&opts, tty); err != nil {
		return err
	}
	if err := guardSudoUserModeMismatch(opts.Mode, os.Geteuid(), os.Getenv("SUDO_USER")); err != nil {
		return err
	}
	if err := resolveDotenvPath(&opts, tty); err != nil {
		return err
	}

	cfg, err := buildServiceConfig(opts)
	if err != nil {
		return err
	}
	execPath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("os.Executable: %w", err)
	}

	rendered, err := renderSystemdUnit(cfg, execPath)
	if err != nil {
		return fmt.Errorf("render unit: %w", err)
	}
	fmt.Println("--- rendered unit ---")
	fmt.Print(rendered)
	fmt.Println("--- end ---")

	if opts.DryRun {
		fmt.Println("(--dry-run: nothing written)")
		return nil
	}

	if tty && !opts.Force {
		ok, err := promptYesNo("Write this unit and start the service?", true)
		if err != nil {
			return err
		}
		if !ok {
			return errors.New("aborted by user")
		}
	}

	svc, err := service.New(noopProgram{}, cfg)
	if err != nil {
		return fmt.Errorf("construct service: %w", err)
	}

	// Detect existing unit and offer a nicer overwrite path than the
	// library's "Init already exists" error from Install().
	if status, statusErr := svc.Status(); statusErr == nil && status != service.StatusUnknown {
		if !opts.Force {
			if !tty {
				return fmt.Errorf("service %s already installed; pass --force to overwrite", opts.Name)
			}
			ok, err := promptYesNo(fmt.Sprintf("Service %s is already installed. Overwrite?", opts.Name), false)
			if err != nil {
				return err
			}
			if !ok {
				return errors.New("aborted by user")
			}
		}
		_ = svc.Stop()
		if err := svc.Uninstall(); err != nil {
			return fmt.Errorf("remove existing unit: %w", err)
		}
	}

	if err := svc.Install(); err != nil {
		return fmt.Errorf("install: %w", err)
	}
	fmt.Printf("Installed service %s (mode=%s).\n", opts.Name, opts.Mode)

	if err := svc.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "warning: install succeeded but start failed: %v\n", err)
		return nil
	}
	fmt.Printf("Started service %s. Manage via `qatlasd service status` or `systemctl%s status %s`.\n",
		opts.Name, userFlagFor(opts.Mode), opts.Name)
	return nil
}

// resolveMode picks user vs system mode. Explicit --mode wins; otherwise
// the uid is used as a hint (root -> system, else user), prompted for
// confirmation in TTY contexts and rejected in non-TTY.
func resolveMode(opts *serviceInstallOpts, tty bool) error {
	if opts.Mode != "" {
		switch opts.Mode {
		case "user", "system":
			return nil
		default:
			return fmt.Errorf("invalid --mode %q (want user|system)", opts.Mode)
		}
	}
	suggested := "user"
	if os.Geteuid() == 0 {
		suggested = "system"
	}
	if !tty {
		return fmt.Errorf("--mode required in non-interactive context (suggested: %s)", suggested)
	}
	ok, err := promptYesNo(fmt.Sprintf("Detected uid=%d, install in %s mode?", os.Geteuid(), suggested), true)
	if err != nil {
		return err
	}
	if ok {
		opts.Mode = suggested
	} else if suggested == "user" {
		opts.Mode = "system"
	} else {
		opts.Mode = "user"
	}
	return nil
}

// resolveDotenvPath fills opts.DotenvPath: explicit flag > $QATLAS_DOTENV >
// auto-detect, then validates the path exists and is a regular file.
//
// Whenever the path isn't from --dotenv-path (i.e. operator didn't type it
// in this command line), we print an announcement to stdout so the chosen
// path is visible in deploy logs without scrolling to the rendered-unit
// preview. Three sources, three announcement styles:
//   - --dotenv-path: silent (operator just typed it, no surprise)
//   - $QATLAS_DOTENV: stdout note (env var might come from .bashrc / parent
//     shell / systemd unit operator didn't write themselves)
//   - autodetect: TTY prompts [Y/n]; non-TTY prints to stdout
func resolveDotenvPath(opts *serviceInstallOpts, tty bool) error {
	if opts.DotenvPath != "" {
		return validateDotenvPath(opts.DotenvPath)
	}
	if env := strings.TrimSpace(os.Getenv("QATLAS_DOTENV")); env != "" {
		fmt.Printf("Using .env from $QATLAS_DOTENV: %s\n", env)
		opts.DotenvPath = env
		return validateDotenvPath(env)
	}
	candidates := autodetectDotenvCandidates()
	var found string
	for _, c := range candidates {
		if info, err := os.Stat(c); err == nil && !info.IsDir() {
			found = c
			break
		}
	}
	if found == "" {
		return fmt.Errorf("could not auto-detect .env file (tried: %s); pass --dotenv-path", strings.Join(candidates, ", "))
	}
	if tty {
		ok, err := promptYesNo(fmt.Sprintf("Use auto-detected .env at %s?", found), true)
		if err != nil {
			return err
		}
		if !ok {
			return errors.New("aborted; pass --dotenv-path explicitly")
		}
	} else {
		// Non-TTY (CI / --force / sudo bash script) — can't prompt, but
		// must not stay silent: deploy logs need to show which .env was
		// picked, in case autodetect found the wrong one and the operator
		// only notices when the service fails to start.
		fmt.Printf("Auto-detected .env: %s (override with --dotenv-path)\n", found)
	}
	opts.DotenvPath = found
	return nil
}

func autodetectDotenvCandidates() []string {
	out := []string{}
	if home := effectiveHomeDir(); home != "" {
		out = append(out, filepath.Join(home, "QuantumAtlas", ".env"))
	}
	if cwd, err := os.Getwd(); err == nil {
		out = append(out, filepath.Join(cwd, ".env"))
	}
	return out
}

func validateDotenvPath(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("dotenv path %s: %w", path, err)
	}
	if info.IsDir() {
		return fmt.Errorf("dotenv path %s is a directory, expected a regular file", path)
	}
	return nil
}

// effectiveHomeDir returns the home directory of the user the server will
// actually run as, NOT the home of the current process. The distinction
// matters under sudo: sudo resets $HOME to /root by default (see sudoers(5)
// `env_reset`), so a plain os.UserHomeDir() during
// `sudo qatlasd service install --mode system` would yield /root,
// poisoning ReadWritePaths (and the .env autodetect candidates) with
// /root/.local/share/quantum-atlas — a path the eventual `User=<sudo-user>`
// daemon will never write to, making the hardening grant useless.
//
// Resolution order:
//  1. $SUDO_USER set + lookup succeeds → that user's home (the sudo case)
//  2. plain os.UserHomeDir() → reads $HOME (the normal case)
//
// Returns empty string if both fail; callers gate on that.
func effectiveHomeDir() string {
	if sudoUser := strings.TrimSpace(os.Getenv("SUDO_USER")); sudoUser != "" {
		if u, err := user.Lookup(sudoUser); err == nil && u.HomeDir != "" {
			return u.HomeDir
		}
	}
	home, _ := os.UserHomeDir()
	return home
}

// computeReadWritePaths returns the paths systemd should grant write access
// to under ReadWritePaths=. We include:
//   - the .env directory (server may rewrite .env in future migrations)
//   - $XDG_DATA_HOME/quantum-atlas (PB_DATA_DIR / DATA_DIR / RAW_DIR fallback)
//   - ~/QuantumAtlas-Wiki if it exists (git fetch writes refs)
//
// Duplicates are removed. Order is preserved for stable test snapshots.
//
// "Home" here resolves via effectiveHomeDir so sudo invocations target the
// real daemon-user's home, not /root — see effectiveHomeDir docs.
func computeReadWritePaths(absDotenv string) []string {
	paths := []string{filepath.Dir(absDotenv)}

	if home := effectiveHomeDir(); home != "" {
		share := strings.TrimSpace(os.Getenv("XDG_DATA_HOME"))
		if share == "" {
			share = filepath.Join(home, ".local", "share")
		}
		paths = append(paths, filepath.Join(share, "quantum-atlas"))

		wiki := filepath.Join(home, "QuantumAtlas-Wiki")
		if info, err := os.Stat(wiki); err == nil && info.IsDir() {
			paths = append(paths, wiki)
		}
	}

	seen := make(map[string]bool, len(paths))
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		if seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	return out
}

// buildServiceConfig assembles a *service.Config aligned with our template.
// Exported (within the package) so tests can build a fixed config without
// going through the interactive install flow.
func buildServiceConfig(opts serviceInstallOpts) (*service.Config, error) {
	absDotenv, err := filepath.Abs(opts.DotenvPath)
	if err != nil {
		return nil, fmt.Errorf("dotenv abs: %w", err)
	}
	workingDir := filepath.Dir(absDotenv)
	rwPaths := computeReadWritePaths(absDotenv)

	wantedBy := "default.target"
	userName := ""
	if opts.Mode == "system" {
		wantedBy = "multi-user.target"
		userName = resolveSystemUser()
	}

	return &service.Config{
		Name:             opts.Name,
		DisplayName:      "QuantumAtlas server",
		Description:      "QuantumAtlas server (Go + PocketBase)",
		UserName:         userName,
		WorkingDirectory: workingDir,
		Arguments:        []string{"serve", "--http=" + opts.Bind},
		EnvVars: map[string]string{
			"QATLAS_DOTENV": absDotenv,
		},
		Option: service.KeyValue{
			"SystemdScript":  serviceUnitTemplate,
			"UserService":    opts.Mode == "user",
			"ReadWritePaths": strings.Join(rwPaths, " "),
			"WantedBy":       wantedBy,
		},
	}, nil
}

// resolveSystemUser picks a sensible User= value for system-mode units.
// Prefers $SUDO_USER (operator ran `sudo qatlasd service install`),
// falls back to the username corresponding to the current effective uid,
// finally empty (caller will run as root, not recommended but allowed).
//
// Sibling of effectiveHomeDir — same sudo-aware resolution philosophy, but
// returns a username string rather than a home directory path.
func resolveSystemUser() string {
	if u := strings.TrimSpace(os.Getenv("SUDO_USER")); u != "" {
		return u
	}
	if u, err := user.LookupId(strconv.Itoa(os.Geteuid())); err == nil {
		return u.Username
	}
	return ""
}

// guardSudoUserModeMismatch refuses the broken `sudo qatlasd service
// install --mode user` combination upfront.
//
// Why it's broken: kardianos/service's user-mode backend computes the unit
// path via os.UserHomeDir(), which under sudo returns /root (sudo's
// env_reset). The unit ends up at /root/.config/systemd/user/<name>.service
// — orphaned from the would-be daemon user's systemd --user instance, and
// invisible to `systemctl --user`. The library bug is upstream; we can't fix
// it from here, but we can stop the user before they create the orphan.
//
// The legitimate combinations are:
//   - sudo + --mode system  (production daemon, writes to /etc/systemd/system)
//   - no sudo + --mode user (per-user daemon, writes to ~/.config/systemd/user)
// Both work cleanly after the effectiveHomeDir() fix.
//
// Pure function (no process state read) so it's trivially testable; the
// install command supplies live values via os.Geteuid() / os.Getenv.
func guardSudoUserModeMismatch(mode string, euid int, sudoUser string) error {
	insideSudo := euid == 0 && strings.TrimSpace(sudoUser) != ""
	if mode == "user" && insideSudo {
		return fmt.Errorf("refusing `sudo ... service install --mode user`: " +
			"sudo + user mode is not supported (the resulting unit would land in /root, not the invoking user's systemd --user dir). " +
			"Use one of:\n" +
			"  - to install a per-user service: drop sudo (`qatlasd service install --mode user ...`)\n" +
			"  - to install a system service: pass `--mode system` instead")
	}
	return nil
}

// lifecycle (uninstall / start / stop / restart / status) -------------------

type serviceLifecycleOpts struct {
	Name string
	Mode string
}

func addLifecycleFlags(cmd *cobra.Command, opts *serviceLifecycleOpts) {
	cmd.Flags().StringVar(&opts.Name, "name", defaultServiceName, "Service unit name")
	cmd.Flags().StringVar(&opts.Mode, "mode", "", `"user" or "system" (default: auto-detected from uid)`)
}

// newManagedService builds a minimal service.Service for lifecycle calls
// that don't need the full template-aware Config (start/stop/status only
// need Name and the UserService Option to pick systemctl --user vs system).
func newManagedService(opts serviceLifecycleOpts) (service.Service, error) {
	userService := opts.Mode == "user" || (opts.Mode == "" && os.Geteuid() != 0)
	cfg := &service.Config{
		Name: opts.Name,
		Option: service.KeyValue{
			"UserService": userService,
		},
	}
	return service.New(noopProgram{}, cfg)
}

func newServiceUninstallCommand() *cobra.Command {
	opts := serviceLifecycleOpts{}
	cmd := &cobra.Command{
		Use:   "uninstall",
		Short: "Stop and remove the installed service",
		RunE: func(cmd *cobra.Command, args []string) error {
			svc, err := newManagedService(opts)
			if err != nil {
				return err
			}
			_ = svc.Stop() // best-effort; may not be running
			if err := svc.Uninstall(); err != nil {
				return fmt.Errorf("uninstall: %w", err)
			}
			fmt.Printf("Uninstalled service %s.\n", opts.Name)
			return nil
		},
	}
	addLifecycleFlags(cmd, &opts)
	return cmd
}

func newServiceStartCommand() *cobra.Command {
	opts := serviceLifecycleOpts{}
	cmd := &cobra.Command{
		Use:   "start",
		Short: "Start the installed service (equivalent to `systemctl start <name>`)",
		RunE: func(cmd *cobra.Command, args []string) error {
			svc, err := newManagedService(opts)
			if err != nil {
				return err
			}
			return svc.Start()
		},
	}
	addLifecycleFlags(cmd, &opts)
	return cmd
}

func newServiceStopCommand() *cobra.Command {
	opts := serviceLifecycleOpts{}
	cmd := &cobra.Command{
		Use:   "stop",
		Short: "Stop the installed service (equivalent to `systemctl stop <name>`)",
		RunE: func(cmd *cobra.Command, args []string) error {
			svc, err := newManagedService(opts)
			if err != nil {
				return err
			}
			return svc.Stop()
		},
	}
	addLifecycleFlags(cmd, &opts)
	return cmd
}

func newServiceRestartCommand() *cobra.Command {
	opts := serviceLifecycleOpts{}
	cmd := &cobra.Command{
		Use:   "restart",
		Short: "Restart the installed service",
		RunE: func(cmd *cobra.Command, args []string) error {
			svc, err := newManagedService(opts)
			if err != nil {
				return err
			}
			return svc.Restart()
		},
	}
	addLifecycleFlags(cmd, &opts)
	return cmd
}

func newServiceStatusCommand() *cobra.Command {
	opts := serviceLifecycleOpts{}
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show service status (passes through systemctl status output)",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runServiceStatus(opts)
		},
	}
	addLifecycleFlags(cmd, &opts)
	return cmd
}

func runServiceStatus(opts serviceLifecycleOpts) error {
	svc, err := newManagedService(opts)
	if err != nil {
		return err
	}
	status, statusErr := svc.Status()
	label := "unknown"
	switch status {
	case service.StatusRunning:
		label = "running"
	case service.StatusStopped:
		label = "stopped"
	}
	fmt.Printf("Service %s: %s\n", opts.Name, label)
	if errors.Is(statusErr, service.ErrNotInstalled) {
		fmt.Println("(not installed)")
		return nil
	}

	// Pass through full systemctl status for richer info than the
	// library's tri-state enum. Failure here is non-fatal — on macOS /
	// Windows there's no systemctl to call.
	args := []string{"status", opts.Name, "--no-pager"}
	if opts.Mode == "user" || (opts.Mode == "" && os.Geteuid() != 0) {
		args = append([]string{"--user"}, args...)
	}
	c := exec.Command("systemctl", args...)
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	_ = c.Run()
	return nil
}

// helpers --------------------------------------------------------------------

func promptYesNo(question string, defaultYes bool) (bool, error) {
	hint := "[Y/n]"
	if !defaultYes {
		hint = "[y/N]"
	}
	fmt.Printf("%s %s ", question, hint)
	var line string
	_, err := fmt.Scanln(&line)
	if err != nil {
		// Empty line surfaces as "unexpected newline" — treat as default.
		if strings.Contains(err.Error(), "unexpected newline") {
			return defaultYes, nil
		}
		return false, err
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true, nil
	case "n", "no":
		return false, nil
	default:
		return defaultYes, nil
	}
}

func isTTY() bool {
	fi, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

func userFlagFor(mode string) string {
	if mode == "user" {
		return " --user"
	}
	return ""
}
