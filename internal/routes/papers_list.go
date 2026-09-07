package routes

// papers_list.go: GET /api/papers — the paginated papers list backing
// the "converted papers" frontend page. Filters: has_md (converted
// markdown present on the default asset), status, title substring, and
// the exact-identity trio arxiv_id / doi / paper_id; sorted by
// created_at (default) or updated_at, descending.

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/paperassets"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/registry"

	"github.com/pocketbase/pocketbase/core"
)

const (
	papersListDefaultPerPage = 20
	papersListMaxPerPage     = 100
)

// papersListHandler answers GET /api/papers. The bare path is registered
// separately from the /api/papers/{path...} catch-all (which matches only
// sub-paths), so it cannot shadow stats / needs-mineru / lookup / detail.
func papersListHandler(re *core.RequestEvent, catalog *registry.Store) error {
	q := re.Request.URL.Query()

	filter := registry.ListFilter{Sort: "created_at"}
	if v := q.Get("has_md"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return re.JSON(http.StatusBadRequest, map[string]string{
				"detail": "invalid has_md (want true/false): " + v,
			})
		}
		filter.HasMD = &b
	}
	if v := q.Get("status"); v != "" {
		switch v {
		case "pending", "ready", "failed":
			filter.Status = v
		default:
			return re.JSON(http.StatusBadRequest, map[string]string{
				"detail": "invalid status (want pending/ready/failed): " + v,
			})
		}
	}
	filter.Query = q.Get("q")
	// Exact-identity filters: format-checked here (400 on garbage), then
	// normalized inside ListPapers — arxiv_id may carry a version suffix
	// and doi a URL prefix; both are canonicalized before the SQL.
	if v := q.Get("arxiv_id"); v != "" {
		if _, perr := paperassets.Parse(v); perr != nil {
			return re.JSON(http.StatusBadRequest, map[string]string{
				"detail": "invalid arxiv_id (want e.g. 2501.00010 or quant-ph/9508027, optional vN): " + v,
			})
		}
		filter.ArxivID = v
	}
	if v := q.Get("doi"); v != "" {
		if _, ok := paperassets.ValidateDOI(v); !ok {
			return re.JSON(http.StatusBadRequest, map[string]string{
				"detail": "invalid doi: " + v,
			})
		}
		filter.DOI = v
	}
	if v := q.Get("paper_id"); v != "" {
		if !strings.HasPrefix(v, "qa_") || len(v) <= len("qa_") {
			return re.JSON(http.StatusBadRequest, map[string]string{
				"detail": "invalid paper_id (want the qa_ surrogate id): " + v,
			})
		}
		filter.PaperID = v
	}
	if v := q.Get("sort"); v != "" {
		switch v {
		case "created_at", "updated_at":
			filter.Sort = v
		default:
			return re.JSON(http.StatusBadRequest, map[string]string{
				"detail": "invalid sort (want created_at/updated_at): " + v,
			})
		}
	}
	filter.Page, _ = strconv.Atoi(q.Get("page"))
	if filter.Page < 1 {
		filter.Page = 1
	}
	filter.PerPage, _ = strconv.Atoi(q.Get("per_page"))
	if filter.PerPage < 1 {
		filter.PerPage = papersListDefaultPerPage
	} else if filter.PerPage > papersListMaxPerPage {
		filter.PerPage = papersListMaxPerPage
	}

	items, total, err := catalog.ListPapers(re.Request.Context(), filter)
	if err != nil {
		if errors.Is(err, registry.ErrCatalogUnavailable) {
			return re.JSON(http.StatusServiceUnavailable, map[string]string{
				"detail": "catalog unavailable (PostgreSQL unreachable); retry shortly",
			})
		}
		return re.JSON(http.StatusInternalServerError, map[string]string{"detail": err.Error()})
	}

	out := make([]map[string]any, 0, len(items))
	for _, it := range items {
		out = append(out, map[string]any{
			"paper_id":    it.PaperID,
			"arxiv_id":    it.ArxivID,
			"doi":         it.DOI,
			"title":       it.Title,
			"status":      it.Status,
			"has_pdf":     it.HasPDF,
			"has_md":      it.HasMD,
			"image_count": it.ImageCount,
			"created_at":  it.CreatedAt.UTC().Format(time.RFC3339),
			"updated_at":  it.UpdatedAt.UTC().Format(time.RFC3339),
		})
	}
	return re.JSON(http.StatusOK, map[string]any{
		"items":    out,
		"total":    total,
		"page":     filter.Page,
		"per_page": filter.PerPage,
	})
}
