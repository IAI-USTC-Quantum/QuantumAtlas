package main

import (
	_ "embed"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"
)

// defaultEnvTemplate is the minimal commented .env template emitted by
// `qatlasd config init`. Lives in templates/default.env so editing it
// is a normal text-file diff, not a Go string literal.
//
//go:embed templates/default.env
var defaultEnvTemplate []byte

// envPrefixes lists the prefixes whose values `qatlasd config show`
// considers part of "QuantumAtlas configuration" and prints. Other
// process env vars (PATH, HOME, ...) are filtered out so the output
// stays focused.
var envPrefixes = []string{
	"QATLAS_",
	"NEO4J_",
	"MINERU_",
	"OPENAI_",
	"ANTHROPIC_",
	"GITHUB_CLIENT_",
}

// secretSubstrings are case-insensitive needles found in env-var names
// whose VALUES should be redacted in `qatlasd config show` output. The
// match is on the var NAME, not the value, so we don't leak by
// accidental substring match against a long URL.
var secretSubstrings = []string{
	"TOKEN",
	"SECRET",
	"KEY",
	"PASSWORD",
}

// NewConfigCommand returns the `qatlasd config` subcommand group:
// init / show / path. The design mirrors common CLI conventions
// (kubectl config, gh config, code-server config) so operators don't
// have to learn yet another vocabulary.
func NewConfigCommand() *cobra.Command {
	root := &cobra.Command{
		Use:   "config",
		Short: "Manage the qatlasd .env configuration file",
		Long: `Manage qatlasd's .env configuration file.

qatlasd reads its configuration from process environment variables,
which are typically populated from a .env file at startup (godotenv
non-override semantics — existing env always wins over the file).

These subcommands help you bootstrap, inspect, and locate that file
without manually copying .env.example or grepping the running unit.`,
	}
	root.AddCommand(newConfigInitCommand())
	root.AddCommand(newConfigPathCommand())
	root.AddCommand(newConfigShowCommand())
	return root
}

// ---------------------------------------------------------------------------
// `qatlasd config init`
// ---------------------------------------------------------------------------

type configInitOpts struct {
	path  string
	force bool
}

func newConfigInitCommand() *cobra.Command {
	opts := &configInitOpts{}
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Write a minimal default .env to disk",
		Long: `Write a minimal commented .env template to disk.

Default target path is $XDG_CONFIG_HOME/qatlasd/.env (or ~/.config/qatlasd/.env).
The file is created with mode 0600 so secrets don't leak via group/other read.

The template is intentionally small — only the most commonly touched fields
appear. The exhaustive reference lives in .env.example in the repository.`,
		Example: `  # Write to the XDG default
  qatlasd config init

  # Write somewhere specific (e.g. /etc/quantum-atlas/.env)
  sudo qatlasd config init --path /etc/quantum-atlas/.env

  # Overwrite an existing file
  qatlasd config init --force`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runConfigInit(cmd, opts)
		},
	}
	cmd.Flags().StringVar(&opts.path, "path", "", "Target path (default: $XDG_CONFIG_HOME/qatlasd/.env)")
	cmd.Flags().BoolVar(&opts.force, "force", false, "Overwrite an existing file (default: refuse with non-zero exit)")
	return cmd
}

func runConfigInit(cmd *cobra.Command, opts *configInitOpts) error {
	path := opts.path
	if path == "" {
		def, err := defaultConfigPath()
		if err != nil {
			return fmt.Errorf("resolve default config path: %w", err)
		}
		path = def
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("resolve absolute path for %q: %w", path, err)
	}

	if _, err := os.Stat(abs); err == nil {
		if !opts.force {
			return fmt.Errorf("%s already exists; pass --force to overwrite, or run `qatlasd config show` to inspect it", abs)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("stat %s: %w", abs, err)
	}

	if err := os.MkdirAll(filepath.Dir(abs), 0o700); err != nil {
		return fmt.Errorf("mkdir %s: %w", filepath.Dir(abs), err)
	}
	if err := os.WriteFile(abs, defaultEnvTemplate, 0o600); err != nil {
		return fmt.Errorf("write %s: %w", abs, err)
	}

	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "Wrote default config to %s (mode 0600).\n", abs)
	fmt.Fprintln(out, "Edit it, then start the server with one of:")
	fmt.Fprintf(out, "  QATLAS_DOTENV=%s qatlasd serve --http=127.0.0.1:4200\n", abs)
	fmt.Fprintf(out, "  qatlasd service install --dotenv-path %s\n", abs)
	return nil
}

