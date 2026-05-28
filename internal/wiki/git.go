// Package wiki — git subprocess helpers used by the /api/wiki/sync/*
// endpoints. We deliberately shell out to the `git` binary rather than
// pull in go-git: the operations needed (rev-parse, fetch, pull --ff-only)
// are simple, and shelling out matches the existing Python implementation
// exactly, which makes operational behavior easy to reason about across
// the transition.
package wiki

import (
	"context"
	"errors"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// GitInfo captures the local state of a wiki working tree. Mirrors the
// Python _git_info() return shape exactly so the JSON payload is
// byte-compatible with the existing UI.
type GitInfo struct {
	Enabled    bool         `json:"enabled"`
	Branch     string       `json:"branch,omitempty"`
	Commit     string       `json:"commit,omitempty"`
	CommitTime string       `json:"commit_time,omitempty"` // ISO 8601, from `git log -1 --format=%cI HEAD`
	Upstream   string       `json:"upstream,omitempty"`
	Ahead      *int         `json:"ahead,omitempty"`
	Behind     *int         `json:"behind,omitempty"`
	Dirty      *bool        `json:"dirty,omitempty"`
	Warnings   []GitWarning `json:"warnings,omitempty"`
}

// GitWarning is one structured warning entry under GitInfo.Warnings.
type GitWarning struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Branch  string `json:"branch,omitempty"`
}

// gitRun executes `git <args...>` in dir with the given timeout. The
// returned bool indicates whether the command was actually launched
// (false if git was missing or the timeout aborted the start).
func gitRun(dir string, timeout time.Duration, args ...string) (stdout string, stderr string, code int, ok bool) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	var outBuf, errBuf strings.Builder
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf

	if err := cmd.Run(); err != nil {
		// Distinguish "git not installed" from "git ran but exited non-zero".
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return outBuf.String(), errBuf.String(), exitErr.ExitCode(), true
		}
		return outBuf.String(), errBuf.String(), -1, false
	}
	return outBuf.String(), errBuf.String(), 0, true
}

// gitOutput is the trimmed stdout of a successful git command, or "" on
// any failure (matches Python _git_output behavior).
func gitOutput(dir string, args ...string) string {
	stdout, _, code, ok := gitRun(dir, 2*time.Second, args...)
	if !ok || code != 0 {
		return ""
	}
	return strings.TrimSpace(stdout)
}

// ReadGitInfo inspects dir as a git working tree and returns its state.
// Returns {Enabled: false} for non-existent or non-git directories.
func ReadGitInfo(dir string) GitInfo {
	// rev-parse --is-inside-work-tree returns "true" for valid worktrees
	// and exit-codes non-zero (or returns "false") otherwise.
	if gitOutput(dir, "rev-parse", "--is-inside-work-tree") != "true" {
		return GitInfo{Enabled: false}
	}

	info := GitInfo{Enabled: true}
	info.Branch = gitOutput(dir, "branch", "--show-current")
	info.Commit = gitOutput(dir, "rev-parse", "--short", "HEAD")
	// %cI is the committer date in strict ISO 8601 (RFC 3339). Falls
	// back to empty string when HEAD doesn't exist (fresh / empty repo).
	info.CommitTime = gitOutput(dir, "log", "-1", "--format=%cI", "HEAD")
	info.Upstream = gitOutput(dir, "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{upstream}")
	info.Ahead, info.Behind = gitCounts(dir, info.Upstream)

	if status, ok := gitStatus(dir); ok {
		dirty := status != ""
		info.Dirty = &dirty
	}

	if info.Branch != "" && info.Branch != "main" && info.Branch != "master" {
		info.Warnings = append(info.Warnings, GitWarning{
			Code:    "wiki_branch_not_main",
			Message: "Wiki repo is not checked out on main or master.",
			Branch:  info.Branch,
		})
	}

	return info
}

// gitStatus returns the porcelain status output and whether the call
// succeeded. Empty string + true = clean tree.
func gitStatus(dir string) (string, bool) {
	stdout, _, code, ok := gitRun(dir, 2*time.Second, "status", "--porcelain")
	if !ok || code != 0 {
		return "", false
	}
	return strings.TrimSpace(stdout), true
}

// gitCounts returns (ahead, behind) commit counts vs upstream. nil/nil
// when upstream is empty or the call fails. Splits the "ahead\tbehind"
// output of `rev-list --left-right --count HEAD...upstream`.
func gitCounts(dir, upstream string) (*int, *int) {
	if upstream == "" {
		return nil, nil
	}
	out := gitOutput(dir, "rev-list", "--left-right", "--count", "HEAD..."+upstream)
	if out == "" {
		return nil, nil
	}
	parts := strings.Fields(out)
	if len(parts) != 2 {
		return nil, nil
	}
	a, err1 := strconv.Atoi(parts[0])
	b, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil {
		return nil, nil
	}
	return &a, &b
}

// PullResult is the JSON shape returned by /api/wiki/sync/pull on success.
type PullResult struct {
	Status    string `json:"status"`
	Changed   bool   `json:"changed"`
	OldCommit string `json:"old_commit"`
	NewCommit string `json:"new_commit"`
}

// PullError carries a status code and a human-readable message so the
// route handler can map onto an HTTPException-equivalent response.
type PullError struct {
	Status  int    // intended HTTP status
	Detail  string // safe-to-show error message
}

func (e *PullError) Error() string {
	return e.Detail
}

// Pull runs `git fetch --prune` + `git pull --ff-only` on dir. Returns
// PullResult on success or a PullError describing the failure mode in
// the same way the Python wiki_sync_pull does (HTTP 409 / 502 / 500).
func Pull(dir string) (*PullResult, error) {
	before := ReadGitInfo(dir)
	if !before.Enabled {
		return nil, &PullError{Status: 409, Detail: "wiki directory is not a git repository"}
	}
	if before.Dirty != nil && *before.Dirty {
		return nil, &PullError{Status: 409, Detail: "wiki worktree has local changes"}
	}

	oldCommit := gitOutput(dir, "rev-parse", "--short", "HEAD")

	_, stderr, code, ok := gitRun(dir, 30*time.Second, "fetch", "--prune")
	if !ok {
		return nil, &PullError{Status: 500, Detail: "git fetch could not be executed"}
	}
	if code != 0 {
		return nil, &PullError{Status: 502, Detail: nonEmpty(stderr, "git fetch failed")}
	}

	afterFetch := ReadGitInfo(dir)
	if afterFetch.Dirty != nil && *afterFetch.Dirty {
		return nil, &PullError{Status: 409, Detail: "wiki worktree has local changes"}
	}

	_, stderr, code, ok = gitRun(dir, 30*time.Second, "pull", "--ff-only")
	if !ok {
		return nil, &PullError{Status: 500, Detail: "git pull could not be executed"}
	}
	if code != 0 {
		return nil, &PullError{Status: 409, Detail: nonEmpty(stderr, "git pull --ff-only failed")}
	}

	newCommit := gitOutput(dir, "rev-parse", "--short", "HEAD")
	return &PullResult{
		Status:    "succeeded",
		Changed:   oldCommit != newCommit,
		OldCommit: oldCommit,
		NewCommit: newCommit,
	}, nil
}

// nonEmpty returns s trimmed if non-empty, otherwise fallback.
func nonEmpty(s, fallback string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return fallback
	}
	return s
}
