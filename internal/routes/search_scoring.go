package routes

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/config"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/registry"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/search"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/usage"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/userkeys"
	"github.com/casbin/casbin/v2"
	"github.com/pocketbase/pocketbase/core"
)

type ScoringBackend interface {
	ScoringCapabilities(context.Context) (map[string]any, error)
	GenerateScorer(context.Context, search.ScorerGenerateRequest) (search.ScorerGenerateResponse, error)
	SearchRanked(context.Context, search.RankedSearchRequest, map[string]string) (search.RankedSearchResponse, error)
}

// A small interface makes quota/refund/token semantics testable without a DB.
type ScoringMeter interface {
	EffectiveLimit(context.Context, string, int) (int, error)
	CheckAndReserve(context.Context, string, string, int) (int, bool, error)
	Refund(context.Context, string, string) error
	RecordTokens(context.Context, string, string, int64) error
}

var _ ScoringBackend = (*search.RemoteProvider)(nil)
var _ ScoringMeter = (*usage.Store)(nil)

// Admission is nonblocking, bounded and process-local. Active user entries are
// removed on release; rate windows expire and their map has a hard size cap.
// Deployment-wide limits belong at the ingress.
type scoringRateWindow struct {
	start time.Time
	count int
}
type scoringAdmission struct {
	mu                               sync.Mutex
	active                           int
	users                            map[string]int
	totalLimit, userLimit, perMinute int
	windows                          map[string]scoringRateWindow
}

func (a *scoringAdmission) acquire(user string) (func(), bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.active >= a.totalLimit || a.users[user] >= a.userLimit {
		return nil, false
	}
	if a.perMinute > 0 {
		now := time.Now()
		if a.windows == nil {
			a.windows = make(map[string]scoringRateWindow)
		}
		window, exists := a.windows[user]
		if !exists && len(a.windows) >= 4096 {
			for id, old := range a.windows {
				if now.Sub(old.start) >= time.Minute {
					delete(a.windows, id)
				}
			}
			if len(a.windows) >= 4096 {
				return nil, false
			}
		}
		if now.Sub(window.start) >= time.Minute {
			window = scoringRateWindow{start: now}
		}
		if window.count >= a.perMinute {
			return nil, false
		}
		window.count++
		a.windows[user] = window
	}
	if a.users == nil {
		a.users = make(map[string]int)
	}
	a.active++
	a.users[user]++
	return func() {
		a.mu.Lock()
		defer a.mu.Unlock()
		a.active--
		a.users[user]--
		if a.users[user] == 0 {
			delete(a.users, user)
		}
	}, true
}

const maxScoringBody = 64 << 10

func scoringProblem(re *core.RequestEvent, status int, code, message string) error {
	return re.JSON(status, map[string]any{"detail": search.ScoringErrorDetail{Code: code, Message: message}})
}

func scoringBody(re *core.RequestEvent, dst any) (int, error) {
	// PocketBase bodies rewind on EOF; decode the bounded copy, not the original.
	raw, err := io.ReadAll(io.LimitReader(re.Request.Body, maxScoringBody+1))
	if err != nil {
		return http.StatusBadRequest, err
	}
	if len(raw) > maxScoringBody {
		return http.StatusRequestEntityTooLarge, errors.New("body too large")
	}
	if !utf8.Valid(raw) {
		return http.StatusBadRequest, errors.New("invalid UTF-8")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		return http.StatusBadRequest, err
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return http.StatusBadRequest, errors.New("trailing JSON")
	}
	if _, ranked := dst.(*search.RankedSearchRequest); ranked {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(raw, &fields); err != nil {
			return http.StatusBadRequest, err
		}
		if value, supplied := fields["explain"]; supplied {
			value = bytes.TrimSpace(value)
			if !bytes.Equal(value, []byte("true")) && !bytes.Equal(value, []byte("false")) {
				return http.StatusUnprocessableEntity, errors.New("explain must be a boolean")
			}
		}
	}
	return 0, nil
}

func scoringBodyError(re *core.RequestEvent, status int) error {
	if status == http.StatusRequestEntityTooLarge {
		return scoringProblem(re, status, "request_too_large", "Request body exceeds 64 KiB")
	}
	return scoringProblem(re, status, "invalid_request", "Invalid scoring request")
}

func scoringUpstreamError(re *core.RequestEvent, err error, usageJSON any) error {
	status := http.StatusBadGateway
	detail := search.ScoringErrorDetail{Code: "upstream_failed", Message: "Scoring service request failed"}
	var upstream *search.ScoringRemoteError
	if errors.As(err, &upstream) {
		status, detail = upstream.Status, upstream.Detail
	}
	body := map[string]any{"detail": detail}
	if usageJSON != nil {
		body["usage"] = usageJSON
	}
	if status == http.StatusServiceUnavailable || status == http.StatusTooManyRequests {
		re.Response.Header().Set("Retry-After", "2")
	}
	return re.JSON(status, body)
}

