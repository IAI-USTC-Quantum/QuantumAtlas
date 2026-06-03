package main

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/config"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// newTestServeCommand returns a bare `serve` cobra.Command (without
// PocketBase wiring) we can attach registerServeFlags to and parse
// argv against. We don't reach into PocketBase's NewServeCommand
// because we want test isolation — the flags + applyServeFlags logic
// is independent of PocketBase's flag set.
func newTestServeCommand() *cobra.Command {
	return &cobra.Command{
		Use:          "serve",
		SilenceUsage: true,
		Run:          func(cmd *cobra.Command, args []string) {},
	}
}

// ---------------------------------------------------------------------------
// serveFlagSpecs / registerServeFlags — schema-level invariants
// ---------------------------------------------------------------------------

func TestServeFlagSpecs_AllFieldsPopulated(t *testing.T) {
	f := &qatlasdServeFlags{}
	specs := serveFlagSpecs(f)
	if len(specs) == 0 {
		t.Fatal("serveFlagSpecs is empty; nothing to expose to the operator")
	}
	for _, s := range specs {
		if s.name == "" {
			t.Errorf("spec %+v: empty flag name", s)
		}
		if s.env == "" {
			t.Errorf("spec %q: empty env tag (every flag must document its env counterpart)", s.name)
		}
		if s.help == "" {
			t.Errorf("spec %q: empty help (every flag must have a one-liner)", s.name)
		}
		if s.bindPtr == nil {
			t.Errorf("spec %q: nil bindPtr (no destination to fill)", s.name)
		}
		switch s.kind {
		case "string", "bool", "stringslice":
			// known kind
		default:
			t.Errorf("spec %q: unknown kind %q", s.name, s.kind)
		}
	}
}

func TestServeFlagSpecs_FlagNamesAreUnique(t *testing.T) {
	seen := map[string]bool{}
	for _, s := range serveFlagSpecs(&qatlasdServeFlags{}) {
		if seen[s.name] {
			t.Errorf("duplicate flag name %q (cobra would panic at runtime)", s.name)
		}
		seen[s.name] = true
	}
}

func TestServeFlagSpecs_EnvNamesAreUnique(t *testing.T) {
	// Two different flags pointing at the same env var would make
	// the env→flag mapping ambiguous and break easytier-style
	// expectations.
	seen := map[string]bool{}
	for _, s := range serveFlagSpecs(&qatlasdServeFlags{}) {
		if seen[s.env] {
			t.Errorf("duplicate env tag %q on flag %q (every env name must map to exactly one flag)", s.env, s.name)
		}
		seen[s.env] = true
	}
}

func TestServeFlagSpecs_OmitsOAuthFields(t *testing.T) {
	// Deliberate omission — see package doc "Known limitation —
	// OAuth credentials are env-only". If someone adds these back
	// without addressing the Bootstrap timing issue, this test
	// catches it.
	for _, s := range serveFlagSpecs(&qatlasdServeFlags{}) {
		switch s.env {
		case "GITHUB_CLIENT_ID", "GITHUB_CLIENT_SECRET",
			"QATLAS_ALLOWED_GITHUB_LOGINS", "QATLAS_ADMIN_GITHUB_LOGINS":
			t.Errorf("flag %q exposes %q — but PocketBase Bootstrap runs before cobra parse, so this would silently no-op. Either fix the timing or keep this flag out.",
				s.name, s.env)
		}
	}
}

func TestRegisterServeFlags_AppendsEnvTagToHelp(t *testing.T) {
	// easytier-style "[env: ET_FOO=]" — operators reading `serve
	// --help` must see the env-var alias for every flag inline.
	cmd := newTestServeCommand()
	_ = registerServeFlags(cmd)

	buf := &bytes.Buffer{}
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	if err := cmd.Help(); err != nil {
		t.Fatalf("cmd.Help: %v", err)
	}
	out := buf.String()
	// Spot-check a representative sample of each section.
	for _, mustContain := range []string{
		"[env: QATLAS_SERVER_URL=]",
		"[env: NEO4J_URI=]",
		"[env: QATLAS_S3_ENDPOINT=]",
		"[env: QATLAS_SYSTEM_PAT=]",
		"[env: QATLAS_EDGE_NAME=]",
	} {
		if !strings.Contains(out, mustContain) {
			t.Errorf("`serve --help` output missing %q; got:\n%s", mustContain, out)
		}
	}
}

func TestRegisterServeFlags_DefaultsAreEmptyForChangedDetection(t *testing.T) {
	// applyServeFlags uses cobra's Flag.Changed bit to decide whether
	// to clobber cfg with the flag value. That decision relies on
	// every flag being registered with an EMPTY default (so we can
	// distinguish "user didn't pass --foo" from "user passed
	// --foo=''"). Non-empty defaults would silently override env
	// values on every boot — exactly the regression this test
	// guards against.
	cmd := newTestServeCommand()
	_ = registerServeFlags(cmd)
	cmd.PersistentFlags().VisitAll(func(flag *pflag.Flag) {
		switch flag.Value.Type() {
		case "string":
			if flag.DefValue != "" {
				t.Errorf("flag %q: non-empty default %q breaks Flag.Changed-based override detection",
					flag.Name, flag.DefValue)
			}
		case "bool":
			if flag.DefValue != "false" {
				t.Errorf("flag %q: bool default %q should be false", flag.Name, flag.DefValue)
			}
		case "stringSlice":
			if flag.DefValue != "[]" {
				t.Errorf("flag %q: stringSlice default %q should be empty", flag.Name, flag.DefValue)
			}
		}
	})
}

