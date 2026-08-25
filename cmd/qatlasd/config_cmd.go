package main

import (
	_ "embed"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/config"

	"github.com/spf13/cobra"
)

// defaultConfigTemplate is the commented config.yaml template emitted by
// `qatlasd config init`. Lives in templates/config.yaml so editing it is
// a normal text-file diff, not a Go string literal.
//
//go:embed templates/config.yaml
var defaultConfigTemplate []byte

// NewConfigCommand returns the `qatlasd config` subcommand group:
// init / show / path. The design mirrors common CLI conventions
// (kubectl config, gh config, code-server config) so operators don't
// have to learn yet another vocabulary.
func NewConfigCommand() *cobra.Command {
	opts := &configPathOpts{}
	root := &cobra.Command{
		Use:   "config",
		Short: "Manage the qatlasd YAML configuration file",
		Long: `Manage qatlasd's YAML configuration file.

qatlasd reads ALL of its configuration from a single YAML file
(default: ~/.qatlas/config.yaml, override with --config). Environment
variables are NOT consulted — any QATLAS_* variable set in the process
environment makes startup fail with an error naming the offender.

These subcommands help you bootstrap, inspect, and locate that file.`,
	}
	root.PersistentFlags().StringVar(&opts.path, "config", "",
		"Path to config.yaml (default: ~/.qatlas/config.yaml)")
	root.AddCommand(newConfigInitCommand(opts))
	root.AddCommand(newConfigPathCommand(opts))
	root.AddCommand(newConfigShowCommand(opts))
	return root
}

// configPathOpts carries the --config flag value shared by all config
// subcommands.
type configPathOpts struct {
	path string
}

// resolved returns the effective config file path: the explicit
// --config value when given, else the default ~/.qatlas/config.yaml.
func (o *configPathOpts) resolved() (string, error) {
	if strings.TrimSpace(o.path) != "" {
		abs, err := filepath.Abs(o.path)
		if err != nil {
			return "", fmt.Errorf("resolve absolute path for %q: %w", o.path, err)
		}
		return abs, nil
	}
	def := config.DefaultPath()
	if def == "" {
		return "", errors.New("cannot determine user home directory; pass --config /path/to/config.yaml")
	}
	return def, nil
}

// ---------------------------------------------------------------------------
// `qatlasd config init`
// ---------------------------------------------------------------------------

type configInitOpts struct {
	force bool
}

func newConfigInitCommand(shared *configPathOpts) *cobra.Command {
	opts := &configInitOpts{}
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Write a commented default config.yaml to disk",
		Long: `Write a commented default config.yaml template to disk.

Default target path is ~/.qatlas/config.yaml (override with --config).
The file is created with mode 0600 so secrets don't leak via group/other read.

The template lists every supported key, commented out — uncomment + fill
the ones you need.`,
		Example: `  # Write to the default location
  qatlasd config init

  # Write somewhere specific (e.g. /etc/quantum-atlas/config.yaml)
  sudo qatlasd config init --config /etc/quantum-atlas/config.yaml

  # Overwrite an existing file
  qatlasd config init --force`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runConfigInit(cmd, shared, opts)
		},
	}
	cmd.Flags().BoolVar(&opts.force, "force", false, "Overwrite an existing file (default: refuse with non-zero exit)")
	return cmd
}

func runConfigInit(cmd *cobra.Command, shared *configPathOpts, opts *configInitOpts) error {
	path, err := shared.resolved()
	if err != nil {
		return err
	}

	if _, err := os.Stat(path); err == nil {
		if !opts.force {
			return fmt.Errorf("%s already exists; pass --force to overwrite, or run `qatlasd config show` to inspect it", path)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("stat %s: %w", path, err)
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("mkdir %s: %w", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, defaultConfigTemplate, 0o600); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}

	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "Wrote default config to %s (mode 0600).\n", path)
	fmt.Fprintln(out, "Edit it, then start the server with:")
	fmt.Fprintln(out, "  qatlasd serve")
	return nil
}

// ---------------------------------------------------------------------------
// `qatlasd config path`
// ---------------------------------------------------------------------------

