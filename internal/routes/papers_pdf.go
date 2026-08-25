package routes

import (
	"fmt"
	"net/http"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/config"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/mineru"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/objstore"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/paperassets"

	"github.com/pocketbase/pocketbase/core"
)

// pdfGoneDetail is the 410 Gone body shared by the arxiv and DOI pdf
// handlers.
const pdfGoneDetail = "PDF delivery is disabled; use the markdown endpoint instead"

// pdfHandler answers GET /api/papers/{arxiv_id}/pdf.
//
// PDF delivery is disabled (plan §B): the endpoint validates the id and
// always answers 410 Gone, pointing callers at the markdown endpoint.
// The fetch machinery that used to back this handler
// (converter.EnsurePDF, retained as an internal asset-preparation path
// for the conversion pipeline) is no longer reachable from here, and
// pdfStatusHandler below keeps only the pdf_ready/md_ready debug
// booleans — no pdf_url.
//
// Response codes:
//
//   400  malformed arxiv id
//   410  always (PDF delivery disabled)
//
// Routing: only registered when cfg.PaperAccessEnabled is true. Auth:
// gated by scopeGuard("papers", "read") at the route layer.
func pdfHandler(re *core.RequestEvent, cfg *config.Config, store objstore.Store, converter *mineru.Converter, arxivID string) error {
	if _, ok := paperassets.ValidateUploadID(arxivID); !ok {
		return re.JSON(http.StatusBadRequest, map[string]string{
			"detail": fmt.Sprintf("invalid arxiv_id for pdf: %q (version suffix vN required)", arxivID),
		})
	}
	return re.JSON(http.StatusGone, map[string]string{
		"detail": pdfGoneDetail,
	})
}

// pdfStatusHandler answers GET /api/papers/{arxiv_id}/pdf/status.
// Side-effect-free poll surface — same contract as
// markdownStatusHandler but states are restricted to the fetch-only
// flow (no convert phase).
func pdfStatusHandler(re *core.RequestEvent, cfg *config.Config, store objstore.Store, converter *mineru.Converter, arxivID string) error {
	canonical, ok := paperassets.ValidateUploadID(arxivID)
	if !ok {
		return re.JSON(http.StatusBadRequest, map[string]string{
			"detail": fmt.Sprintf("invalid arxiv_id for pdf/status: %q (version suffix vN required)", arxivID),
		})
	}

	ctx := re.Request.Context()
	resolution := resolutionFromContext(ctx)
	applyResolutionHeaders(re.Response, resolution)
	pdfReady, mdReady := probeAssetReadiness(ctx, store, canonical)

	if pdfReady {
		body := map[string]any{
			"arxiv_id":  canonical,
			"state":     "cached",
			"phase":     string(mineru.PhaseReady),
			"pdf_ready": true,
			"md_ready":  mdReady,
		}
		embedResolutionInBody(body, resolution)
		return re.JSON(http.StatusOK, body)
	}

	if job, ok := converter.Lookup(canonical); ok {
		body := snapshotBody(canonical, job)
		body["pdf_ready"] = job.State == mineru.JobStateDone
		body["md_ready"] = false
		// PDF status reports cached only on Done+pdf-present.
		if job.State == mineru.JobStateDone {
			body["state"] = "cached"
			body["phase"] = string(mineru.PhaseReady)
		}
		embedResolutionInBody(body, resolution)
		return re.JSON(http.StatusOK, body)
	}

	// No cache, no job — the /pdf endpoint itself is disabled (410), so
	// this branch is a pure debug probe: it reports whether the PDF bytes
	// happen to be in the store, nothing more.
	state := "none"
	detail := "no PDF in store; PDF delivery is disabled (GET /api/papers/{id}/pdf returns 410) — use /markdown instead"
	if !converter.Enabled() {
		state = "unavailable"
		detail = converter.DisabledReason()
	}
	body := map[string]any{
		"arxiv_id":  canonical,
		"state":     state,
		"phase":     "",
		"pdf_ready": false,
		"md_ready":  mdReady,
		"detail":    detail,
	}
	embedResolutionInBody(body, resolution)
	return re.JSON(http.StatusOK, body)
}

// sanitizeFilename converts a canonical arxiv id (which may contain
// '/' for old-style ids like "quant-ph/9508027v2") into a safe
// Content-Disposition filename component. Slash → underscore is the
// minimum needed; nothing else in the canonical alphabet needs escaping.
func sanitizeFilename(canonical string) string {
	out := make([]byte, 0, len(canonical))
	for i := 0; i < len(canonical); i++ {
		c := canonical[i]
		if c == '/' || c == '\\' || c == '"' {
			out = append(out, '_')
			continue
		}
		out = append(out, c)
	}
	return string(out)
}
