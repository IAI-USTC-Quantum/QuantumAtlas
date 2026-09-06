package downloader

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// LinkExtractor is the agent-fallback interface: given the landing-page
// evidence, propose candidate direct-PDF URLs. Two implementations
// ship: the OpenAI-compatible HTTP client (AgentConfig.Backend
// "openai") and the local claude CLI runner (Backend "claude"). Every
// proposal still goes through the validation pipeline — the agent only
// nominates, never decides.
type LinkExtractor interface {
	Name() string
	Extract(ctx context.Context, input ExtractInput) ([]string, error)
}

// ExtractInput is the evidence handed to a LinkExtractor.
type ExtractInput struct {
	DOI        string
	LandingURL string
	HTML       []byte // bounded landing-page body (may be empty)
}

// AgentConfig configures the fallback link extractor. Backend selects
// the implementation: "" / "off" disables the agent strategy; "openai"
// uses BaseURL/APIKey/Model against an OpenAI-compatible
// /chat/completions endpoint; "claude" drives the local claude CLI
// headlessly.
type AgentConfig struct {
	Backend string
	// OpenAI-compatible endpoint settings.
	BaseURL   string
	APIKey    string
	Model     string
	MaxTokens int
	// Claude CLI settings.
	ClaudeBin    string
	ClaudeModel  string
	Timeout      time.Duration
	MaxBudgetUSD float64
}

// NewLinkExtractor builds the configured extractor (nil when disabled
// or misconfigured — the ladder then simply ends after the landing
// strategy).
func NewLinkExtractor(cfg AgentConfig) LinkExtractor {
	switch strings.ToLower(strings.TrimSpace(cfg.Backend)) {
	case "", "off", "none", "false":
		return nil
	case "openai", "openai-compatible", "llm":
		if cfg.BaseURL == "" || cfg.APIKey == "" || cfg.Model == "" {
			return nil
		}
		return &OpenAIExtractor{cfg: cfg}
	case "claude", "claude-cli":
		return &ClaudeExtractor{cfg: cfg}
	}
	return nil
}

// agentSystemPrompt is shared by both implementations.
const agentSystemPrompt = `You are a link-extraction assistant for an academic paper downloader with legitimate institutional access. Given the HTML of a publisher article landing page (and its URL), identify direct PDF download URLs.

Rules:
- Return ONLY absolute http(s) URLs that serve the full-text PDF of THIS article.
- Prefer canonical direct-download links (citation_pdf_url, "PDF" buttons, stamp/getPDF endpoints) over viewer pages.
- Do NOT return landing pages, TOC pages, supplementary files, or links to other articles.
- If no plausible PDF URL exists in the page, return an empty list.
Answer with a JSON object: {"candidates": ["https://...", ...]} with at most 5 URLs ordered by confidence.`

// buildAgentUserPrompt renders the evidence for the model.
func buildAgentUserPrompt(input ExtractInput) string {
	html := string(input.HTML)
	const maxHTML = 120 * 1024
	if len(html) > maxHTML {
		html = html[:maxHTML]
	}
	var b strings.Builder
	b.WriteString("Article DOI: ")
	b.WriteString(input.DOI)
	b.WriteString("\nLanding page URL: ")
	b.WriteString(input.LandingURL)
	b.WriteString("\n\nLanding page HTML (truncated):\n```\n")
	b.WriteString(html)
	b.WriteString("\n```\n")
	return b.String()
}

// parseAgentAnswer extracts the candidates JSON from an LLM answer,
// tolerating markdown code fences and prose wrappers.
func parseAgentAnswer(answer string) []string {
	answer = strings.TrimSpace(answer)
	if i := strings.Index(answer, "{"); i > 0 {
		answer = answer[i:]
	}
	if i := strings.LastIndex(answer, "}"); i >= 0 {
		answer = answer[:i+1]
	}
	var parsed struct {
		Candidates []string `json:"candidates"`
	}
	if err := json.Unmarshal([]byte(answer), &parsed); err != nil {
		return nil
	}
	var out []string
	for _, c := range parsed.Candidates {
		c = strings.TrimSpace(c)
		if strings.HasPrefix(c, "http://") || strings.HasPrefix(c, "https://") {
			out = append(out, c)
		}
	}
	return out
}

// OpenAIExtractor talks to an OpenAI-compatible /chat/completions
// endpoint (the same shape qatlas-search's agent uses).
type OpenAIExtractor struct {
	cfg AgentConfig
}

func (o *OpenAIExtractor) Name() string { return "agent:openai" }

type chatRequest struct {
	Model       string        `json:"model"`
	Messages    []chatMessage `json:"messages"`
	MaxTokens   int           `json:"max_tokens,omitempty"`
	Temperature float64       `json:"temperature"`
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
}

func (o *OpenAIExtractor) Extract(ctx context.Context, input ExtractInput) ([]string, error) {
	maxTokens := o.cfg.MaxTokens
	if maxTokens == 0 {
		maxTokens = 1024
	}
	timeout := o.cfg.Timeout
	if timeout == 0 {
		timeout = 60 * time.Second
	}
	body, err := json.Marshal(chatRequest{
		Model: o.cfg.Model,
		Messages: []chatMessage{
			{Role: "system", Content: agentSystemPrompt},
			{Role: "user", Content: buildAgentUserPrompt(input)},
		},
		MaxTokens:   maxTokens,
		Temperature: 0,
	})
	if err != nil {
		return nil, err
	}
	base := strings.TrimRight(o.cfg.BaseURL, "/")
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+o.cfg.APIKey)
	client := &http.Client{Timeout: timeout}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("openai extractor: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("openai extractor: http %d: %.200s", resp.StatusCode, raw)
	}
	var out chatResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	if len(out.Choices) == 0 {
		return nil, fmt.Errorf("openai extractor: empty choices")
	}
	return parseAgentAnswer(out.Choices[0].Message.Content), nil
}
