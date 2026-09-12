package routes

// papers_match.go: the paper identity matching surface.
//
//	POST /api/papers/match — scopeGuard(papers, read). Proxies one match
//	                           query to the qatlas-match microservice:
//	                           given qatlas-ids / DOIs / arXiv ids /
//	                           OpenAlex ids / paper URLs / titles, decide
//	                           whether each work is already in the qatlas
//	                           registry and return the unified qa_… id.
//	                           Precision-first rules live in the
//	                           microservice (strict normalization; titles
//	                           only match when every word matches) —
//	                           qatlasd owns auth, the microservice only
//	                           sees network-internal callers. Not metered
//	                           (same as the other papers read paths).

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/match"

	"github.com/casbin/casbin/v2"
	"github.com/pocketbase/pocketbase/core"
)

// MatchBackend is the backend behind POST /api/papers/match: the remote
// qatlas-match microservice (*match.RemoteClient). The interface keeps
// the handler testable with a fake, mirroring MultiBackend.
type MatchBackend interface {
	Match(ctx context.Context, q match.Query) (match.MatchResponse, error)
}

// compile-time check: the remote microservice client is a backend.
var _ MatchBackend = (*match.RemoteClient)(nil)

// paperMatchRequest is the POST /api/papers/match body (same shape as
// the microservice's /v1/match contract).
type paperMatchRequest struct {
	Inputs     []string `json:"inputs"`
	DOI        string   `json:"doi"`
	ArxivID    string   `json:"arxiv_id"`
	OpenAlexID string   `json:"openalex_id"`
	QatlasID   string   `json:"qatlas_id"`
	URL        string   `json:"url"`
	Title      string   `json:"title"`
	Author     string   `json:"author"`
	Year       int      `json:"year"`
}

const (
	// maxMatchInputs bounds the free-form input list (mirrors the
	// microservice's own cap).
	maxMatchInputs = 50

	// maxMatchBody bounds the request body before parsing.
	maxMatchBody = 1 << 16
)

// normalizeMatchInputs trims, drops empties, de-dupes (first-seen order)
// and caps the free-form inputs.
func normalizeMatchInputs(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
		if len(out) >= maxMatchInputs {
			break
		}
	}
	return out
}

// RegisterPaperMatch mounts POST /api/papers/match. backend is nil when
// match.remote is disabled — the route stays registered but every call
// answers 503 (same convention as the multi-search endpoint).
func RegisterPaperMatch(se *core.ServeEvent, backend MatchBackend, enforcer *casbin.Enforcer) {
	se.Router.POST("/api/papers/match", scopeGuard(enforcer, "papers", "read", func(re *core.RequestEvent) error {
		if backend == nil {
			return re.JSON(http.StatusServiceUnavailable, map[string]string{
				"detail": "match requires the qatlas-match microservice (match.remote)",
			})
		}
		raw, err := io.ReadAll(io.LimitReader(re.Request.Body, maxMatchBody))
		if err != nil {
			return re.JSON(http.StatusBadRequest, map[string]string{"detail": "read body: " + err.Error()})
		}
		var req paperMatchRequest
		if err := json.Unmarshal(raw, &req); err != nil {
			return re.JSON(http.StatusBadRequest, map[string]string{"detail": "invalid JSON: " + err.Error()})
		}

		query := match.Query{
			Inputs:     normalizeMatchInputs(req.Inputs),
			DOI:        strings.TrimSpace(req.DOI),
			ArxivID:    strings.TrimSpace(req.ArxivID),
			OpenAlexID: strings.TrimSpace(req.OpenAlexID),
			QatlasID:   strings.TrimSpace(req.QatlasID),
			URL:        strings.TrimSpace(req.URL),
			Title:      strings.TrimSpace(req.Title),
			Author:     strings.TrimSpace(req.Author),
			Year:       req.Year,
		}
		if len(query.Inputs) == 0 && query.DOI == "" && query.ArxivID == "" &&
			query.OpenAlexID == "" && query.QatlasID == "" && query.URL == "" && query.Title == "" {
			return re.JSON(http.StatusBadRequest, map[string]string{
				"detail": "nothing to match: provide inputs or a typed field (doi/arxiv_id/openalex_id/qatlas_id/url/title)",
			})
		}

		resp, err := backend.Match(re.Request.Context(), query)
		if err != nil {
			return re.JSON(http.StatusBadGateway, map[string]string{
				"detail": "match upstream failed: " + err.Error(),
			})
		}
		if resp.Results == nil {
			resp.Results = []match.MatchResult{}
		}
		return re.JSON(http.StatusOK, resp)
	}))
}