func scoringUser(re *core.RequestEvent) string {
	if re.Auth != nil {
		return re.Auth.Id
	}
	return "system-pat"
}

func scoringBusy(re *core.RequestEvent) error {
	re.Response.Header().Set("Retry-After", "2")
	return scoringProblem(re, http.StatusTooManyRequests, "busy", "Too many scoring requests in flight; retry later")
}

// RegisterSearchScoring preserves the old multi/agentic APIs and exposes a
// separate remote-only, confirmed-scorer path. No LLM runs in /ranked.
func RegisterSearchScoring(se *core.ServeEvent, cfg *config.Config, keys *userkeys.Store, backend ScoringBackend, meter ScoringMeter, engine *search.Engine, catalog *registry.Store, enforcer *casbin.Enforcer) {
	generateGate := &scoringAdmission{totalLimit: 8, userLimit: 1, perMinute: 10}
	rankedGate := &scoringAdmission{totalLimit: 16, userLimit: 2, perMinute: 30}
	se.Router.GET("/api/search/scoring/capabilities", sessionGuard(func(re *core.RequestEvent) error {
		if backend == nil {
			return scoringProblem(re, 503, "unavailable", "Scoring requires the remote search service")
		}
		ctx, cancel := context.WithTimeout(re.Request.Context(), 5*time.Second)
		defer cancel()
		out, err := backend.ScoringCapabilities(ctx)
		if err != nil {
			return scoringUpstreamError(re, err, nil)
		}
		if meter == nil {
			out["generation_available"] = false
		}
		return re.JSON(http.StatusOK, out)
	}))
	se.Router.POST("/api/search/scoring/generate", scopeGuard(enforcer, "papers", "read", func(re *core.RequestEvent) error {
		if backend == nil {
			return scoringProblem(re, 503, "unavailable", "Scoring requires the remote search service")
		}
		if re.Auth == nil {
			return scoringProblem(re, 403, "user_required", "Scorer generation requires a user-bound credential")
		}
		release, ok := generateGate.acquire(scoringUser(re))
		if !ok {
			return scoringBusy(re)
		}
		defer release()
		var req search.ScorerGenerateRequest
		if status, err := scoringBody(re, &req); err != nil {
			return scoringBodyError(re, status)
		}
		req.Query, req.Requirements = strings.TrimSpace(req.Query), strings.TrimSpace(req.Requirements)
		if req.Query == "" || req.Requirements == "" || utf8.RuneCountInString(req.Query) > 4000 || utf8.RuneCountInString(req.Requirements) > 4000 {
			return scoringProblem(re, 400, "invalid_request", "query and requirements must each contain 1–4000 characters")
		}
		if meter == nil {
			return scoringProblem(re, 503, "metering_unavailable", "Generation metering is unavailable")
		}
		// Covers the service's maximum 120s generation deadline plus transport
		// margin. The configured remote client timeout still takes precedence.
		ctx, cancel := context.WithTimeout(re.Request.Context(), 150*time.Second)
		defer cancel()
		defaultLimit := 0
		if cfg != nil {
			defaultLimit = cfg.AgenticDailyLimit
		}
		limit, err := meter.EffectiveLimit(ctx, re.Auth.Id, defaultLimit)
		if err != nil {
			return agenticStoreError(re, err)
		}
		// The shared Store's insert path historically admits a first call at zero;
		// explicitly reject zero/negative allowances before attempting a reservation.
		if limit <= 0 {
			return re.JSON(429, map[string]any{"detail": search.ScoringErrorDetail{Code: "quota_exceeded", Message: "Daily AI generation/search limit reached"}, "usage": agenticUsageJSON{Limit: limit}})
		}
		today, allowed, err := meter.CheckAndReserve(ctx, re.Auth.Id, usage.MetricAgenticSearch, limit)
		if err != nil {
			return agenticStoreError(re, err)
		}
		if !allowed {
			return re.JSON(429, map[string]any{"detail": search.ScoringErrorDetail{Code: "quota_exceeded", Message: "Daily AI generation/search limit reached"}, "usage": agenticUsageJSON{Today: today, Limit: limit}})
		}
		out, upstreamErr := backend.GenerateScorer(ctx, req)
		tokens := out.Usage.LLMTokens
		var remoteErr *search.ScoringRemoteError
		if errors.As(upstreamErr, &remoteErr) && remoteErr.Usage.LLMTokens > tokens {
			tokens = remoteErr.Usage.LLMTokens
		}
		if tokens < 0 {
			tokens = 0
		}
		// Browser disconnect must not prevent accounting or refunds. Keep cleanup
		// bounded rather than using an already-cancelled HTTP request context.
		if tokens > 0 {
			accountingCtx, accountingCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			if err := meter.RecordTokens(accountingCtx, re.Auth.Id, usage.MetricAgenticSearch, tokens); err != nil {
				slog.Error("scorer generation: record tokens failed", "user_id", re.Auth.Id)
			}
			accountingCancel()
		}
		if upstreamErr != nil {
			// Independent deadline: a slow token write must not consume the
			// refund's entire cleanup budget.
			refundCtx, refundCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			if err := meter.Refund(refundCtx, re.Auth.Id, usage.MetricAgenticSearch); err != nil {
				slog.Error("scorer generation: refund failed", "user_id", re.Auth.Id)
			} else if today > 0 {
				today--
			}
			refundCancel()
			return scoringUpstreamError(re, upstreamErr, agenticUsageJSON{Today: today, Limit: limit, LLMTokens: tokens})
		}
		if out.Warnings == nil {
			out.Warnings = []string{}
		}
		return re.JSON(200, struct {
			search.ScorerGenerateResponse
			Usage agenticUsageJSON `json:"usage"`
		}{out, agenticUsageJSON{Today: today, Limit: limit, LLMTokens: tokens}})
	}))
	se.Router.POST("/api/search/ranked", scopeGuard(enforcer, "papers", "read", func(re *core.RequestEvent) error {
		if backend == nil {
			return scoringProblem(re, 503, "unavailable", "Scoring requires the remote search service")
		}
		release, ok := rankedGate.acquire(scoringUser(re))
		if !ok {
			return scoringBusy(re)
		}
		defer release()
		var req search.RankedSearchRequest
		if status, err := scoringBody(re, &req); err != nil {
			return scoringBodyError(re, status)
		}
		req.Text = strings.TrimSpace(req.Text)
		if req.Text == "" || utf8.RuneCountInString(req.Text) > 4000 || len(req.Sources) == 0 || len(req.Sources) > maxMultiSources || req.MaxResults < 0 || req.MaxResults > 100 {
			return scoringProblem(re, 400, "invalid_request", "text, 1–32 sources and max_results between 1 and 100 are required")
		}
		for _, source := range req.Sources {
			if strings.TrimSpace(source) == "" || len(source) > 100 {
				return scoringProblem(re, 400, "invalid_sources", "Invalid search source")
			}
		}
		req.Sources = normalizeSources(req.Sources)
		if req.MaxResults == 0 {
			req.MaxResults = search.DefaultMaxResults
		}
		program := bytes.TrimSpace(req.Scorer)
		if len(program) == 0 || program[0] != '{' || len(program) > 4096 {
			return scoringProblem(re, 422, "invalid_scorer", "scorer must be an object of at most 4096 bytes")
		}
		var apiKeys map[string]string
		if re.Auth != nil && keys != nil && keys.Enabled() {
			available, err := keys.GetKeys(re.Auth.Id)
			if err != nil {
				return scoringProblem(re, 500, "keys_unavailable", "Cannot load search keys")
			}
			apiKeys = make(map[string]string)
			for _, source := range req.Sources {
				if key := available[source]; key != "" {
					apiKeys[source] = key
				}
			}
		}
		ctx, cancel := context.WithTimeout(re.Request.Context(), 90*time.Second)
		defer cancel()
		out, err := backend.SearchRanked(ctx, req, apiKeys)
		if err != nil {
			return scoringUpstreamError(re, err, nil)
		}
		if len(out.Hits) > req.MaxResults {
			return scoringProblem(re, 502, "upstream_invalid", "Scoring service exceeded the requested result limit")
		}
		// Do not split results/candidates: attaching registry identity must preserve
		// every hit's custom score, explanation and exact global position.
		if engine != nil {
			hits := []search.Hit{}
			for i := range out.Hits {
				out.Hits[i].PaperID, out.Hits[i].Created, out.Hits[i].HasMD, out.Hits[i].Status = "", false, nil, ""
				if out.Hits[i].DOI != "" || out.Hits[i].ArxivID != "" {
					hits = append(hits, out.Hits[i].ToHit())
				}
			}
			if len(hits) > 0 {
				results, _, mintErr := engine.MintHits(ctx, hits, len(hits))
				if mintErr != nil {
					slog.Warn("ranked search: registry enrichment failed")
				}
				anchors := multiAnchors(results)
				ids := make([]string, 0, len(anchors))
				for _, anchor := range anchors {
					ids = append(ids, anchor.PaperID)
				}
				attachMultiAnchors(map[string][]search.RemoteHit{"ranked": out.Hits}, anchors, paperSummaries(ctx, catalog, ids))
			}
		}
		if out.Hits == nil {
			out.Hits = []search.RemoteHit{}
		}
		if out.Errors == nil {
			out.Errors = map[string]string{}
		}
		out.Remote = true
		return re.JSON(200, out)
	}))
}
