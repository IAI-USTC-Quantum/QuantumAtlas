package agentic

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"text/template"
	"time"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/search"
)

// DefaultTimeout bounds one claude CLI call when Options.Timeout is unset.
const DefaultTimeout = 5 * time.Minute

// Options configures a Runner.
type Options struct {
	// Engine supplies the fan-out (raw hits + per-provider failures) the
	// sandbox seeds from. Required.
	Engine *search.Engine
	// ClaudeBin is the claude CLI binary (default "claude", PATH lookup).
	// Injectable for tests.
	ClaudeBin string
	// Model overrides claude's default model (empty = CLI default).
	Model string
	// SandboxRoot is the parent directory of the per-request sandboxes.
	// Required.
	SandboxRoot string
	// Timeout bounds one claude call (default 5m).
	Timeout time.Duration
	// MaxBudgetUSD adds --max-budget-usd when > 0.
	MaxBudgetUSD float64
	// PromptTemplate overrides the embedded prompt (empty = embedded).
	PromptTemplate string
}

// Runner is the local agentic backend: it satisfies the route layer's
// AgenticBackend interface with the same signature as
// search.RemoteProvider.SearchAgentic.
type Runner struct {
	opts Options
	tmpl *template.Template
}

// NewRunner validates opts and compiles the prompt template.
func NewRunner(opts Options) (*Runner, error) {
	if opts.Engine == nil {
		return nil, errors.New("agentic: Options.Engine is required")
	}
	if strings.TrimSpace(opts.SandboxRoot) == "" {
		return nil, errors.New("agentic: Options.SandboxRoot is required")
	}
	if opts.ClaudeBin == "" {
		opts.ClaudeBin = "claude"
	}
	if opts.Timeout <= 0 {
		opts.Timeout = DefaultTimeout
	}
	tplText := defaultPromptTemplate
	if strings.TrimSpace(opts.PromptTemplate) != "" {
		tplText = opts.PromptTemplate
	}
	tmpl, err := template.New("prompt").Parse(tplText)
	if err != nil {
		return nil, fmt.Errorf("agentic: parse prompt template: %w", err)
	}
	return &Runner{opts: opts, tmpl: tmpl}, nil
}

// runMeta is the per-request audit record written to meta.json.
type runMeta struct {
	Agent          bool              `json:"agent"`
	StartedAt      string            `json:"started_at"`
	DurationMs     int64             `json:"duration_ms"`
	ExitCode       int               `json:"exit_code"`
	LLMTokens      int64             `json:"llm_tokens,omitempty"`
	CostUSD        float64           `json:"cost_usd,omitempty"`
	Subtype        string            `json:"subtype,omitempty"`
	ProviderErrors map[string]string `json:"provider_errors,omitempty"`
	Error          string            `json:"error,omitempty"`
}

// SearchAgentic implements the agentic backend contract. The normalized
// entry and the fan-out hits always land in a fresh sandbox; agent=false
// stops there and returns the merged hits in the standardized response
// (conclusion nil, errors = provider failure table, no claude call).
// agent=true renders the prompt, runs claude inside the sandbox's run/
// directory and normalizes its structured output. A claude/parse failure
// is returned as a real error so the route can refund the user's quota —
// the sandbox (including meta.json with the failure detail) is kept for
// audit and reaped by the janitor. The sources parameter (remote-backend
// pinning) is ignored by the local runner: the local provider set is
// configured server-side.
func (r *Runner) SearchAgentic(ctx context.Context, entry search.SearchEntry, agent bool, _ []string) (search.RemoteResponse, error) {
	entry.Normalize()
	sb, err := NewSandbox(r.opts.SandboxRoot)
	if err != nil {
		return search.RemoteResponse{}, err
	}
	meta := &runMeta{Agent: agent, StartedAt: time.Now().UTC().Format(time.RFC3339), ExitCode: -1}
	started := time.Now()

	// query.json: the normalized entry plus the agent flag.
	if err := sb.WriteJSON("query.json", struct {
		search.SearchEntry
		Agent bool `json:"agent"`
	}{SearchEntry: entry, Agent: agent}); err != nil {
		return search.RemoteResponse{}, fmt.Errorf("agentic local: %w", err)
	}

	hits, provErrs := r.opts.Engine.Collect(ctx, entry)
	if err := sb.WriteJSON("results.json", hits); err != nil {
		return search.RemoteResponse{}, fmt.Errorf("agentic local: %w", err)
	}
	meta.ProviderErrors = errStrings(provErrs)

	if !agent {
		resp := search.RemoteResponse{
			Hits:   toRemoteHits(hits, entry.MaxResults),
			Errors: errStrings(provErrs),
		}
		r.finishMeta(sb, meta, started, nil)
		_ = sb.WriteJSON("response.json", resp)
		return resp, nil
	}

	prompt, err := renderPrompt(r.tmpl, entry, "../results.json")
	if err != nil {
		r.finishMeta(sb, meta, started, err)
		return search.RemoteResponse{}, fmt.Errorf("agentic local: render prompt: %w", err)
	}
	if err := sb.WriteText("prompt.txt", prompt); err != nil {
		r.finishMeta(sb, meta, started, err)
		return search.RemoteResponse{}, fmt.Errorf("agentic local: %w", err)
	}
	runDir, err := sb.RunDir()
	if err != nil {
		r.finishMeta(sb, meta, started, err)
		return search.RemoteResponse{}, err
	}

	stdout, exitCode, runErr := r.execClaude(ctx, runDir, prompt)
	meta.ExitCode = exitCode
	if runErr != nil {
		r.finishMeta(sb, meta, started, runErr)
		return search.RemoteResponse{}, runErr
	}

	ans, llmTokens, costUSD, err := parseEnvelope(stdout)
	meta.LLMTokens = llmTokens
	meta.CostUSD = costUSD
	if err != nil {
		r.finishMeta(sb, meta, started, err)
		return search.RemoteResponse{}, err
	}

	respHits, conclusion := normalizeAnswer(ans, entry.MaxResults)
	resp := search.RemoteResponse{
		Hits:       respHits,
		Conclusion: conclusion,
		Usage:      search.RemoteUsage{LLMTokens: llmTokens},
		Errors:     errStrings(provErrs),
	}
	r.finishMeta(sb, meta, started, nil)
	if err := sb.WriteJSON("response.json", resp); err != nil {
		return search.RemoteResponse{}, fmt.Errorf("agentic local: %w", err)
	}
	return resp, nil
}

