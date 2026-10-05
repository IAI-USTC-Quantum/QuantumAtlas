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
	"errors"
	"io"
	"net"
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
			status := http.StatusBadGateway
			body := map[string]any{
				"code":   "match_upstream_failed",
				"detail": "match upstream request failed; no match result is available",
			}
			var networkErr net.Error
			var httpErr *match.HTTPError
			switch {
			case errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &networkErr) && networkErr.Timeout()):
				status = http.StatusGatewayTimeout
				body["code"] = "match_upstream_timeout"
				body["detail"] = "match upstream timed out; no match result is available"
			case errors.As(err, &httpErr):
				body["code"] = "match_upstream_http_error"
				body["detail"] = httpErr.Error()
				body["upstream_status"] = httpErr.Status
			case errors.Is(err, match.ErrInvalidResponse):
				body["code"] = "match_upstream_invalid_response"
				body["detail"] = "match upstream returned an invalid response; no match result is available"
			case errors.As(err, &networkErr):
				body["code"] = "match_upstream_transport_error"
				body["detail"] = "could not communicate with match upstream; no match result is available"
			}
			// Log only the bounded classification: do not copy service URLs,
			// DB errors, credentials or user query text into public diagnostics.
			re.App.Logger().Warn("paper match upstream failure", "code", body["code"], "status", status, "input_count", len(query.Inputs), "upstream_status", body["upstream_status"])
			return re.JSON(status, body)
		}
		if resp.Results == nil {
			resp.Results = []match.MatchResult{}
		}
		return re.JSON(http.StatusOK, resp)
	}))
}
