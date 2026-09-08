package routes

// search_multi.go: the per-backend ("multi") search surface.
//
//	POST /api/search/multi   — scopeGuard(papers, read). Proxies one
//	                           mode="multi" call to the qatlas-search
//	                           microservice: one raw hit list per
//	                           requested backend, no cross-backend merge
//	                           or ranking (the fused scoring lives on
//	                           POST /api/search and /api/search/agentic).
//	                           The caller's stored third-party keys are
//	                           decrypted and forwarded as api_keys so
//	                           key-requiring backends run under the
//	                           user's own credentials. Identity-anchored
//	                           hits are resolve-or-minted (lazy ingest)
//	                           and backfilled with paper_id / created /
//	                           has_md / status — title-only hits get no
//	                           enrichment. Not metered (same as POST
//	                           /api/search).
//	GET  /api/search/backends — sessionGuard. The backend catalog the
//	                           SPA renders as checkboxes: static table
//	                           merged with the microservice's live
//	                           /v1/backends (server-side availability)
//	                           and the caller's stored keys.
//	                           selectable = server_ready ||
//	                           (user_key && key_configured).

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/registry"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/search"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/userkeys"

	"github.com/casbin/casbin/v2"
	"github.com/pocketbase/pocketbase/core"
)

// MultiBackend is the backend behind POST /api/search/multi and the
// catalog source of GET /api/search/backends: the remote qatlas-search
// microservice (*search.RemoteProvider). The interface keeps the
// handlers testable with a fake, mirroring AgenticBackend.
type MultiBackend interface {
	SearchMulti(ctx context.Context, query string, maxResults int, sources []string, apiKeys map[string]string) (search.RemoteMultiResponse, error)
	ListBackends(ctx context.Context) ([]search.RemoteBackendMeta, error)
}

// compile-time check: the remote microservice client is a backend.
var _ MultiBackend = (*search.RemoteProvider)(nil)

// multiSearchRequest is the POST /api/search/multi body.
type multiSearchRequest struct {
	Text       string   `json:"text"`
	MaxResults int      `json:"max_results"`
	Sources    []string `json:"sources"`
}

// multiSearchResponse forwards the microservice's multi contract plus
// the remote flag (frontends use it to pick the rendering path).
type multiSearchResponse struct {
	Results map[string][]search.RemoteHit `json:"results"`
	Usage   search.RemoteUsage            `json:"usage"`
	Errors  map[string]string             `json:"errors"`
	Remote  bool                          `json:"remote"`
}

// maxMultiSources bounds the requested backend list.
const maxMultiSources = 32

// normalizeSources trims, de-duplicates (first-seen order) and caps.
func normalizeSources(in []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
		if len(out) >= maxMultiSources {
			break
		}
	}
	return out
}

// multiHitAnchor is the registry anchoring one minted identity resolved
// to: the paper_id and whether this search minted the paper.
type multiHitAnchor struct {
	PaperID string
	Created bool
}

// multiIdentityKey reduces a hit to the cross-backend dedup identity —
// DOI first, else the arXiv id, "" for title-only hits. The mint loop
// and the backfill below both key off it, so a raw hit and its mint
// result can never disagree.
func multiIdentityKey(doi, arxivID string) string {
	if doi != "" {
		return doi
	}
	return arxivID
}

// multiAnchors indexes the mint results by identity key. Results with an
// empty paper_id (minting disabled — nil registry) are left out: there
// is nothing to anchor.
func multiAnchors(results []search.Result) map[string]multiHitAnchor {
	anchors := make(map[string]multiHitAnchor, len(results))
	for _, r := range results {
		if r.PaperID == "" {
			continue
		}
		anchors[multiIdentityKey(r.Hit.DOI, r.Hit.ArxivID)] = multiHitAnchor{PaperID: r.PaperID, Created: r.Created}
	}
	return anchors
}

// attachMultiAnchors backfills the server-enriched fields onto every
// backend's raw hits: paper_id / created from the anchors, has_md /
// status from the registry summaries. Title-only hits (no DOI, no arXiv
// id), identities that were not minted and missing summary entries all
// keep the fields omitted — enrichment never fails the response.
func attachMultiAnchors(results map[string][]search.RemoteHit, anchors map[string]multiHitAnchor, summaries map[string]registry.PaperSummary) {
	for _, backendHits := range results {
		for i := range backendHits {
			a, ok := anchors[multiIdentityKey(backendHits[i].DOI, backendHits[i].ArxivID)]
			if !ok {
				continue
			}
			backendHits[i].PaperID = a.PaperID
			backendHits[i].Created = a.Created
			if sum, has := summaries[a.PaperID]; has {
				hasMD := sum.HasMD
				backendHits[i].HasMD = &hasMD
				backendHits[i].Status = sum.Status
			}
		}
	}
}

