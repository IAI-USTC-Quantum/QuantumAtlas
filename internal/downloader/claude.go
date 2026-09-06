package downloader

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// ClaudeExtractor drives the local claude CLI headlessly — the same
// invocation shape internal/agentic uses (`claude -p … --output-format
// json --json-schema … --allowedTools "Read Glob Grep"`), scoped down
// to a throwaway directory holding the landing-page HTML. The model
// reads the file itself, so multi-hundred-KB pages don't have to ride
// inside the prompt. Requires a claude binary logged in on this host.
type ClaudeExtractor struct {
	cfg AgentConfig
}

func (c *ClaudeExtractor) Name() string { return "agent:claude" }

const claudeAnswerSchema = `{
  "type": "object",
  "properties": {
    "candidates": {
      "type": "array",
      "items": {"type": "string"},
      "maxItems": 5
    }
  },
  "required": ["candidates"]
}`

const claudeTaskPrompt = `A file named landing.html in this directory contains the HTML of an academic article landing page (it may be truncated).

%TASK_RULES%

Read landing.html (and Glob/Grep it as needed) and identify direct PDF download URLs for THIS article.
Write your answer to a file named answer.json in this directory, containing exactly: {"candidates": ["<absolute pdf url>", ...]} (at most 5, ordered by confidence, empty array if none).`

const claudeTaskRules = `Rules: only absolute http(s) URLs serving the full-text PDF of this article; prefer direct-download links (citation_pdf_url, PDF buttons, stamp/getPDF endpoints) over viewer pages; no landing/TOC pages, supplementary files, or other articles' links.`

func (c *ClaudeExtractor) Extract(ctx context.Context, input ExtractInput) ([]string, error) {
	timeout := c.cfg.Timeout
	if timeout == 0 {
		timeout = 120 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	dir, err := os.MkdirTemp("", "qatlas-downloader-claude-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)

	html := input.HTML
	const maxHTML = 400 * 1024
	if len(html) > maxHTML {
		html = html[:maxHTML]
	}
	if err := os.WriteFile(filepath.Join(dir, "landing.html"), html, 0o600); err != nil {
		return nil, err
	}

	bin := c.cfg.ClaudeBin
	if bin == "" {
		bin = "claude"
	}
	prompt := strings.Replace(claudeTaskPrompt, "%TASK_RULES%", claudeTaskRules, 1)
	if input.DOI != "" || input.LandingURL != "" {
		prompt += fmt.Sprintf("\nArticle DOI: %s\nLanding page URL: %s", input.DOI, input.LandingURL)
	}

	args := []string{
		"-p", prompt,
		"--output-format", "json",
		"--permission-mode", "bypassPermissions",
		"--allowedTools", "Read Write Glob Grep",
		"--no-session-persistence",
	}
	if c.cfg.ClaudeModel != "" {
		args = append(args, "--model", c.cfg.ClaudeModel)
	}
	if c.cfg.MaxBudgetUSD > 0 {
		args = append(args, "--max-budget-usd", fmt.Sprintf("%.4f", c.cfg.MaxBudgetUSD))
	}

	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("claude extractor: %v: %.300s", err, strings.TrimSpace(stderr.String()))
	}

	// Preferred path: the model wrote answer.json in the sandbox dir.
	if raw, err := os.ReadFile(filepath.Join(dir, "answer.json")); err == nil {
		if cands := parseAgentAnswer(string(raw)); len(cands) > 0 {
			return cands, nil
		}
	}
	// Fallback: parse the CLI's JSON envelope result field.
	var envelope struct {
		Result string `json:"result"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &envelope); err == nil && envelope.Result != "" {
		if cands := parseAgentAnswer(envelope.Result); len(cands) > 0 {
			return cands, nil
		}
	}
	if cands := parseAgentAnswer(stdout.String()); len(cands) > 0 {
		return cands, nil
	}
	return nil, fmt.Errorf("claude extractor: no parsable candidates")
}