// ---------------------------------------------------------------------------
// applyServeFlags — precedence behaviour
// ---------------------------------------------------------------------------

func TestApplyServeFlags_NoCLIPreservesCfg(t *testing.T) {
	// When the operator doesn't pass any --foo, applyServeFlags must
	// leave cfg untouched (so the env / .env values config.Load
	// already filled keep working).
	cmd := newTestServeCommand()
	flags := registerServeFlags(cmd)
	if err := cmd.ParseFlags([]string{}); err != nil {
		t.Fatalf("ParseFlags: %v", err)
	}

	cfg := &config.Config{
		Neo4jURI:    "bolt://from-env:7687",
		S3Endpoint:  "https://from-env.example",
		WikiDir:     "/from-env/wiki",
	}
	applyServeFlags(cmd, flags, cfg)

	if cfg.Neo4jURI != "bolt://from-env:7687" {
		t.Errorf("Neo4jURI clobbered without --neo4j-uri: %q", cfg.Neo4jURI)
	}
	if cfg.S3Endpoint != "https://from-env.example" {
		t.Errorf("S3Endpoint clobbered without --s3-endpoint: %q", cfg.S3Endpoint)
	}
	if cfg.WikiDir != "/from-env/wiki" {
		t.Errorf("WikiDir clobbered without --wiki-dir: %q", cfg.WikiDir)
	}
}

func TestApplyServeFlags_CLIWinsOverCfg(t *testing.T) {
	cmd := newTestServeCommand()
	flags := registerServeFlags(cmd)
	if err := cmd.ParseFlags([]string{
		"--neo4j-uri", "bolt://from-cli:7687",
		"--s3-endpoint", "https://from-cli.example",
		"--wiki-dir", "/from-cli/wiki",
	}); err != nil {
		t.Fatalf("ParseFlags: %v", err)
	}

	cfg := &config.Config{
		Neo4jURI:    "bolt://from-env:7687",
		S3Endpoint:  "https://from-env.example",
		WikiDir:     "/from-env/wiki",
	}
	applyServeFlags(cmd, flags, cfg)

	if cfg.Neo4jURI != "bolt://from-cli:7687" {
		t.Errorf("CLI flag should beat env: got %q", cfg.Neo4jURI)
	}
	if cfg.S3Endpoint != "https://from-cli.example" {
		t.Errorf("CLI flag should beat env: got %q", cfg.S3Endpoint)
	}
	if cfg.WikiDir != "/from-cli/wiki" {
		t.Errorf("CLI flag should beat env: got %q", cfg.WikiDir)
	}
}

func TestApplyServeFlags_StringSliceParsesCSV(t *testing.T) {
	cmd := newTestServeCommand()
	flags := registerServeFlags(cmd)
	if err := cmd.ParseFlags([]string{
		"--system-pat-scopes", "wiki:read,papers:write,graph:read",
	}); err != nil {
		t.Fatalf("ParseFlags: %v", err)
	}

	// System PAT scopes get mirrored into os.Environ (not stored on
	// cfg). Snapshot the env, run apply, check it shows up.
	t.Setenv("QATLAS_SYSTEM_PAT_SCOPES", "")
	applyServeFlags(cmd, flags, &config.Config{})

	got := os.Getenv("QATLAS_SYSTEM_PAT_SCOPES")
	if got != "wiki:read,papers:write,graph:read" {
		t.Errorf("QATLAS_SYSTEM_PAT_SCOPES = %q, want comma-joined CSV", got)
	}
}

func TestApplyServeFlags_ExplicitEmptyClobbersCfg(t *testing.T) {
	// Passing --neo4j-uri="" must clear cfg.Neo4jURI, because the
	// operator explicitly asked for empty. This is the
	// distinguishing case our "use Flag.Changed, not zero-value
	// check" design exists to support.
	cmd := newTestServeCommand()
	flags := registerServeFlags(cmd)
	if err := cmd.ParseFlags([]string{"--neo4j-uri", ""}); err != nil {
		t.Fatalf("ParseFlags: %v", err)
	}

	cfg := &config.Config{Neo4jURI: "bolt://from-env:7687"}
	applyServeFlags(cmd, flags, cfg)

	if cfg.Neo4jURI != "" {
		t.Errorf("explicit --neo4j-uri='' should clear cfg.Neo4jURI; got %q", cfg.Neo4jURI)
	}
}

func TestApplyServeFlags_NilArgsAreNoOp(t *testing.T) {
	// Defensive: nil cfg / nil flags / nil cmd shouldn't panic.
	applyServeFlags(nil, nil, nil)
	applyServeFlags(newTestServeCommand(), nil, nil)
	applyServeFlags(newTestServeCommand(), &qatlasdServeFlags{}, nil)
}

func TestApplyServeFlags_SystemPATMirrorsToEnv(t *testing.T) {
	// SystemPAT isn't on cfg (LoadSystemPAT reads env directly), so
	// applyServeFlags must os.Setenv the value back.
	cmd := newTestServeCommand()
	flags := registerServeFlags(cmd)
	if err := cmd.ParseFlags([]string{
		"--system-pat", "super-secret-token-min-16-chars",
	}); err != nil {
		t.Fatalf("ParseFlags: %v", err)
	}

	t.Setenv("QATLAS_SYSTEM_PAT", "")
	applyServeFlags(cmd, flags, &config.Config{})

	if got := os.Getenv("QATLAS_SYSTEM_PAT"); got != "super-secret-token-min-16-chars" {
		t.Errorf("QATLAS_SYSTEM_PAT = %q, want CLI value reflected", got)
	}
}