// RegisterSearchMulti mounts POST /api/search/multi. backend is nil when
// search.remote is disabled — the route stays registered but every call
// answers 503 (same convention as the agentic endpoint).
//
// The minter (search engine) is used for lazy ingestion: after the
// microservice returns per-backend raw hits, identity-anchored results
// (DOI / arXiv ID) are resolve-or-minted into the registry, which fires
// the ingester's OnMint hook → PDF fetch → MinerU conversion. This
// mirrors what POST /api/search and /api/search/agentic already do; the
// mint results are backfilled onto the raw hits (paper_id / created /
// hosting summary) so frontends can link minted hits to the site.
// catalog decorates those hits with the registry hosting summary
// (has_md / status) and may be nil — the fields are then omitted.
func RegisterSearchMulti(se *core.ServeEvent, keys *userkeys.Store, backend MultiBackend, engine *search.Engine, catalog *registry.Store, enforcer *casbin.Enforcer, surveyOptions ...SurveyOptions) {
	RegisterSearchSurvey(se, keys, backend, engine, catalog, enforcer, surveyOptions...)
	se.Router.POST("/api/search/multi", scopeGuard(enforcer, "papers", "read", func(re *core.RequestEvent) error {
		if backend == nil {
			return re.JSON(http.StatusServiceUnavailable, map[string]string{
				"detail": "multi search requires the qatlas-search microservice (search.remote)",
			})
		}
		raw, err := io.ReadAll(io.LimitReader(re.Request.Body, 1<<20))
		if err != nil {
			return re.JSON(http.StatusBadRequest, map[string]string{"detail": "read body: " + err.Error()})
		}
		var req multiSearchRequest
		if err := json.Unmarshal(raw, &req); err != nil {
			return re.JSON(http.StatusBadRequest, map[string]string{"detail": "invalid JSON: " + err.Error()})
		}
		if strings.TrimSpace(req.Text) == "" {
			return re.JSON(http.StatusBadRequest, map[string]string{"detail": "text is required"})
		}
		sources := normalizeSources(req.Sources)
		if len(sources) == 0 {
			return re.JSON(http.StatusBadRequest, map[string]string{"detail": "at least one source backend is required"})
		}

		// Per-user third-party keys, restricted to the requested backends.
		// System PAT callers (re.Auth == nil) search without user keys.
		var apiKeys map[string]string
		if re.Auth != nil && keys != nil && keys.Enabled() {
			userKeys, err := keys.GetKeys(re.Auth.Id)
			if err != nil {
				slog.Error("multi search: load user keys failed", "user_id", re.Auth.Id, "error", err)
				return re.JSON(http.StatusInternalServerError, map[string]string{"detail": "internal server error"})
			}
			for _, src := range sources {
				if k, ok := userKeys[src]; ok {
					if apiKeys == nil {
						apiKeys = map[string]string{}
					}
					apiKeys[src] = k
				}
			}
		}

		resp, err := backend.SearchMulti(re.Request.Context(), req.Text, req.MaxResults, sources, apiKeys)
		if err != nil {
			return re.JSON(http.StatusBadGateway, map[string]string{
				"detail": "multi search upstream failed: " + err.Error(),
			})
		}

		// Lazy ingestion: resolve-or-mint identity-anchored hits so the
		// ingest pipeline (PDF fetch → MinerU) fires for new papers —
		// the same behavior as POST /api/search and /api/search/agentic.
		// Dedup by DOI > arXiv across backends to avoid minting the same
		// paper N times when N backends return it.
		var mintResults []search.Result
		if engine != nil {
			var hits []search.Hit
			seen := map[string]bool{}
			for _, backendHits := range resp.Results {
				for _, rh := range backendHits {
					h := rh.ToHit()
					key := multiIdentityKey(h.DOI, h.ArxivID)
					if key == "" || seen[key] {
						continue // title-only or duplicate
					}
					seen[key] = true
					hits = append(hits, h)
				}
			}
			if len(hits) > 0 {
				// Best-effort: minting failures must not break the
				// search response; the paper stays unminted and the
				// user can still see the raw hit.
				results, _, mintErr := engine.MintHits(re.Request.Context(), hits, len(hits))
				if mintErr != nil {
					slog.Warn("multi search: lazy minting failed", "error", mintErr)
				}
				mintResults = results
			}
		}

		// Server-side enrichment: reflect the anchors back onto every
		// backend's raw hits (paper_id / created from the mint results,
		// has_md / status from the registry summaries — best-effort, a
		// summary miss just omits the fields).
		anchors := multiAnchors(mintResults)
		ids := make([]string, 0, len(anchors))
		for _, a := range anchors {
			if a.PaperID != "" {
				ids = append(ids, a.PaperID)
			}
		}
		attachMultiAnchors(resp.Results, anchors, paperSummaries(re.Request.Context(), catalog, ids))

		out := multiSearchResponse{
			Results: resp.Results,
			Usage:   resp.Usage,
			Errors:  resp.Errors,
			Remote:  true,
		}
		if out.Results == nil {
			out.Results = map[string][]search.RemoteHit{}
		}
		if out.Errors == nil {
			out.Errors = map[string]string{}
		}
		return re.JSON(http.StatusOK, out)
	}))
}

