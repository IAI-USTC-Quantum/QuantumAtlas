package routes

// downloader.go: the robust downloader surface.
//
//	POST /api/downloader/fetch  — scopeGuard(papers, write). Body:
//	  {"items": ["<doi|arxiv id|url>", ...]} (≤50). Each line is parsed,
//	  resolve-or-minted into the registry, and enqueued on the strategy
//	  ladder (arXiv → OA APIs → publisher patterns → landing page →
//	  agent fallback). Responds per item with the parsed kind, the
//	  paper_id and any parse error.
//	GET  /api/downloader/jobs   — scopeGuard(papers, read). The job
//	  snapshot: per-paper state/phase, winning strategy, full attempt
//	  trace and the healthz-style counters.
//
// The module is registered as the third builtin plugin manifest
// (id "downloader", capability "download") in cmd/qatlasd/main.go; dl
// is nil when paper access or the downloader switch is off — the routes
// stay mounted but answer 503 (same convention as the agentic route).

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/downloader"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/registry"

	"github.com/casbin/casbin/v2"
	"github.com/pocketbase/pocketbase/core"
)

// maxDownloaderItems bounds one submission batch.
const maxDownloaderItems = 50

// Downloader is the slice of *downloader.Downloader the routes need.
type Downloader interface {
	Enqueue(ctx context.Context, paperID, input string, kind downloader.IdentifierKind, ref registry.PaperRef) bool
	Snapshot() []downloader.Progress
	SnapshotCounters() map[string]int64
}

// minter resolve-or-mints an identifier into the registry.
type downloaderMinter interface {
	ResolveOrMint(ctx context.Context, ref registry.PaperRef) (paperID string, created bool, err error)
}

// downloaderFetchRequest is the POST body shape.
type downloaderFetchRequest struct {
	Items []string `json:"items"`
}

// downloaderFetchItem is one response row.
type downloaderFetchItem struct {
	Input   string `json:"input"`
	Kind    string `json:"kind"`
	PaperID string `json:"paper_id,omitempty"`
	Created bool   `json:"created"`
	Error   string `json:"error,omitempty"`
}

// RegisterDownloader mounts both endpoints.
func RegisterDownloader(se *core.ServeEvent, dl Downloader, minter downloaderMinter, enforcer *casbin.Enforcer) {
	if dl == nil {
		// Still mount (stable surface) — both routes answer 503.
		se.Router.POST("/api/downloader/fetch", scopeGuard(enforcer, "papers", "write", func(re *core.RequestEvent) error {
			return re.JSON(http.StatusServiceUnavailable, map[string]string{
				"detail": "downloader not configured (paper_access.enabled / downloader.enabled)",
			})
		}))
		se.Router.GET("/api/downloader/jobs", scopeGuard(enforcer, "papers", "read", func(re *core.RequestEvent) error {
			return re.JSON(http.StatusOK, map[string]any{"jobs": []downloader.Progress{}, "counters": map[string]int64{}})
		}))
		return
	}

	se.Router.POST("/api/downloader/fetch", scopeGuard(enforcer, "papers", "write", downloaderFetchHandler(dl, minter)))
	se.Router.GET("/api/downloader/jobs", scopeGuard(enforcer, "papers", "read", downloaderJobsHandler(dl)))
}

func downloaderFetchHandler(dl Downloader, minter downloaderMinter) func(re *core.RequestEvent) error {
	return func(re *core.RequestEvent) error {
		if minter == nil {
			return re.JSON(http.StatusServiceUnavailable, map[string]string{
				"detail": "downloader not configured (registry unavailable)",
			})
		}
		raw, err := io.ReadAll(io.LimitReader(re.Request.Body, 1<<20))
		if err != nil {
			return re.JSON(http.StatusBadRequest, map[string]string{"detail": "read body: " + err.Error()})
		}
		var body downloaderFetchRequest
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &body); err != nil {
				return re.JSON(http.StatusBadRequest, map[string]string{"detail": "invalid JSON: " + err.Error()})
			}
		}
		if len(body.Items) == 0 {
			return re.JSON(http.StatusBadRequest, map[string]string{"detail": "items is required"})
		}
		if len(body.Items) > maxDownloaderItems {
			return re.JSON(http.StatusBadRequest, map[string]string{
				"detail": "too many items (max 50 per batch)",
			})
		}

		ctx := re.Request.Context()
		out := make([]downloaderFetchItem, 0, len(body.Items))
		enqueued := 0
		for _, input := range body.Items {
			item := downloaderFetchItem{Input: input}
			id, err := downloader.ParseIdentifier(input)
			if err != nil {
				item.Kind = string(downloader.KindBad)
				item.Error = err.Error()
				out = append(out, item)
				continue
			}
			item.Kind = string(id.Kind)
			paperID, created, err := minter.ResolveOrMint(ctx, id.Ref)
			if err != nil {
				if err == registry.ErrTitleOnlyRef {
					item.Error = "identifier carries neither DOI nor arXiv id"
				} else if strings.Contains(err.Error(), "catalog unavailable") {
					return re.JSON(http.StatusServiceUnavailable, map[string]string{
						"detail": "catalog unavailable (PostgreSQL unreachable); retry shortly",
					})
				} else {
					item.Error = err.Error()
				}
				out = append(out, item)
				continue
			}
			item.PaperID = paperID
			item.Created = created
			if dl.Enqueue(ctx, paperID, input, id.Kind, id.Ref) {
				enqueued++
			} else {
				item.Error = "download queue unavailable; retry shortly"
			}
			out = append(out, item)
		}
		return re.JSON(http.StatusOK, map[string]any{
			"items":    out,
			"enqueued": enqueued,
		})
	}
}

func downloaderJobsHandler(dl Downloader) func(re *core.RequestEvent) error {
	return func(re *core.RequestEvent) error {
		jobs := dl.Snapshot()
		if jobs == nil {
			jobs = []downloader.Progress{}
		}
		return re.JSON(http.StatusOK, map[string]any{
			"jobs":     jobs,
			"counters": dl.SnapshotCounters(),
		})
	}
}
