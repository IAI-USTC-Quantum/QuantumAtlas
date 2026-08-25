package agentic

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/search"
)

// fakeProvider mirrors search's engine_test fakeProvider: canned hits,
// contract-style failure recording via BaseProvider.
type fakeProvider struct {
	search.BaseProvider
	name string
	hits []search.Hit
	err  error
}

func (f *fakeProvider) Name() string { return f.name }

func (f *fakeProvider) Search(_ context.Context, _ search.SearchEntry) ([]search.Hit, error) {
	if f.err != nil {
		return f.RecordFailure(f.err)
	}
	return f.hits, nil
}

// writeFakeClaude installs a shell script as the claude binary inside
// t.TempDir(). The script convention: every argv line is logged to
// $FAKE_CLAUDE_ARGV (when set), then the script body runs.
func writeFakeClaude(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "claude")
	script := "#!/bin/sh\n" +
		"if [ -n \"$FAKE_CLAUDE_ARGV\" ]; then for a in \"$@\"; do printf '%s\\n' \"$a\"; done > \"$FAKE_CLAUDE_ARGV\"; fi\n" +
		body
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake claude: %v", err)
	}
	return path
}

// happyEnvelope is a well-formed claude JSON envelope whose result is a
// JSON STRING carrying the structured answer (the common headless shape).
// The answer deliberately includes a blank-title hit (dropped) and a
// scoreless hit (default 0.5) to exercise normalization.
const happyEnvelope = `{
  "type": "result",
  "subtype": "success",
  "is_error": false,
  "result": "{\"conclusion\":\"Quantum sensors are useful.\",\"hits\":[{\"title\":\"Paper A\",\"doi\":\"10.1/a\",\"score\":0.9},{\"title\":\"   \"},{\"title\":\"Paper B\",\"arxiv_id\":\"2401.00001\"}]}",
  "usage": {"input_tokens": 120, "output_tokens": 34},
  "total_cost_usd": 0.0123
}`

func newTestRunner(t *testing.T, claudeBin string, engine *search.Engine, mutate func(*Options)) *Runner {
	t.Helper()
	opts := Options{
		Engine:      engine,
		ClaudeBin:   claudeBin,
		SandboxRoot: t.TempDir(),
		Timeout:     10 * time.Second,
	}
	if mutate != nil {
		mutate(&opts)
	}
	r, err := NewRunner(opts)
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	return r
}

func testEngine(providers ...search.Provider) *search.Engine {
	return search.NewEngine(nil, nil, providers...)
}