// searchBackendEntry is one row of the /api/search/backends catalog.
type searchBackendEntry struct {
	search.BackendMeta
	ServerReady   bool `json:"server_ready"`
	KeyConfigured bool `json:"key_configured"`
	Selectable    bool `json:"selectable"`
}

// searchBackendsResponse is the GET /api/search/backends wire shape.
type searchBackendsResponse struct {
	Remote   bool                 `json:"remote"`
	KeysOn   bool                 `json:"keys_enabled"`
	Backends []searchBackendEntry `json:"backends"`
}

// backendsCatalogTimeout bounds the live /v1/backends fetch.
const backendsCatalogTimeout = 4 * time.Second

// RegisterSearchBackends mounts GET /api/search/backends (sessionGuard:
// key_configured is user-private, and the catalog only feeds the SPA).
func RegisterSearchBackends(se *core.ServeEvent, keys *userkeys.Store, backend MultiBackend) {
	se.Router.GET("/api/search/backends", sessionGuard(func(re *core.RequestEvent) error {
		out := searchBackendsResponse{Backends: []searchBackendEntry{}}

		keysOn := keys != nil && keys.Enabled()
		out.KeysOn = keysOn
		var configured map[string]bool
		if keysOn && re.Auth != nil {
			userKeys, err := keys.GetKeys(re.Auth.Id)
			if err != nil {
				slog.Error("search backends: load user keys failed", "user_id", re.Auth.Id, "error", err)
				return re.JSON(http.StatusInternalServerError, map[string]string{"detail": "internal server error"})
			}
			configured = make(map[string]bool, len(userKeys))
			for name := range userKeys {
				configured[name] = true
			}
		}

		if backend == nil {
			// search.remote disabled: no multi search at all — empty catalog.
			return re.JSON(http.StatusOK, out)
		}
		out.Remote = true

		ctx, cancel := context.WithTimeout(re.Request.Context(), backendsCatalogTimeout)
		defer cancel()
		live, err := backend.ListBackends(ctx)
		if err != nil {
			// Unreachable microservice: degrade to the static table with
			// server_ready=false (the search page will surface it via the
			// search-remote plugin status anyway).
			slog.Debug("search backends: live catalog unavailable", "error", err)
		}

		// Start from the static table (stable order), overlay live entries;
		// append any live-only backends at the end. When the live fetch
		// failed everything degrades to server_ready=false.
		merged := make([]searchBackendEntry, 0, len(search.BackendCatalog))
		byName := map[string]int{}
		for _, m := range search.BackendCatalog {
			merged = append(merged, searchBackendEntry{BackendMeta: m})
			byName[m.Name] = len(merged) - 1
		}
		for _, lm := range live {
			entry := searchBackendEntry{
				BackendMeta: search.BackendMeta{
					Name:        lm.Name,
					Label:       lm.Label,
					Category:    lm.Category,
					RequiresKey: lm.RequiresKey,
					UserKey:     lm.UserKey,
				},
				ServerReady: lm.Available,
			}
			if i, ok := byName[lm.Name]; ok {
				merged[i] = entry
			} else {
				byName[lm.Name] = len(merged)
				merged = append(merged, entry)
			}
		}

		for i := range merged {
			e := &merged[i]
			if e.Label == "" {
				e.Label = e.Name
			}
			e.KeyConfigured = configured[e.Name]
			e.Selectable = e.ServerReady || (e.UserKey && e.KeyConfigured)
		}
		out.Backends = merged
		return re.JSON(http.StatusOK, out)
	}))
}
