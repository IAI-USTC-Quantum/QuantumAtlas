package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// `qatlasd config init`
// ---------------------------------------------------------------------------

func TestConfigInit_WritesDefaultTemplate(t *testing.T) {
	tmp := t.TempDir()
	target := filepath.Join(tmp, "qatlasd", "config.yaml")

	cmd := NewConfigCommand()
	buf := &bytes.Buffer{}
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"--config", target, "init"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("config init: %v (out=%s)", err, buf.String())
	}

	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read target: %v", err)
	}
	if !bytes.Equal(data, defaultConfigTemplate) {
		t.Errorf("written file does not match embedded template")
	}

	info, err := os.Stat(target)
	if err != nil {
		t.Fatalf("stat target: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("file perm = %o, want 0o600 (secrets must not be group/other readable)",
			info.Mode().Perm())
	}

	out := buf.String()
	if !strings.Contains(out, target) {
		t.Errorf("expected success message to reference target path %q; got:\n%s", target, out)
	}
}

func TestConfigInit_RefuseOverwriteWithoutForce(t *testing.T) {
	tmp := t.TempDir()
	target := filepath.Join(tmp, "qatlasd", "config.yaml")

	// Bootstrap an existing file with a recognisable sentinel content
	// so we can assert it was NOT clobbered.
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	sentinel := []byte("# sentinel - must not be overwritten\n")
	if err := os.WriteFile(target, sentinel, 0o600); err != nil {
		t.Fatalf("seed file: %v", err)
	}

	cmd := NewConfigCommand()
	buf := &bytes.Buffer{}
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SilenceUsage = true
	cmd.SetArgs([]string{"--config", target, "init"})
	err := cmd.Execute()
	if err == nil {
		t.Fatalf("expected error when target exists and --force missing; got success (out=%s)", buf.String())
	}
	if !strings.Contains(err.Error(), "already exists") {
		t.Errorf("error %q should mention `already exists`", err.Error())
	}

	got, _ := os.ReadFile(target)
	if !bytes.Equal(got, sentinel) {
		t.Errorf("file was overwritten without --force; got %q", string(got))
	}
}

func TestConfigInit_ForceOverwrites(t *testing.T) {
	tmp := t.TempDir()
	target := filepath.Join(tmp, "qatlasd", "config.yaml")

	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(target, []byte("# old\n"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}

	cmd := NewConfigCommand()
	buf := &bytes.Buffer{}
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"--config", target, "init", "--force"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("config init --force: %v (out=%s)", err, buf.String())
	}

	got, _ := os.ReadFile(target)
	if !bytes.Equal(got, defaultConfigTemplate) {
		t.Errorf("--force should replace file with default template; got %d bytes", len(got))
	}
}

func TestConfigInit_DefaultPathIsHomeDotQatlas(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	cmd := NewConfigCommand()
	buf := &bytes.Buffer{}
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"init"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("config init: %v (out=%s)", err, buf.String())
	}

	want := filepath.Join(tmp, ".qatlas", "config.yaml")
	if _, err := os.Stat(want); err != nil {
		t.Errorf("expected config at default path %s: %v", want, err)
	}
}

// ---------------------------------------------------------------------------
// `qatlasd config path`
// ---------------------------------------------------------------------------

func TestConfigPath_ReportsConfigFlag(t *testing.T) {
	tmp := t.TempDir()
	target := filepath.Join(tmp, "custom.yaml")

	cmd := NewConfigCommand()
	buf := &bytes.Buffer{}
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"--config", target, "path"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("config path: %v", err)
	}
	got := strings.TrimSpace(buf.String())
	if got != target {
		t.Errorf("config path = %q, want %q", got, target)
	}
}

func TestConfigPath_DefaultsToHomeDotQatlas(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	cmd := NewConfigCommand()
	buf := &bytes.Buffer{}
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"path"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("config path: %v", err)
	}
	got := strings.TrimSpace(buf.String())
	want := filepath.Join(tmp, ".qatlas", "config.yaml")
	if got != want {
		t.Errorf("config path = %q, want %q", got, want)
	}
}

// ---------------------------------------------------------------------------
// `qatlasd config show`
// ---------------------------------------------------------------------------

// clearConfigEnv unsets every env var that would trip the config
// loader's env-rejection scan, so show tests don't depend on the
// developer's shell.
func clearConfigEnv(t *testing.T) {
	t.Helper()
	for _, raw := range os.Environ() {
		eq := strings.IndexByte(raw, '=')
		if eq <= 0 {
			continue
		}
		name := raw[:eq]
		if strings.HasPrefix(name, "QATLAS_") || strings.HasPrefix(name, "MINERU_") ||
			strings.HasPrefix(name, "NEO4J_") || strings.HasPrefix(name, "POSTGRES_") ||
			name == "GITHUB_CLIENT_ID" || name == "GITHUB_CLIENT_SECRET" {
			t.Setenv(name, "")
		}
	}
}

func writeShowConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

