package routes

// search.go: the multi-provider paper search surface (POST /api/search)
// plus the registry paper-detail endpoint (GET /api/papers/{paper_id}).
//
// POST /api/search takes a search.SearchEntry JSON body, fans it out to
// the configured providers (catalog / arxiv / openalex / remote) via the
// search.Engine, and returns the engine's merged response: results are
// identity-anchored hits with their registry paper_id (newly minted
// papers carry created=true and are picked up by the lazy-ingestion
// pipeline), candidates are title-only hits that were NOT minted. An
// entry may carry an identity (arxiv_id / doi) instead of free text —
// identity-only entries are forwarded to the remote provider as
// identity fields; entries with none of the four inputs 400. Each
// minted result also carries its hosting summary (has_md / has_pdf /
// status from the registry default asset, omitted when unavailable).

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/ingest"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/mineru"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/registry"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/search"

	"github.com/casbin/casbin/v2"
	"github.com/pocketbase/pocketbase/core"
)

// searchResultJSON is the wire shape of one engine Result. The hosting
// summary fields (has_md / has_pdf / status, read off the paper's
// default asset) are pointers/omitempty: they are omitted when the
// registry is unavailable or the hit was not minted, so the fields
// degrade silently instead of failing the search response.
type searchResultJSON struct {
	PaperID string     `json:"paper_id"`
	Hit     search.Hit `json:"hit"`
	Created bool       `json:"created"`
	HasMD   *bool      `json:"has_md,omitempty"`
	HasPDF  *bool      `json:"has_pdf,omitempty"`
	Status  string     `json:"status,omitempty"`
}

// resultSummaries fetches the registry hosting summaries for the minted
// results. Any failure (nil catalog, registry down) degrades to nil —
// hosting hints must never fail the search response; callers then omit
// the fields.
func resultSummaries(ctx context.Context, catalog *registry.Store, results []search.Result) map[string]registry.PaperSummary {
	ids := make([]string, 0, len(results))
	for _, r := range results {
		if r.PaperID != "" {
			ids = append(ids, r.PaperID)
		}
	}
	return paperSummaries(ctx, catalog, ids)
}

// paperSummaries is the batch fetch under resultSummaries, shared with
// the multi search surface (which anchors raw per-backend hits instead
// of engine Results). Same degradation contract: any failure returns
// nil and the caller omits the fields.
func paperSummaries(ctx context.Context, catalog *registry.Store, ids []string) map[string]registry.PaperSummary {
	if catalog == nil || len(ids) == 0 {
		return nil
	}
	summaries, err := catalog.PaperSummaries(ctx, ids)
	if err != nil {
		return nil
	}
	return summaries
}

// attachResultSummaries decorates the wire results with the registry
// hosting summary. Results without a paper_id (minting disabled) or
// without a summary entry keep the fields omitted.
func attachResultSummaries(out []searchResultJSON, summaries map[string]registry.PaperSummary) {
	for i := range out {
		sum, ok := summaries[out[i].PaperID]
		if !ok || out[i].PaperID == "" {
			continue
		}
		hasMD, hasPDF := sum.HasMD, sum.HasPDF
		out[i].HasMD = &hasMD
		out[i].HasPDF = &hasPDF
		out[i].Status = sum.Status
	}
}

// searchResponseJSON is the wire shape of one engine Response.
type searchResponseJSON struct {
	Results    []searchResultJSON `json:"results"`
	Candidates []search.Hit       `json:"candidates"`
}

// RegisterSearch mounts POST /api/search, gated by the papers:read
// scope (search resolves against the paper registry, the same resource
// the other papers:read endpoints expose). catalog decorates the minted
// results with their hosting summary (has_md / has_pdf / status) and
// may be nil — the fields are then omitted.
func RegisterSearch(se *core.ServeEvent, engine *search.Engine, catalog *registry.Store, enforcer *casbin.Enforcer) {
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
		entry.Normalize()
		if rejectEmptySearchEntry(re, entry) {
			return nil
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
		attachResultSummaries(out.Results, resultSummaries(re.Request.Context(), catalog, resp.Results))
		return re.JSON(http.StatusOK, out)
	}))
}

// rejectEmptySearchEntry answers the 400 for entries that carry none of
// the four query inputs (text / title / arxiv_id / doi). Guarding here
// keeps an identity-only or text-only typo from fanning out an empty
// query to every provider (the remote microservice would per-backend
// 400 it). Shared by POST /api/search and POST /api/search/agentic;
// reports true when the 400 was written — the caller must then return
// from its handler without writing a second response (re.JSON's return
// value is an encoding error, not a "response written" signal).
func rejectEmptySearchEntry(re *core.RequestEvent, entry search.SearchEntry) bool {
	if entry.Text == "" && entry.Title == "" && entry.ArxivID == "" && entry.DOI == "" {
		re.JSON(http.StatusBadRequest, map[string]string{
			"detail": "empty search entry: provide at least one of text, title, arxiv_id, doi",
		})
		return true
	}
	return false
}

// paperDetailHandler answers GET /api/papers/{paper_id} (dispatched from
// the /api/papers/{path...} catch-all for "qa_"-prefixed single-segment
// paths, and via dispatchDetailByIdentifier for arXiv-id / DOI paths):
// the registry paper — status, identities, and its assets. catalog is
// the paperCatalog seam so tests can drive this without PostgreSQL.
func paperDetailHandler(re *core.RequestEvent, catalog paperCatalog, paperID string, ingester *ingest.Ingester, converter *mineru.Converter) error {
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
		"acquisition": paperAcquisition(ctx, p, detail.Assets, ingester, converter),
	})
}
