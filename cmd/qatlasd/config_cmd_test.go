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
	target := filepath.Join(tmp, "qatlasd", ".env")

	cmd := NewConfigCommand()
	buf := &bytes.Buffer{}
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"init", "--path", target})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("config init: %v (out=%s)", err, buf.String())
	}

	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read target: %v", err)
	}
	if !bytes.Equal(data, defaultEnvTemplate) {
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
	target := filepath.Join(tmp, "qatlasd", ".env")

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
	cmd.SetArgs([]string{"init", "--path", target})
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
	target := filepath.Join(tmp, "qatlasd", ".env")

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
	cmd.SetArgs([]string{"init", "--path", target, "--force"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("config init --force: %v (out=%s)", err, buf.String())
	}

	got, _ := os.ReadFile(target)
	if !bytes.Equal(got, defaultEnvTemplate) {
		t.Errorf("--force should replace file with default template; got %d bytes", len(got))
	}
}

func TestConfigInit_DefaultPathUsesXDGConfigHome(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)

	got, err := defaultConfigPath()
	if err != nil {
		t.Fatalf("defaultConfigPath: %v", err)
	}
	want := filepath.Join(tmp, "qatlasd", ".env")
	if got != want {
		t.Errorf("defaultConfigPath() = %q, want %q", got, want)
	}
}

func TestConfigInit_DefaultPathFallsBackToHomeWhenXDGUnset(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", tmp)

	got, err := defaultConfigPath()
	if err != nil {
		t.Fatalf("defaultConfigPath: %v", err)
	}
	want := filepath.Join(tmp, ".config", "qatlasd", ".env")
	if got != want {
		t.Errorf("defaultConfigPath() = %q, want %q", got, want)
	}
}

// ---------------------------------------------------------------------------
// `qatlasd config show`
// ---------------------------------------------------------------------------

// clearQATLASEnv unsets every QuantumAtlas-relevant env var so the test
// sees a clean baseline regardless of the developer's shell.
func clearQATLASEnv(t *testing.T) {
	t.Helper()
	for _, raw := range os.Environ() {
		eq := strings.IndexByte(raw, '=')
		if eq <= 0 {
			continue
		}
		name := raw[:eq]
		for _, prefix := range envPrefixes {
			if strings.HasPrefix(name, prefix) {
				t.Setenv(name, "")
				break
			}
		}
	}
}

func TestConfigShow_RedactsSecretValues(t *testing.T) {
	clearQATLASEnv(t)
	t.Setenv("QATLAS_SERVER_URL", "https://test.example.com")
	t.Setenv("QATLAS_S3_ACCESS_KEY_ID", "AKIA-EXAMPLE-NEVER-REAL")
	t.Setenv("QATLAS_S3_SECRET_ACCESS_KEY", "SECRET-EXAMPLE-NEVER-REAL")
	t.Setenv("NEO4J_PASSWORD", "supersecret")
	t.Setenv("GITHUB_CLIENT_SECRET", "ghs_example")

	cmd := NewConfigCommand()
	buf := &bytes.Buffer{}
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"show"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("config show: %v", err)
	}

	got := buf.String()
	// Plaintext fields stay visible.
	if !strings.Contains(got, "QATLAS_SERVER_URL=https://test.example.com") {
		t.Errorf("expected QATLAS_SERVER_URL plaintext; got:\n%s", got)
	}
	// Secret fields must be redacted.
	for _, secretName := range []string{"QATLAS_S3_ACCESS_KEY_ID", "QATLAS_S3_SECRET_ACCESS_KEY", "NEO4J_PASSWORD", "GITHUB_CLIENT_SECRET"} {
		if !strings.Contains(got, secretName+"=***") {
			t.Errorf("expected %s=***; got line missing or unredacted:\n%s", secretName, got)
		}
	}
	// And the plaintext secrets must NOT leak through.
	for _, plain := range []string{"AKIA-EXAMPLE-NEVER-REAL", "SECRET-EXAMPLE-NEVER-REAL", "supersecret", "ghs_example"} {
		if strings.Contains(got, plain) {
			t.Errorf("secret %q leaked into show output:\n%s", plain, got)
		}
	}
}

func TestConfigShow_NoRedactShowsPlaintext(t *testing.T) {
	clearQATLASEnv(t)
	t.Setenv("NEO4J_PASSWORD", "supersecret")

	cmd := NewConfigCommand()
	buf := &bytes.Buffer{}
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"show", "--no-redact"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("config show --no-redact: %v", err)
	}
	if !strings.Contains(buf.String(), "NEO4J_PASSWORD=supersecret") {
		t.Errorf("--no-redact should reveal plaintext; got:\n%s", buf.String())
	}
}

