package main

import (
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"testing"
)

// fakeHomeForTest plants a writable HOME with a placeholder .env so
// buildServiceConfig / computeReadWritePaths produce deterministic
// output regardless of the real test runner's environment.
//
// Returns the fake home path and the absolute .env path under it.
func fakeHomeForTest(t *testing.T) (home, dotenvPath string) {
	t.Helper()
	home = t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("SUDO_USER", "")
	repoDir := filepath.Join(home, "QuantumAtlas")
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		t.Fatalf("mkdir repoDir: %v", err)
	}
	dotenvPath = filepath.Join(repoDir, ".env")
	if err := os.WriteFile(dotenvPath, []byte("# fixture\n"), 0o600); err != nil {
		t.Fatalf("write .env: %v", err)
	}
	return home, dotenvPath
}

// TestRenderSystemdUnitUserMode pins the exact user-mode unit content.
//
// Drift detection: kardianos/service ships its default template fields
// (cmd, cmdEscape, .EnvVars, .Option access) — if the library changes
// any of those (e.g. cmd starts using single quotes), or if we forget
// to keep templateFuncs in sync with the library's tf var, this
// snapshot fails and forces a deliberate review.
func TestRenderSystemdUnitUserMode(t *testing.T) {
	home, dotenvPath := fakeHomeForTest(t)

	cfg, err := buildServiceConfig(serviceInstallOpts{
		Name:       "qatlas-server",
		Mode:       "user",
		DotenvPath: dotenvPath,
		Bind:       "127.0.0.1:4200",
	})
	if err != nil {
		t.Fatalf("buildServiceConfig: %v", err)
	}

	got, err := renderSystemdUnit(cfg, "/fixed/bin/qatlas-server")
	if err != nil {
		t.Fatalf("renderSystemdUnit: %v", err)
	}

	want := `[Unit]
Description=QuantumAtlas server (Go + PocketBase)
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
WorkingDirectory=` + filepath.Join(home, "QuantumAtlas") + `
Environment=QATLAS_DOTENV=` + dotenvPath + `
ExecStart=/fixed/bin/qatlas-server "serve" "--http=127.0.0.1:4200"
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
ReadWritePaths=` + filepath.Join(home, "QuantumAtlas") + ` ` + filepath.Join(home, ".local/share/quantum-atlas") + `
LockPersonality=true
RestrictRealtime=true

[Install]
WantedBy=default.target
`
	if got != want {
		t.Errorf("user-mode unit mismatch.\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

// TestRenderSystemdUnitSystemMode mirrors the user-mode snapshot for
// system mode: expects User= line + WantedBy=multi-user.target.
func TestRenderSystemdUnitSystemMode(t *testing.T) {
	home, dotenvPath := fakeHomeForTest(t)
	t.Setenv("SUDO_USER", "deployer")

	cfg, err := buildServiceConfig(serviceInstallOpts{
		Name:       "qatlas-server",
		Mode:       "system",
		DotenvPath: dotenvPath,
		Bind:       "0.0.0.0:4200",
	})
	if err != nil {
		t.Fatalf("buildServiceConfig: %v", err)
	}

	got, err := renderSystemdUnit(cfg, "/usr/local/bin/qatlas-server")
	if err != nil {
		t.Fatalf("renderSystemdUnit: %v", err)
	}

	want := `[Unit]
Description=QuantumAtlas server (Go + PocketBase)
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=deployer
WorkingDirectory=` + filepath.Join(home, "QuantumAtlas") + `
Environment=QATLAS_DOTENV=` + dotenvPath + `
ExecStart=/usr/local/bin/qatlas-server "serve" "--http=0.0.0.0:4200"
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
ReadWritePaths=` + filepath.Join(home, "QuantumAtlas") + ` ` + filepath.Join(home, ".local/share/quantum-atlas") + `
LockPersonality=true
RestrictRealtime=true

[Install]
WantedBy=multi-user.target
`
	if got != want {
		t.Errorf("system-mode unit mismatch.\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

// TestComputeReadWritePathsIncludesWiki verifies the wiki path is
// appended only when ~/QuantumAtlas-Wiki exists, since server-side
// git fetch needs write access to it.
func TestComputeReadWritePathsIncludesWiki(t *testing.T) {
	home, dotenvPath := fakeHomeForTest(t)
	wikiDir := filepath.Join(home, "QuantumAtlas-Wiki")
	if err := os.MkdirAll(wikiDir, 0o755); err != nil {
		t.Fatalf("mkdir wiki: %v", err)
	}

	paths := computeReadWritePaths(dotenvPath)
	joined := strings.Join(paths, " ")
	if !strings.Contains(joined, wikiDir) {
		t.Errorf("expected ReadWritePaths to include %s; got: %s", wikiDir, joined)
	}
}

// TestComputeReadWritePathsHonoursXDG verifies XDG_DATA_HOME redirects
// the share path when set, so installs on FHS-style hosts don't get
// pinned to ~/.local/share.
func TestComputeReadWritePathsHonoursXDG(t *testing.T) {
	home, dotenvPath := fakeHomeForTest(t)
	customShare := filepath.Join(home, "custom-xdg")
	t.Setenv("XDG_DATA_HOME", customShare)

	paths := computeReadWritePaths(dotenvPath)
	want := filepath.Join(customShare, "quantum-atlas")
	found := false
	for _, p := range paths {
		if p == want {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected ReadWritePaths to include %s when XDG_DATA_HOME is set; got: %v", want, paths)
	}
}

// TestResolveModeRejectsInvalid pins the validation of --mode values.
func TestResolveModeRejectsInvalid(t *testing.T) {
	opts := &serviceInstallOpts{Mode: "garbage"}
	err := resolveMode(opts, true)
	if err == nil {
		t.Fatal("expected error for --mode=garbage")
	}
	if !strings.Contains(err.Error(), "invalid --mode") {
		t.Errorf("expected 'invalid --mode' in error, got: %v", err)
	}
}

// TestResolveModeRequiresExplicitInNonTTY pins the safety guard that
// blocks non-interactive installs without --mode.
func TestResolveModeRequiresExplicitInNonTTY(t *testing.T) {
	opts := &serviceInstallOpts{} // no Mode
	err := resolveMode(opts, false /* not a TTY */)
	if err == nil {
		t.Fatal("expected error when --mode missing in non-TTY context")
	}
	if !strings.Contains(err.Error(), "--mode required") {
		t.Errorf("expected '--mode required' in error, got: %v", err)
	}
}

// TestValidateDotenvPathRejectsDirectory pins one of the few validations
// applied to the user-supplied dotenv path.
func TestValidateDotenvPathRejectsDirectory(t *testing.T) {
	dir := t.TempDir()
	err := validateDotenvPath(dir)
	if err == nil {
		t.Fatal("expected error for directory path")
	}
	if !strings.Contains(err.Error(), "is a directory") {
		t.Errorf("expected 'is a directory' in error, got: %v", err)
	}
}

// TestEffectiveHomeDirPrefersSudoUser verifies the sudo-aware fallback:
// when $SUDO_USER points to a real account, effectiveHomeDir returns that
// account's home, NOT the current process's $HOME.
//
// Regression target: pre-fix `computeReadWritePaths` called os.UserHomeDir
// directly, which under `sudo qatlas-server service install` returns /root
// (sudo's default HOME reset). The resulting ReadWritePaths granted writes
// to /root/.local/share/quantum-atlas — a path the eventual User=<sudo-user>
// daemon never touches, leaving the *actual* state dir blocked by
// ProtectSystem=full.
//
// The test impersonates a sudo invocation by setting $HOME to a junk dir
// and $SUDO_USER to the current process's username (guaranteed to be
// lookup-able on any host that runs `go test`; we deliberately do NOT
// hardcode a project-specific username).
func TestEffectiveHomeDirPrefersSudoUser(t *testing.T) {
	current, err := user.Current()
	if err != nil {
		t.Skipf("user.Current failed (cannot run in this environment): %v", err)
	}
	if current.HomeDir == "" {
		t.Skip("current user has no HomeDir; cannot run")
	}

	junkHome := t.TempDir()
	t.Setenv("HOME", junkHome) // simulate sudo's HOME reset
	t.Setenv("SUDO_USER", current.Username)

	got := effectiveHomeDir()
	if got != current.HomeDir {
		t.Errorf("effectiveHomeDir() = %q, want %q (resolved via $SUDO_USER, not $HOME=%q)",
			got, current.HomeDir, junkHome)
	}
}

// TestEffectiveHomeDirFallsBackToEnvHome verifies the non-sudo path:
// no $SUDO_USER -> effectiveHomeDir returns $HOME as-is.
func TestEffectiveHomeDirFallsBackToEnvHome(t *testing.T) {
	tempHome := t.TempDir()
	t.Setenv("HOME", tempHome)
	t.Setenv("SUDO_USER", "") // explicitly unset to defeat any inherited sudo context

	got := effectiveHomeDir()
	if got != tempHome {
		t.Errorf("effectiveHomeDir() = %q, want %q (no $SUDO_USER, should mirror $HOME)",
			got, tempHome)
	}
}

// TestComputeReadWritePathsUnderSimulatedSudo is the integration-level
// guard against the same bug: with $HOME pointing at a junk dir but
// $SUDO_USER naming a real account, the resulting ReadWritePaths must
// include the real account's $XDG_DATA_HOME/quantum-atlas, not the junk
// HOME's. This is the path that ultimately lands in the systemd unit.
func TestComputeReadWritePathsUnderSimulatedSudo(t *testing.T) {
	current, err := user.Current()
	if err != nil || current.HomeDir == "" {
		t.Skipf("cannot resolve current user (env limitation): %v", err)
	}

	junkHome := t.TempDir()
	t.Setenv("HOME", junkHome)
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("SUDO_USER", current.Username)

	// Use a .env under junkHome (a "wrong" dir) — the dotenv directory
	// is computed straight from absDotenv, not from $HOME, so it should
	// still appear correctly. What matters is the *share-derived* path:
	// it should resolve under the SUDO_USER's real home, not under
	// junkHome.
	dotenvPath := filepath.Join(junkHome, ".env")
	if err := os.WriteFile(dotenvPath, []byte("# fixture\n"), 0o600); err != nil {
		t.Fatalf("write .env: %v", err)
	}

	paths := computeReadWritePaths(dotenvPath)
	joined := strings.Join(paths, " ")

	wantShare := filepath.Join(current.HomeDir, ".local/share/quantum-atlas")
	unwantedShare := filepath.Join(junkHome, ".local/share/quantum-atlas")

	if !strings.Contains(joined, wantShare) {
		t.Errorf("expected ReadWritePaths to include %q (real SUDO_USER home), got: %s",
			wantShare, joined)
	}
	if strings.Contains(joined, unwantedShare) {
		t.Errorf("ReadWritePaths leaked junk $HOME path %q (sudo HOME bug regression); got: %s",
			unwantedShare, joined)
	}
}