func TestRunner_AgentHappyPath(t *testing.T) {
	argvLog := filepath.Join(t.TempDir(), "argv.log")
	t.Setenv("FAKE_CLAUDE_ARGV", argvLog)
	bin := writeFakeClaude(t, "cat <<'ENVELOPE'\n"+happyEnvelope+"\nENVELOPE\n")

	engine := testEngine(&fakeProvider{name: "stub", hits: []search.Hit{
		{Title: "Raw A", DOI: "10.1/a", Score: 0.7, Source: "stub"},
	}})
	r := newTestRunner(t, bin, engine, func(o *Options) {
		o.Model = "claude-sonnet-4-5"
		o.MaxBudgetUSD = 1.5
	})

	resp, err := r.SearchAgentic(context.Background(), search.SearchEntry{Text: "quantum sensors"}, true)
	if err != nil {
		t.Fatalf("SearchAgentic: %v", err)
	}
	if resp.Conclusion == nil || *resp.Conclusion != "Quantum sensors are useful." {
		t.Errorf("Conclusion = %v", resp.Conclusion)
	}
	if len(resp.Hits) != 2 {
		t.Fatalf("len(Hits) = %d, want 2 (blank title dropped)", len(resp.Hits))
	}
	if resp.Hits[0].Title != "Paper A" || resp.Hits[0].Score != 0.9 || resp.Hits[0].Source != "agentic-local" {
		t.Errorf("Hits[0] = %+v", resp.Hits[0])
	}
	if resp.Hits[1].Score != 0.5 {
		t.Errorf("Hits[1].Score = %v, want default 0.5", resp.Hits[1].Score)
	}
	if resp.Usage.LLMTokens != 154 {
		t.Errorf("LLMTokens = %d, want 120+34=154", resp.Usage.LLMTokens)
	}

	// claude argv: the exact headless flag set, plus --model and
	// --max-budget-usd from the options.
	rawArgv, err := os.ReadFile(argvLog)
	if err != nil {
		t.Fatalf("read argv log: %v", err)
	}
	argv := string(rawArgv)
	for _, want := range []string{
		"-p\n", "--output-format\njson\n", "--json-schema\n",
		"--permission-mode\nbypassPermissions\n", "--allowedTools\nRead Write Glob Grep\n",
		"--no-session-persistence\n", "--model\nclaude-sonnet-4-5\n", "--max-budget-usd\n1.5\n",
	} {
		if !strings.Contains(argv, want) {
			t.Errorf("claude argv missing %q\nfull argv:\n%s", want, argv)
		}
	}
	if strings.Contains(argv, "--bare") {
		t.Errorf("claude argv must not contain --bare (would disable OAuth):\n%s", argv)
	}

	// Sandbox artifacts: find the single sandbox under the root.
	sandboxes, err := os.ReadDir(r.opts.SandboxRoot)
	if err != nil || len(sandboxes) != 1 {
		t.Fatalf("sandboxes = %v, err = %v", sandboxes, err)
	}
	dir := filepath.Join(r.opts.SandboxRoot, sandboxes[0].Name())
	for _, name := range []string{"query.json", "results.json", "prompt.txt", "response.json", "meta.json", "run"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("sandbox artifact %s missing: %v", name, err)
		}
	}
	if matches, _ := filepath.Glob(filepath.Join(dir, "*.part")); len(matches) > 0 {
		t.Errorf("stray .part files left behind: %v", matches)
	}
	prompt, _ := os.ReadFile(filepath.Join(dir, "prompt.txt"))
	if !strings.Contains(string(prompt), "quantum sensors") {
		t.Errorf("prompt.txt does not mention the query")
	}
	metaRaw, _ := os.ReadFile(filepath.Join(dir, "meta.json"))
	var meta runMeta
	if err := json.Unmarshal(metaRaw, &meta); err != nil {
		t.Fatalf("meta.json: %v", err)
	}
	if meta.ExitCode != 0 || meta.LLMTokens != 154 || meta.Error != "" || !meta.Agent {
		t.Errorf("meta = %+v", meta)
	}
}

func TestRunner_AgentResultObject(t *testing.T) {
	// Some CLI versions return result as an already-parsed object.
	bin := writeFakeClaude(t, `cat <<'ENVELOPE'
{"is_error": false, "result": {"conclusion": "obj conclusion", "hits": [{"title": "Obj Paper"}]}, "usage": {"input_tokens": 1, "output_tokens": 2}}
ENVELOPE
`)
	r := newTestRunner(t, bin, testEngine(), nil)
	resp, err := r.SearchAgentic(context.Background(), search.SearchEntry{Text: "x"}, true)
	if err != nil {
		t.Fatalf("SearchAgentic: %v", err)
	}
	if resp.Conclusion == nil || *resp.Conclusion != "obj conclusion" {
		t.Errorf("Conclusion = %v", resp.Conclusion)
	}
	if len(resp.Hits) != 1 || resp.Hits[0].Title != "Obj Paper" {
		t.Errorf("Hits = %+v", resp.Hits)
	}
	if resp.Usage.LLMTokens != 3 {
		t.Errorf("LLMTokens = %d, want 3", resp.Usage.LLMTokens)
	}
}

func TestRunner_AgentClaudeError(t *testing.T) {
	bin := writeFakeClaude(t, `cat <<'ENVELOPE'
{"is_error": true, "subtype": "error_max_budget_usd", "result": "", "total_cost_usd": 0.5}
ENVELOPE
`)
	r := newTestRunner(t, bin, testEngine(), nil)
	_, err := r.SearchAgentic(context.Background(), search.SearchEntry{Text: "x"}, true)
	if err == nil || !strings.Contains(err.Error(), "error_max_budget_usd") {
		t.Fatalf("err = %v, want is_error surfaced with subtype", err)
	}
}