// defaultConfigPath returns $XDG_CONFIG_HOME/qatlasd/.env (or
// $HOME/.config/qatlasd/.env when XDG_CONFIG_HOME is unset). This
// matches the convention used by code-server, gh, and other modern
// CLIs that ship a default config file.
func defaultConfigPath() (string, error) {
	base := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME"))
	if base == "" || !filepath.IsAbs(base) {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("user home dir: %w", err)
		}
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "qatlasd", ".env"), nil
}

// ---------------------------------------------------------------------------
// `qatlasd config path`
// ---------------------------------------------------------------------------

func newConfigPathCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "path",
		Short: "Print the .env path qatlasd would load",
		Long: `Print the .env file path qatlasd would load if started now.

Lookup order (first hit wins):
  1. $QATLAS_DOTENV — explicit override (systemd / docker convention)
  2. ./.env — relative to the current working directory

Exits non-zero with no output when no file would be loaded.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			path := currentDotenvPath()
			if path == "" {
				return errors.New("no .env located; server would rely on process environment alone")
			}
			fmt.Fprintln(cmd.OutOrStdout(), path)
			return nil
		},
	}
}

// currentDotenvPath returns the absolute path qatlasd would load, or
// "" when no candidate exists. Mirrors the resolution order in
// loadDotEnv() but without side effects (no actual godotenv.Load,
// no slog output). Named to avoid collision with
// service_cmd.go's resolveDotenvPath, which is the interactive
// flavor used by `service install`.
func currentDotenvPath() string {
	if explicit := strings.TrimSpace(os.Getenv("QATLAS_DOTENV")); explicit != "" {
		if info, err := os.Stat(explicit); err == nil && !info.IsDir() {
			if abs, err := filepath.Abs(explicit); err == nil {
				return abs
			}
			return explicit
		}
	}
	if cwd, err := os.Getwd(); err == nil {
		candidate := filepath.Join(cwd, ".env")
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate
		}
	}
	return ""
}

// ---------------------------------------------------------------------------
// `qatlasd config show`
// ---------------------------------------------------------------------------

type configShowOpts struct {
	noRedact bool
}

func newConfigShowCommand() *cobra.Command {
	opts := &configShowOpts{}
	cmd := &cobra.Command{
		Use:   "show",
		Short: "Print the QuantumAtlas-relevant env vars currently visible to the process",
		Long: `Print all QuantumAtlas-relevant environment variables currently set in
the process, in KEY=VALUE form, sorted alphabetically.

Reflects what qatlasd would see *right now* — does NOT pre-load any
.env file. To inspect a specific .env, run with the file pre-sourced:
  set -a; . /path/to/.env; set +a; qatlasd config show

Secret values (vars whose name contains TOKEN / SECRET / KEY / PASSWORD)
are redacted to '***' by default. Use --no-redact to see plaintext
(handy for debug but ONLY in private terminals).`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runConfigShow(cmd, opts)
		},
	}
	cmd.Flags().BoolVar(&opts.noRedact, "no-redact", false, "Print secret values as plaintext (default: redact to '***')")
	return cmd
}

func runConfigShow(cmd *cobra.Command, opts *configShowOpts) error {
	names := collectQATLASEnvNames()
	sort.Strings(names)

	out := cmd.OutOrStdout()
	for _, name := range names {
		val := os.Getenv(name)
		if !opts.noRedact && isSecretName(name) {
			val = "***"
		}
		fmt.Fprintf(out, "%s=%s\n", name, val)
	}
	if len(names) == 0 {
		fmt.Fprintln(cmd.ErrOrStderr(),
			"(no QuantumAtlas-relevant env vars set; check that .env was loaded — see `qatlasd config path`)")
	}
	return nil
}

// collectQATLASEnvNames returns every env var name whose prefix matches
// any of envPrefixes. Empty-value vars are skipped so an exported-but-
// empty alias doesn't add noise.
func collectQATLASEnvNames() []string {
	seen := map[string]struct{}{}
	for _, raw := range os.Environ() {
		eq := strings.IndexByte(raw, '=')
		if eq <= 0 {
			continue
		}
		name := raw[:eq]
		val := raw[eq+1:]
		if val == "" {
			continue
		}
		for _, prefix := range envPrefixes {
			if strings.HasPrefix(name, prefix) {
				seen[name] = struct{}{}
				break
			}
		}
	}
	out := make([]string, 0, len(seen))
	for name := range seen {
		out = append(out, name)
	}
	return out
}

// isSecretName reports whether the env-var name should be redacted.
// Substring match (case-insensitive on the upper-cased name, which is
// the convention for env vars) so SECRET_ACCESS_KEY matches both
// "SECRET" and "KEY" — either is sufficient.
func isSecretName(name string) bool {
	upper := strings.ToUpper(name)
	for _, needle := range secretSubstrings {
		if strings.Contains(upper, needle) {
			return true
		}
	}
	return false
}
