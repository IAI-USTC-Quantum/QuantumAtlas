package routes

// search.go: the multi-provider paper search surface (POST /api/search)
// plus the registry paper-detail endpoint (GET /api/papers/{paper_id}).
//
// POST /api/search takes a search.SearchEntry JSON body, fans it out to
// the configured providers (catalog / arxiv / openalex / remote) via the
// search.Engine, and returns the engine's merged response: results are
// identity-anchored hits with their registry paper_id (newly minted
// papers carry created=true and are picked up by the lazy-ingestion
// pipeline), candidates are title-only hits that were NOT minted.

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/registry"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/search"

	"github.com/casbin/casbin/v2"
	"github.com/pocketbase/pocketbase/core"
)

// searchResultJSON is the wire shape of one engine Result.
type searchResultJSON struct {
	PaperID string     `json:"paper_id"`
	Hit     search.Hit `json:"hit"`
	Created bool       `json:"created"`
}

// searchResponseJSON is the wire shape of one engine Response.
type searchResponseJSON struct {
	Results    []searchResultJSON `json:"results"`
	Candidates []search.Hit       `json:"candidates"`
}

// RegisterSearch mounts POST /api/search, gated by the papers:read
// scope (search resolves against the paper registry, the same resource
// the other papers:read endpoints expose).
func RegisterSearch(se *core.ServeEvent, engine *search.Engine, enforcer *casbin.Enforcer) {
	se.Router.POST("/api/search", scopeGuard(enforcer, "papers", "read", func(re *core.RequestEvent) error {
		raw, err := io.ReadAll(io.LimitReader(re.Request.Body, 1<<20))
		if err != nil {
			return re.JSON(http.StatusBadRequest, map[string]string{
				"detail": "read search entry body: " + err.Error(),
			})
		}
		var entry search.SearchEntry
		if err := json.Unmarshal(raw, &entry); err != nil {
			return re.JSON(http.StatusBadRequest, map[string]string{
				"detail": "invalid search entry JSON: " + err.Error(),
			})
		}
		resp, err := engine.Search(re.Request.Context(), entry)
		if err != nil {
			if errors.Is(err, registry.ErrCatalogUnavailable) {
				return re.JSON(http.StatusServiceUnavailable, map[string]string{
					"detail": "catalog unavailable (PostgreSQL unreachable); retry shortly",
				})
			}
			return re.JSON(http.StatusInternalServerError, map[string]string{"detail": err.Error()})
		}
		out := searchResponseJSON{
			Results:    make([]searchResultJSON, 0, len(resp.Results)),
			Candidates: resp.Candidates,
		}
		if out.Candidates == nil {
			out.Candidates = []search.Hit{}
		}
		for _, r := range resp.Results {
			out.Results = append(out.Results, searchResultJSON{
				PaperID: r.PaperID,
				Hit:     r.Hit,
				Created: r.Created,
			})
		}
		return re.JSON(http.StatusOK, out)
	}))
}

// paperDetailHandler answers GET /api/papers/{paper_id} (dispatched from
// the /api/papers/{path...} catch-all for "qa_"-prefixed single-segment
// paths): the registry paper — status, identities, and its assets.
func paperDetailHandler(re *core.RequestEvent, catalog *registry.Store, paperID string) error {
	ctx := re.Request.Context()
	detail, found, err := catalog.GetWithAssets(ctx, paperID)
	if err != nil {
		if errors.Is(err, registry.ErrCatalogUnavailable) {
			return re.JSON(http.StatusServiceUnavailable, map[string]string{
				"detail": "catalog unavailable (PostgreSQL unreachable); retry shortly",
			})
		}
		return re.JSON(http.StatusInternalServerError, map[string]string{"detail": err.Error()})
	}
	if !found {
		return re.JSON(http.StatusNotFound, map[string]string{
			"detail": "no such paper: " + paperID,
		})
	}
	p := detail.Paper
	assets := make([]map[string]any, 0, len(detail.Assets))
	for _, a := range detail.Assets {
		entry := map[string]any{
			"asset_id":         a.AssetID,
			"source":           a.Source,
			"pdf_path":         a.PDFPath,
			"pdf_size":         a.PDFSize,
			"pdf_sha256":       a.PDFSha256,
			"mineru_md_path":   a.MinerUMDPath,
			"mineru_json_path": a.MinerUJSONPath,
			"image_count":      a.ImageCount,
			"fetched_at":       a.FetchedAt.UTC().Format(time.RFC3339),
		}
		if a.Source == "arxiv" {
			entry["arxiv_version"] = a.ArxivVersion
		}
		if a.LeaseID != "" {
			entry["lease_id"] = a.LeaseID
			entry["lease_holder"] = a.LeaseHolder
			if a.LeaseExpiresAt != nil {
				entry["lease_expires_at"] = a.LeaseExpiresAt.UTC().Format(time.RFC3339)
			}
		}
		assets = append(assets, entry)
	}
	return re.JSON(http.StatusOK, map[string]any{
		"paper_id":    p.PaperID,
		"status":      p.Status,
		"arxiv_id":    p.ArxivID,
		"doi":         p.DOI,
		"openalex_id": p.OpenAlexID,
		"paper_ref":   p.PaperRef,
		"title":       p.Title,
		"authors":     p.Authors,
		"created_at":  p.CreatedAt.UTC().Format(time.RFC3339),
		"updated_at":  p.UpdatedAt.UTC().Format(time.RFC3339),
		"assets":      assets,
	})
}
