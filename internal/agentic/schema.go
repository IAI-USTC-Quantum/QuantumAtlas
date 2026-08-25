package agentic

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/search"
)

// answerSchema is the JSON Schema handed to `claude --json-schema` so the
// CLI itself validates the structured output. Keep it in sync with
// agentAnswer below; the local parser stays defensive regardless (the
// schema is a guardrail, not a guarantee).
const answerSchema = `{
  "type": "object",
  "required": ["conclusion", "hits"],
  "properties": {
    "conclusion": {
      "type": "string",
      "description": "Concise academic conclusion (2-4 sentences) summarizing what the refined hits say about the query; empty string when nothing is relevant."
    },
    "hits": {
      "type": "array",
      "items": {
        "type": "object",
        "required": ["title"],
        "properties": {
          "title":     {"type": "string"},
          "authors":   {"type": "array", "items": {"type": "string"}},
          "year":      {"type": "integer"},
          "doi":       {"type": "string"},
          "arxiv_id":  {"type": "string"},
          "url":       {"type": "string"},
          "venue":     {"type": "string"},
          "citations": {"type": "integer"},
          "score":     {"type": "number", "description": "Relevance 0..1; omit when unknown."}
        },
        "additionalProperties": false
      }
    }
  },
  "additionalProperties": false
}`

// envelope is the `claude -p --output-format json` wrapper. Every field
// is optional on purpose — the CLI's exact shape is not a stable
// contract, so parsing stays defensive and only relies on the documented
// keys (result / usage / is_error / subtype / total_cost_usd).
type envelope struct {
	Result       json.RawMessage `json:"result"`
	IsError      bool            `json:"is_error"`
	Subtype      string          `json:"subtype"`
	TotalCostUSD float64         `json:"total_cost_usd"`
	Usage        struct {
		InputTokens  int64 `json:"input_tokens"`
		OutputTokens int64 `json:"output_tokens"`
	} `json:"usage"`
}

// agentAnswer is the structured payload the prompt asks claude for
// (validated CLI-side against answerSchema).
type agentAnswer struct {
	Conclusion string      `json:"conclusion"`
	Hits       []answerHit `json:"hits"`
}

// answerHit is one refined hit in claude's answer. Score is a pointer so
// an omitted score can default to 0.5 during normalization.
type answerHit struct {
	Title     string   `json:"title"`
	Authors   []string `json:"authors,omitempty"`
	Year      int      `json:"year,omitempty"`
	DOI       string   `json:"doi,omitempty"`
	ArxivID   string   `json:"arxiv_id,omitempty"`
	URL       string   `json:"url,omitempty"`
	Venue     string   `json:"venue,omitempty"`
	Citations int      `json:"citations,omitempty"`
	Score     *float64 `json:"score,omitempty"`
}

// defaultHitScore applies when claude omits a hit's score.
const defaultHitScore = 0.5

// localSource attributes hits refined by the local backend.
const localSource = "agentic-local"

// parseEnvelope decodes one claude JSON envelope into the answer, the
// total LLM token count and the reported cost. result may arrive either
// as a JSON string containing the answer document or as an already-parsed
// object — both are accepted.
func parseEnvelope(raw []byte) (ans *agentAnswer, llmTokens int64, costUSD float64, err error) {
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, 0, 0, fmt.Errorf("agentic local: decode claude envelope: %w", err)
	}
	if env.IsError {
		subtype := env.Subtype
		if subtype == "" {
			subtype = "unknown"
		}
		return nil, 0, env.TotalCostUSD, fmt.Errorf("agentic local: claude reported error (subtype=%s)", subtype)
	}
	if len(env.Result) == 0 {
		return nil, 0, env.TotalCostUSD, fmt.Errorf("agentic local: claude envelope has no result")
	}
	var answer agentAnswer
	var resultText string
	if sErr := json.Unmarshal(env.Result, &resultText); sErr == nil {
		if err := json.Unmarshal([]byte(resultText), &answer); err != nil {
			return nil, 0, env.TotalCostUSD, fmt.Errorf("agentic local: decode claude result string: %w", err)
		}
	} else if err := json.Unmarshal(env.Result, &answer); err != nil {
		return nil, 0, env.TotalCostUSD, fmt.Errorf("agentic local: decode claude result object: %w", err)
	}
	return &answer, env.Usage.InputTokens + env.Usage.OutputTokens, env.TotalCostUSD, nil
}

// normalizeAnswer maps the parsed answer onto the standardized hit list:
// titles trimmed, empty-title hits dropped, hits capped at maxResults,
// missing scores defaulted, source attributed to the local backend. An
// empty conclusion normalizes to nil.
func normalizeAnswer(ans *agentAnswer, maxResults int) (hits []search.RemoteHit, conclusion *string) {
	if maxResults <= 0 {
		maxResults = search.DefaultMaxResults
	}
	for _, h := range ans.Hits {
		if len(hits) >= maxResults {
			break
		}
		title := strings.TrimSpace(h.Title)
		if title == "" {
			continue
		}
		score := defaultHitScore
		if h.Score != nil {
			score = *h.Score
		}
		hits = append(hits, search.RemoteHit{
			Title:     title,
			Authors:   h.Authors,
			Year:      h.Year,
			DOI:       strings.TrimSpace(h.DOI),
			ArxivID:   strings.TrimSpace(h.ArxivID),
			URL:       strings.TrimSpace(h.URL),
			Venue:     strings.TrimSpace(h.Venue),
			Citations: h.Citations,
			Source:    localSource,
			Score:     score,
		})
	}
	if c := strings.TrimSpace(ans.Conclusion); c != "" {
		conclusion = &c
	}
	return hits, conclusion
}

// toRemoteHits maps merged engine hits onto the wire shape, capped at
// maxResults, for the agent=false response and results.json.
func toRemoteHits(hits []search.Hit, maxResults int) []search.RemoteHit {
	if maxResults <= 0 {
		maxResults = search.DefaultMaxResults
	}
	out := make([]search.RemoteHit, 0, min(len(hits), maxResults))
	for _, h := range hits {
		if len(out) >= maxResults {
			break
		}
		out = append(out, search.RemoteHit{
			Title:   h.Title,
			Authors: h.Authors,
			Year:    h.Year,
			DOI:     h.DOI,
			ArxivID: h.ArxivID,
			Source:  h.Source,
			Score:   h.Score,
		})
	}
	return out
}