func TestRunner_AgentMalformedEnvelope(t *testing.T) {
	bin := writeFakeClaude(t, "echo 'not json at all'\n")
	r := newTestRunner(t, bin, testEngine(), nil)
	_, err := r.SearchAgentic(context.Background(), search.SearchEntry{Text: "x"}, true)
	if err == nil || !strings.Contains(err.Error(), "decode claude envelope") {
		t.Fatalf("err = %v, want envelope decode failure", err)
	}
}

func TestRunner_AgentNonZeroExit(t *testing.T) {
	bin := writeFakeClaude(t, "echo 'boom' >&2\nexit 2\n")
	r := newTestRunner(t, bin, testEngine(), nil)
	_, err := r.SearchAgentic(context.Background(), search.SearchEntry{Text: "x"}, true)
	if err == nil || !strings.Contains(err.Error(), "exited 2") || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("err = %v, want exit code + stderr", err)
	}
}

func TestRunner_AgentTimeout(t *testing.T) {
	// `exec sleep` replaces the shell so the context kill lands on the
	// sleeper itself (no grandchild holding the stdout pipe open).
	bin := writeFakeClaude(t, "exec sleep 30\n")
	r := newTestRunner(t, bin, testEngine(), func(o *Options) { o.Timeout = 200 * time.Millisecond })
	_, err := r.SearchAgentic(context.Background(), search.SearchEntry{Text: "x"}, true)
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("err = %v, want timeout", err)
	}
}

func TestRunner_NonAgentSkipsClaude(t *testing.T) {
	// The fake claude exits 3 when invoked — agent=false must never call it.
	bin := writeFakeClaude(t, "exit 3\n")
	engine := testEngine(
		&fakeProvider{name: "ok", hits: []search.Hit{
			{Title: "Hit A", DOI: "10.1/a", Score: 0.8, Source: "ok"},
			{Title: "Hit B", ArxivID: "2401.00001", Score: 0.6, Source: "ok"},
		}},
		&fakeProvider{name: "bad", err: errors.New("backend exploded")},
	)
	r := newTestRunner(t, bin, engine, nil)

	resp, err := r.SearchAgentic(context.Background(), search.SearchEntry{Text: "q", MaxResults: 10}, false)
	if err != nil {
		t.Fatalf("SearchAgentic: %v", err)
	}
	if resp.Conclusion != nil {
		t.Errorf("Conclusion = %v, want nil for agent=false", resp.Conclusion)
	}
	if resp.Usage.LLMTokens != 0 {
		t.Errorf("LLMTokens = %d, want 0 for agent=false", resp.Usage.LLMTokens)
	}
	if len(resp.Hits) != 2 {
		t.Fatalf("len(Hits) = %d, want 2 merged fan-out hits", len(resp.Hits))
	}
	if resp.Hits[0].Title != "Hit A" || resp.Hits[0].Source != "ok" {
		t.Errorf("Hits[0] = %+v", resp.Hits[0])
	}
	if resp.Errors["bad"] != "backend exploded" {
		t.Errorf("Errors = %v, want the failing provider recorded", resp.Errors)
	}

	// Sandbox carries query/results/response/meta but NO prompt.txt or run/.
	sandboxes, _ := os.ReadDir(r.opts.SandboxRoot)
	if len(sandboxes) != 1 {
		t.Fatalf("sandboxes = %v", sandboxes)
	}
	dir := filepath.Join(r.opts.SandboxRoot, sandboxes[0].Name())
	for _, name := range []string{"query.json", "results.json", "response.json", "meta.json"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("sandbox artifact %s missing: %v", name, err)
		}
	}
	for _, name := range []string{"prompt.txt", "run"} {
		if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Errorf("sandbox artifact %s must not exist for agent=false", name)
		}
	}
}

func TestNewRunner_Validation(t *testing.T) {
	if _, err := NewRunner(Options{SandboxRoot: t.TempDir()}); err == nil {
		t.Error("NewRunner without Engine succeeded, want error")
	}
	if _, err := NewRunner(Options{Engine: testEngine()}); err == nil {
		t.Error("NewRunner without SandboxRoot succeeded, want error")
	}
	if _, err := NewRunner(Options{
		Engine: testEngine(), SandboxRoot: t.TempDir(), PromptTemplate: "{{.Bogus",
	}); err == nil {
		t.Error("NewRunner with malformed template succeeded, want error")
	}
}
