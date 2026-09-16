package search

// The scoring surface is deliberately separate from SearchAgentic: it always
// talks to the remote service and must never silently fall back to local search.
import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
)

const maxScoringResponseBytes = 8 << 20

type ScorerGenerateRequest struct {
	Query        string `json:"query"`
	Requirements string `json:"requirements"`
}

type ScorerGenerateResponse struct {
	Scorer         json.RawMessage `json:"scorer"`
	Summary        string          `json:"summary"`
	Warnings       []string        `json:"warnings"`
	ScorerHash     string          `json:"scorer_hash"`
	FeatureVersion string          `json:"feature_version"`
	Usage          RemoteUsage     `json:"usage"`
}

type RankedSearchRequest struct {
	Text       string          `json:"text"`
	Sources    []string        `json:"sources"`
	MaxResults int             `json:"max_results,omitempty"`
	Scorer     json.RawMessage `json:"scorer"`
	Explain    bool            `json:"explain"`
}

type RankedSearchResponse struct {
	Hits    []RemoteHit       `json:"hits"`
	Ranking json.RawMessage   `json:"ranking"`
	Usage   RemoteUsage       `json:"usage"`
	Errors  map[string]string `json:"errors"`
	Remote  bool              `json:"remote"`
}

// Only the structured, public error envelope is decoded; never relay raw HTTP
// bodies (which may contain reverse-proxy pages, URLs or credentials).
type ScoringErrorDetail struct {
	Code           string         `json:"code"`
	Message        string         `json:"message"`
	Field          string         `json:"field,omitempty"`
	Position       map[string]int `json:"position,omitempty"`
	CandidateIndex *int           `json:"candidate_index,omitempty"`
}

type ScoringRemoteError struct {
	Status int
	Detail ScoringErrorDetail
	Usage  RemoteUsage
}

func (e *ScoringRemoteError) Error() string { return "remote scoring request failed" }

func (p *RemoteProvider) ScoringCapabilities(ctx context.Context) (map[string]any, error) {
	var out map[string]any
	err := p.scoringJSON(ctx, http.MethodGet, "/v1/scoring/capabilities", nil, &out)
	if err == nil && (out == nil || out["language"] != "qatlas-expr-v1") {
		return nil, errors.New("invalid scoring capabilities response")
	}
	return out, err
}

func (p *RemoteProvider) GenerateScorer(ctx context.Context, req ScorerGenerateRequest) (ScorerGenerateResponse, error) {
	var out ScorerGenerateResponse
	err := p.scoringJSON(ctx, http.MethodPost, "/v1/scoring/generate", req, &out)
	if err == nil && (!validGeneratedScorer(out.Scorer) || out.ScorerHash == "" || out.FeatureVersion == "") {
		// Keep any known token usage even when a successful envelope is malformed.
		return out, &ScoringRemoteError{Status: http.StatusBadGateway, Detail: ScoringErrorDetail{Code: "upstream_invalid", Message: "Invalid scoring service response"}, Usage: out.Usage}
	}
	return out, err
}