func newConfigPathCommand(shared *configPathOpts) *cobra.Command {
	return &cobra.Command{
		Use:   "path",
		Short: "Print the config.yaml path qatlasd would load",
		Long: `Print the config file path qatlasd would load if started now:
the --config value when given, else ~/.qatlas/config.yaml.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			path, err := shared.resolved()
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), path)
			return nil
		},
	}
}

// ---------------------------------------------------------------------------
// `qatlasd config show`
// ---------------------------------------------------------------------------

type configShowOpts struct {
	noRedact bool
}

func newConfigShowCommand(shared *configPathOpts) *cobra.Command {
	opts := &configShowOpts{}
	cmd := &cobra.Command{
		Use:   "show",
		Short: "Print the effective configuration as YAML (secrets masked)",
		Long: `Load the config file exactly as qatlasd would and print the
effective configuration (after defaults and path resolution) as YAML.

Secret values (tokens, keys, secrets) are masked to '***' by default.
Use --no-redact to see plaintext (handy for debug but ONLY in private
terminals).`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runConfigShow(cmd, shared, opts)
		},
	}
	cmd.Flags().BoolVar(&opts.noRedact, "no-redact", false, "Print secret values as plaintext (default: mask to '***')")
	return cmd
}

func runConfigShow(cmd *cobra.Command, shared *configPathOpts, opts *configShowOpts) error {
	path, err := shared.resolved()
	if err != nil {
		return err
	}
	cfg, err := config.Load(path)
	if err != nil {
		return err
	}
	out, err := effectiveConfigYAML(cfg, !opts.noRedact)
	if err != nil {
		return err
	}
	_, err = cmd.OutOrStdout().Write(out)
	return err
}

// effectiveConfigYAML renders the resolved Config back into the YAML
// schema operators edit, so `config show` output is directly diffable
// against config.yaml. When redact is true, secret leaf values are
// replaced with "***".
func effectiveConfigYAML(cfg *config.Config, redact bool) ([]byte, error) {
	mask := func(v string) string {
		if redact && v != "" {
			return "***"
		}
		return v
	}
	maskList := func(vs []string) []string {
		if !redact || len(vs) == 0 {
			return vs
		}
		out := make([]string, len(vs))
		for i := range vs {
			out[i] = "***"
		}
		return out
	}

	doc := map[string]any{
		"http_addr":         cfg.HTTPAddr,
		"public_url":        cfg.PublicURL,
		"user_header":       cfg.UserHeader,
		"edge_name":         cfg.EdgeName,
		"force_tcp4":        cfg.ForceTCP4,
		"skip_pb_data_lock": cfg.SkipPBDataLock,
		"paths": map[string]any{
			"raw_dir":     cfg.RawDir,
			"data_dir":    cfg.DataDir,
			"pb_data_dir": cfg.PBDataDir,
		},
		"postgres": map[string]any{
			"dsn":                   cfg.PostgresDSN,
			"max_conns":             cfg.PostgresMaxConns,
			"corpus_ensure_indexes": cfg.CorpusEnsureIndexes,
		},
		"search": map[string]any{
			"providers": cfg.SearchProviders,
			"remote": map[string]any{
				"enabled": cfg.RemoteEnabled,
				"url":     cfg.RemoteURL,
				"token":   mask(cfg.RemoteToken),
				"timeout": cfg.RemoteTimeout.String(),
			},
			"agentic": map[string]any{
				"daily_limit":    cfg.AgenticDailyLimit,
				"price_per_mtok": cfg.AgenticPricePerMtok,
			},
		},
		"auth": map[string]any{
			"github_client_id":     cfg.GitHubClientID,
			"github_client_secret": mask(cfg.GitHubClientSecret),
			"allowed_logins":       cfg.AllowedGitHubLogins,
			"admin_logins":         cfg.AdminGitHubLogins,
		},
		"s3": map[string]any{
			"endpoint":          cfg.S3Endpoint,
			"public_endpoint":   cfg.S3PublicEndpoint,
			"bucket_pdf":        cfg.S3BucketPDF,
			"bucket_md":         cfg.S3BucketMD,
			"bucket_images":     cfg.S3BucketImages,
			"bucket_openalex":   cfg.S3BucketOpenAlex,
			"access_key_id":     mask(cfg.S3AccessKeyID),
			"secret_access_key": mask(cfg.S3SecretAccessKey),
		},
		"paper_access": map[string]any{
			"enabled":                cfg.PaperAccessEnabled,
			"openalex_mailto":        cfg.OpenAlexMailto,
			"arxiv_fetch_concurrent": cfg.ArxivFetchConcurrent,
			"arxiv_fetch_rps":        cfg.ArxivFetchRPS,
			"mineru": map[string]any{
				"api_tokens":          maskList(cfg.MinerUAPITokens),
				"api_base_url":        cfg.MinerUAPIBaseURL,
				"model_version":       cfg.MinerUModelVersion,
				"language":            cfg.MinerULanguage,
				"is_ocr":              cfg.MinerUIsOCR,
				"enable_formula":      cfg.MinerUEnableFormula,
				"enable_table":        cfg.MinerUEnableTable,
				"poll_interval":       cfg.MinerUPollInterval.String(),
				"timeout":             cfg.MinerUTimeout.String(),
				"max_concurrent_jobs": cfg.MinerUMaxConcurrentJobs,
			},
		},
		"rag": map[string]any{
			"qdrant_url":        cfg.RAGQdrantURL,
			"qdrant_api_key":    mask(cfg.RAGQdrantAPIKey),
			"qdrant_collection": cfg.RAGQdrantCollection,
			"embed_url":         cfg.RAGEmbedURL,
			"embed_token":       mask(cfg.RAGEmbedToken),
		},
		"plugins": map[string]any{
			"dir":                cfg.PluginsDir,
			"enabled":            cfg.PluginsEnabled,
			"disabled":           cfg.PluginsDisabled,
			"connect_secret":     mask(cfg.PluginConnectSecret),
			"rpc_ws_bind":        cfg.RPCWSBind,
			"event_retention":    cfg.EventRetention.String(),
			"rpc_timeout":        cfg.PluginRPCTimeout.String(),
			"reconnect_interval": cfg.PluginReconnectInterval.String(),
			"deadletter_dir":     cfg.DeadLetterDir,
		},
		"system_pat": map[string]any{
			"token":  mask(cfg.SystemPATToken),
			"scopes": cfg.SystemPATScopes,
		},
	}
	return yaml.Marshal(doc)
}