func TestConfigShow_PrintsEffectiveYAMLAndMasksSecrets(t *testing.T) {
	clearConfigEnv(t)
	target := writeShowConfig(t, `
public_url: https://test.example.com
postgres:
  dsn: postgres://qatlas:pw@pg:5432/qatlas
auth:
  github_client_id: gh-client-a
  github_client_secret: ghs_example
  gitea_url: https://git.example.com
  gitea_client_id: gitea-client-a
  gitea_client_secret: gto_example
s3:
  endpoint: http://s3.internal:9000
  bucket_pdf: qatlas-pdf
  bucket_md: qatlas-md
  bucket_images: qatlas-images
  access_key_id: AKIA-EXAMPLE-NEVER-REAL
  secret_access_key: SECRET-EXAMPLE-NEVER-REAL
system_pat:
  token: supersecret-system-pat
`)

	cmd := NewConfigCommand()
	buf := &bytes.Buffer{}
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"--config", target, "show"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("config show: %v", err)
	}

	got := buf.String()
	// Plaintext fields stay visible.
	for _, want := range []string{
		"public_url: https://test.example.com",
		"github_client_id: gh-client-a",
		"gitea_url: https://git.example.com",
		"gitea_client_id: gitea-client-a",
		"endpoint: http://s3.internal:9000",
		"postgres:", // DSN is not a masked class (matches old config show behaviour)
	} {
		if !strings.Contains(got, want) {
			t.Errorf("expected show output to contain %q; got:\n%s", want, got)
		}
	}
	// Secret values must be masked and never leak.
	for _, plain := range []string{"AKIA-EXAMPLE-NEVER-REAL", "SECRET-EXAMPLE-NEVER-REAL", "ghs_example", "gto_example", "supersecret-system-pat"} {
		if strings.Contains(got, plain) {
			t.Errorf("secret %q leaked into show output:\n%s", plain, got)
		}
	}
	if !strings.Contains(got, "***") {
		t.Errorf("expected masked *** entries in show output; got:\n%s", got)
	}
}

func TestConfigShow_NoRedactShowsPlaintext(t *testing.T) {
	clearConfigEnv(t)
	target := writeShowConfig(t, `
auth:
  github_client_secret: ghs_example
`)

	cmd := NewConfigCommand()
	buf := &bytes.Buffer{}
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"--config", target, "show", "--no-redact"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("config show --no-redact: %v", err)
	}
	if !strings.Contains(buf.String(), "ghs_example") {
		t.Errorf("--no-redact should reveal plaintext; got:\n%s", buf.String())
	}
}

func TestConfigShow_ErrorsWhenFileMissing(t *testing.T) {
	clearConfigEnv(t)
	missing := filepath.Join(t.TempDir(), "nope.yaml")

	cmd := NewConfigCommand()
	buf := &bytes.Buffer{}
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SilenceUsage = true
	cmd.SetArgs([]string{"--config", missing, "show"})
	err := cmd.Execute()
	if err == nil {
		t.Fatalf("expected error for missing config file; got success out=%s", buf.String())
	}
	if !strings.Contains(err.Error(), "qatlasd config init") {
		t.Errorf("error should hint at `qatlasd config init`; got: %v", err)
	}
}

func TestConfigShow_FailsOnEnvVars(t *testing.T) {
	// Env-based configuration is rejected everywhere — including the
	// diagnostic surface, so an operator with a stale export sees the
	// migration error instead of a config that looks fine.
	clearConfigEnv(t)
	target := writeShowConfig(t, "{}\n")
	t.Setenv("QATLAS_POSTGRES_DSN", "postgres://x/y")

	cmd := NewConfigCommand()
	buf := &bytes.Buffer{}
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SilenceUsage = true
	cmd.SetArgs([]string{"--config", target, "show"})
	err := cmd.Execute()
	if err == nil {
		t.Fatalf("expected env-rejection error; got success out=%s", buf.String())
	}
	if !strings.Contains(err.Error(), "QATLAS_POSTGRES_DSN") {
		t.Errorf("error should name the offending env var; got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// --config extraction from raw argv (used by main before cobra parses)
// ---------------------------------------------------------------------------

func TestConfigPathFromArgs(t *testing.T) {
	cases := []struct {
		args []string
		want string
	}{
		{[]string{"qatlasd", "serve"}, ""},
		{[]string{"qatlasd", "--config", "/x.yaml", "serve"}, "/x.yaml"},
		{[]string{"qatlasd", "serve", "--config", "/x.yaml"}, "/x.yaml"},
		{[]string{"qatlasd", "--config=/x.yaml", "serve"}, "/x.yaml"},
		{[]string{"qatlasd", "serve", "--config=/x.yaml"}, "/x.yaml"},
		{[]string{"qatlasd", "--config"}, ""}, // dangling flag — no value
	}
	for _, c := range cases {
		if got := configPathFromArgs(c.args); got != c.want {
			t.Errorf("configPathFromArgs(%v) = %q, want %q", c.args, got, c.want)
		}
	}
}

func TestFirstPositionalIsConfig(t *testing.T) {
	cases := []struct {
		args []string
		want bool
	}{
		{[]string{"config"}, true},
		{[]string{"config", "show"}, true},
		{[]string{"--config", "/x.yaml", "config", "show"}, true},
		{[]string{"--config=/x.yaml", "config"}, true},
		{[]string{"serve"}, false},
		{[]string{"--config", "/x.yaml", "serve"}, false},
		{[]string{}, false},
	}
	for _, c := range cases {
		if got := firstPositionalIsConfig(c.args); got != c.want {
			t.Errorf("firstPositionalIsConfig(%v) = %v, want %v", c.args, got, c.want)
		}
	}
}