// Validate the wire shape before the browser renders .filter/.score as strings.
// This is not a second DSL compiler; the service remains the execution authority.
func validGeneratedScorer(raw json.RawMessage) bool {
	if len(raw) == 0 || len(raw) > 4096 {
		return false
	}
	var program struct {
		Language string  `json:"language"`
		Filter   *string `json:"filter"`
		Score    *string `json:"score"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	return decoder.Decode(&program) == nil && program.Language == "qatlas-expr-v1" && program.Filter != nil && strings.TrimSpace(*program.Filter) != "" && program.Score != nil && strings.TrimSpace(*program.Score) != ""
}

func (p *RemoteProvider) SearchRanked(ctx context.Context, req RankedSearchRequest, keys map[string]string) (RankedSearchResponse, error) {
	payload := struct {
		Query      string            `json:"query"`
		Sources    []string          `json:"sources"`
		MaxResults int               `json:"max_results"`
		Scorer     json.RawMessage   `json:"scorer"`
		Explain    bool              `json:"explain"`
		Ranking    string            `json:"ranking"`
		Mode       string            `json:"mode"`
		Agent      bool              `json:"agent"`
		APIKeys    map[string]string `json:"api_keys,omitempty"`
	}{req.Text, req.Sources, req.MaxResults, req.Scorer, req.Explain, "scorer", "fused", false, keys}
	var out RankedSearchResponse
	err := p.scoringJSON(ctx, http.MethodPost, "/v1/search", payload, &out)
	if err == nil {
		// A mixed-version deployment must not turn an ignored scorer into an
		// apparently successful default-ranked response.
		var ranking struct {
			Source string `json:"source"`
		}
		if json.Unmarshal(out.Ranking, &ranking) != nil || ranking.Source != "scorer" || out.Hits == nil {
			return out, errors.New("scoring service did not return custom-ranked hits")
		}
		// Some upstream libraries include query-string API keys in errors.
		// Keep useful source errors while removing credentials we injected.
		secrets := make([]string, 0, len(keys)+1)
		for _, secret := range keys {
			secrets = append(secrets, secret)
		}
		secrets = append(secrets, p.token)
		for source, message := range out.Errors {
			for _, secret := range secrets {
				if secret != "" {
					message = strings.ReplaceAll(message, secret, "[redacted]")
					message = strings.ReplaceAll(message, url.QueryEscape(secret), "[redacted]")
				}
			}
			out.Errors[source] = message
		}
	}
	return out, err
}

func (p *RemoteProvider) scoringJSON(ctx context.Context, method, path string, payload any, out any) error {
	if p == nil || p.baseURL == "" {
		return errors.New("scoring service unavailable")
	}
	var body io.Reader
	if payload != nil {
		raw, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, p.baseURL+path, body)
	if err != nil {
		return errors.New("invalid scoring service URL")
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if p.token != "" {
		req.Header.Set("Authorization", "Bearer "+p.token)
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return errors.New("scoring service transport failed")
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxScoringResponseBytes+1))
	if err != nil || len(raw) > maxScoringResponseBytes {
		return errors.New("invalid scoring service response size")
	}
	if resp.StatusCode != http.StatusOK {
		e := &ScoringRemoteError{Status: http.StatusBadGateway, Detail: ScoringErrorDetail{Code: "upstream_failed", Message: "Scoring service request failed"}}
		// Decode usage independently: a legacy string detail or malformed
		// position must never erase otherwise valid token accounting.
		var envelope struct {
			Detail json.RawMessage `json:"detail"`
			Usage  RemoteUsage     `json:"usage"`
		}
		var detail ScoringErrorDetail
		validDetail := false
		if json.Unmarshal(raw, &envelope) == nil {
			e.Usage = envelope.Usage
			validDetail = json.Unmarshal(envelope.Detail, &detail) == nil && detail.Code != "" && detail.Message != "" && len(detail.Message) <= 8192
		}
		// Upstream authentication is a deployment issue, not the user's session.
		switch resp.StatusCode {
		case 400, 413, 422, 429, 503:
			e.Status = resp.StatusCode
			if validDetail {
				e.Detail = detail
			}
		case 502, 504:
			// Only documented generator errors may cross a failed gateway;
			// arbitrary proxy/debug responses are replaced by the safe default.
			if validDetail {
				switch detail.Code {
				case "model_error", "model_unavailable", "model_response_invalid", "model_response_too_large", "generation_failed", "generation_timeout":
					e.Status, e.Detail = resp.StatusCode, detail
				}
			}
		case 404:
			e.Status = http.StatusServiceUnavailable
			e.Detail = ScoringErrorDetail{Code: "unsupported_service", Message: "Upgrade the search service to enable scoring"}
		}
		return e
	}
	if err := json.Unmarshal(raw, out); err != nil {
		var envelope struct {
			Usage RemoteUsage `json:"usage"`
		}
		_ = json.Unmarshal(raw, &envelope)
		return &ScoringRemoteError{Status: http.StatusBadGateway, Detail: ScoringErrorDetail{Code: "upstream_invalid", Message: "Invalid scoring service response"}, Usage: envelope.Usage}
	}
	return nil
}