// finishMeta stamps the duration + failure detail and writes meta.json
// (best-effort — an audit write never fails the request).
func (r *Runner) finishMeta(sb *Sandbox, meta *runMeta, started time.Time, runErr error) {
	meta.DurationMs = time.Since(started).Milliseconds()
	if runErr != nil {
		meta.Error = runErr.Error()
	}
	_ = sb.WriteJSON("meta.json", meta)
}

// execClaude runs one headless claude call with the prompt as the -p
// argument (never via a shell), cwd pinned to the sandbox's run dir.
// stdout is the JSON envelope; a non-zero exit or a context timeout is
// reported as an error carrying stderr.
func (r *Runner) execClaude(ctx context.Context, runDir, prompt string) (stdout []byte, exitCode int, err error) {
	cctx, cancel := context.WithTimeout(ctx, r.opts.Timeout)
	defer cancel()

	args := []string{
		"-p", prompt,
		"--output-format", "json",
		"--json-schema", answerSchema,
		"--permission-mode", "bypassPermissions",
		"--allowedTools", "Read Write Glob Grep",
		"--no-session-persistence",
	}
	if r.opts.Model != "" {
		args = append(args, "--model", r.opts.Model)
	}
	if r.opts.MaxBudgetUSD > 0 {
		args = append(args, "--max-budget-usd", strconv.FormatFloat(r.opts.MaxBudgetUSD, 'f', -1, 64))
	}

	cmd := exec.CommandContext(cctx, r.opts.ClaudeBin, args...)
	cmd.Dir = runDir
	var outBuf, errBuf strings.Builder
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf

	if runErr := cmd.Run(); runErr != nil {
		if cctx.Err() != nil {
			return []byte(outBuf.String()), -1, fmt.Errorf("agentic local: claude call timed out after %s", r.opts.Timeout)
		}
		var exitErr *exec.ExitError
		if errors.As(runErr, &exitErr) {
			return []byte(outBuf.String()), exitErr.ExitCode(), fmt.Errorf(
				"agentic local: claude exited %d: %s", exitErr.ExitCode(), nonEmptyTrimmed(errBuf.String(), "no stderr"))
		}
		return []byte(outBuf.String()), -1, fmt.Errorf("agentic local: run claude: %w", runErr)
	}
	return []byte(outBuf.String()), 0, nil
}

// errStrings converts the per-provider failure table to the wire shape
// (nil when empty so the response's errors map stays clean).
func errStrings(errs map[string]error) map[string]string {
	if len(errs) == 0 {
		return nil
	}
	out := make(map[string]string, len(errs))
	for name, err := range errs {
		out[name] = err.Error()
	}
	return out
}

func nonEmptyTrimmed(s, fallback string) string {
	if s = strings.TrimSpace(s); s != "" {
		return s
	}
	return fallback
}
