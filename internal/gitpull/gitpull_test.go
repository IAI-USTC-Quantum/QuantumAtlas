package gitpull

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestReadGitInfoNonGitDir(t *testing.T) {
	info := ReadGitInfo(t.TempDir())
	if info.Enabled {
		t.Fatal("non-git dir should report Enabled=false")
	}
}

func TestReadGitInfoGitRepo(t *testing.T) {
	dir := t.TempDir()
	runGit(t, dir, "init", "-b", "main")
	runGit(t, dir, "config", "user.email", "t@example.com")
	runGit(t, dir, "config", "user.name", "t")
	if err := writeFile(filepath.Join(dir, "f.txt"), "hi"); err != nil {
		t.Fatalf("write: %v", err)
	}
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-m", "init")

	info := ReadGitInfo(dir)
	if !info.Enabled {
		t.Fatal("git repo should report Enabled=true")
	}
	if info.Branch != "main" {
		t.Fatalf("branch = %q, want main", info.Branch)
	}
	if info.Commit == "" {
		t.Fatal("commit should be non-empty")
	}
	if len(info.Warnings) != 0 {
		t.Fatalf("main branch should warn-free, got %v", info.Warnings)
	}
}

func TestReadGitInfoNonMainWarns(t *testing.T) {
	dir := t.TempDir()
	runGit(t, dir, "init", "-b", "feature")
	runGit(t, dir, "config", "user.email", "t@example.com")
	runGit(t, dir, "config", "user.name", "t")
	if err := writeFile(filepath.Join(dir, "f.txt"), "hi"); err != nil {
		t.Fatalf("write: %v", err)
	}
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-m", "init")

	info := ReadGitInfo(dir)
	if len(info.Warnings) != 1 || info.Warnings[0].Code != "branch_not_main" {
		t.Fatalf("warnings = %v, want one branch_not_main", info.Warnings)
	}
}

func TestPullNonGitDirIsConflict(t *testing.T) {
	_, err := Pull(t.TempDir())
	pe, ok := err.(*PullError)
	if !ok {
		t.Fatalf("err = %T, want *PullError", err)
	}
	if pe.Status != 409 {
		t.Fatalf("status = %d, want 409", pe.Status)
	}
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func writeFile(path, body string) error {
	return os.WriteFile(path, []byte(body), 0o644)
}