func TestConfigShow_SortsAlphabeticallyAndSkipsEmpty(t *testing.T) {
	clearQATLASEnv(t)
	t.Setenv("QATLAS_SERVER_URL", "https://b.example")
	t.Setenv("NEO4J_URI", "bolt://a.example:7687")
	t.Setenv("QATLAS_EDGE_NAME", "us-east")
	t.Setenv("MINERU_API_TOKEN", "")
	t.Setenv("MINERU_API_BASE_URL", "https://mineru.example")

	cmd := NewConfigCommand()
	buf := &bytes.Buffer{}
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"show"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("config show: %v", err)
	}

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	wantOrder := []string{
		"MINERU_API_BASE_URL=https://mineru.example",
		"NEO4J_URI=bolt://a.example:7687",
		"QATLAS_EDGE_NAME=us-east",
		"QATLAS_SERVER_URL=https://b.example",
	}
	if len(lines) != len(wantOrder) {
		t.Fatalf("got %d lines, want %d:\n%s", len(lines), len(wantOrder), buf.String())
	}
	for i, want := range wantOrder {
		if lines[i] != want {
			t.Errorf("line[%d] = %q, want %q", i, lines[i], want)
		}
	}
}

func TestConfigShow_FilterOnlyQuantumAtlasVars(t *testing.T) {
	clearQATLASEnv(t)
	t.Setenv("QATLAS_SERVER_URL", "https://only.example")
	// Random unrelated var that must NOT appear (PATH is always set).
	t.Setenv("UNRELATED_VAR_FOR_TEST", "leak-me-if-broken")

	cmd := NewConfigCommand()
	buf := &bytes.Buffer{}
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"show"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("config show: %v", err)
	}

	if !strings.Contains(buf.String(), "QATLAS_SERVER_URL=") {
		t.Errorf("expected QATLAS_SERVER_URL to be listed; got:\n%s", buf.String())
	}
	if strings.Contains(buf.String(), "UNRELATED_VAR_FOR_TEST") {
		t.Errorf("non-QuantumAtlas env vars must not appear in show; got:\n%s", buf.String())
	}
	// And PATH (always set) also must not leak.
	if strings.Contains(buf.String(), "PATH=") {
		t.Errorf("PATH should not appear in show; got:\n%s", buf.String())
	}
}

// ---------------------------------------------------------------------------
// `qatlasd config path`
// ---------------------------------------------------------------------------

func TestConfigPath_ReportsQATLAS_DOTENV(t *testing.T) {
	tmp := t.TempDir()
	envFile := filepath.Join(tmp, "custom.env")
	if err := os.WriteFile(envFile, []byte("# stub\n"), 0o600); err != nil {
		t.Fatalf("write stub: %v", err)
	}
	t.Setenv("QATLAS_DOTENV", envFile)

	cmd := NewConfigCommand()
	buf := &bytes.Buffer{}
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"path"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("config path: %v", err)
	}
	got := strings.TrimSpace(buf.String())
	want, _ := filepath.Abs(envFile)
	if got != want {
		t.Errorf("config path = %q, want %q", got, want)
	}
}

func TestConfigPath_ErrorsWhenNoFile(t *testing.T) {
	// Isolated dir with no .env and no QATLAS_DOTENV pointer.
	tmp := t.TempDir()
	t.Setenv("QATLAS_DOTENV", "")
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })
	if err := os.Chdir(tmp); err != nil {
		t.Fatalf("chdir tmp: %v", err)
	}

	cmd := NewConfigCommand()
	buf := &bytes.Buffer{}
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SilenceUsage = true
	cmd.SetArgs([]string{"path"})
	if err := cmd.Execute(); err == nil {
		t.Errorf("expected error when no .env; got success out=%s", buf.String())
	}
}

// ---------------------------------------------------------------------------
// secrecy heuristic guard (catch silent regressions on new field types)
// ---------------------------------------------------------------------------

func TestIsSecretName_KnownClasses(t *testing.T) {
	cases := map[string]bool{
		"QATLAS_S3_ACCESS_KEY_ID":     true,
		"QATLAS_S3_SECRET_ACCESS_KEY": true,
		"NEO4J_PASSWORD":              true,
		"GITHUB_CLIENT_SECRET":        true,
		"MINERU_API_TOKEN":            true,
		"QATLAS_SYSTEM_PAT":           false, // intentional: name doesn't hint secret
		"QATLAS_SERVER_URL":           false,
		"NEO4J_URI":                   false,
		"NEO4J_USERNAME":              false,
		"QATLAS_EDGE_NAME":            false,
	}
	for name, want := range cases {
		if got := isSecretName(name); got != want {
			t.Errorf("isSecretName(%q) = %v, want %v", name, got, want)
		}
	}
}
