package routes

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/config"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/mineru"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/objstore"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/paperassets"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/registry"
	"github.com/pocketbase/pocketbase/core"
)

// Kept until the legacy DOI wrapper is replaced by the common gated dispatcher.
const pdfGoneDetail = "PDF delivery is disabled; use the markdown endpoint instead"

type contentPDFConverter interface {
	EnsurePDF(context.Context, string) *mineru.Job
	Lookup(string) (*mineru.Job, bool)
}

func contentPDFConverterPresent(c contentPDFConverter) bool {
	if c == nil {
		return false
	}
	if real, ok := c.(*mineru.Converter); ok {
		return real != nil
	}
	return true
}

// Gated PDF delivery is independent of MinerU tokens and never triggers parsing.
// Only genuinely absent unselected arXiv PDFs may enter the fetch-only pipeline.
func contentPDFHandler(re *core.RequestEvent, cfg *config.Config, store objstore.Store, catalog contentCatalog, converter contentPDFConverter, requestedID string) error {
	if !contentAccessEnabled(cfg) {
		return re.JSON(http.StatusNotFound, map[string]string{"detail": "paper access disabled"})
	}
	ctx, cancel := objstore.ReadContext(re.Request.Context())
	defer cancel()
	q := re.Request.URL.Query()
	rp, src, err := sourceForAccess(ctx, store, catalog, requestedID, q.Get("source_id"), q.Get("version"))
	if err != nil {
		var access *contentAccessError
		if !contentPDFConverterPresent(converter) || !errors.As(err, &access) || !access.AllowFetch || q.Get("source_id") != "" {
			return contentAccessErrorResponse(re, err)
		}
		job, fetchErr := startContentPDFFetch(re.Request.Context(), catalog, converter, rp, requestedID, q.Get("version"), "")
		if fetchErr != nil {
			return contentAccessErrorResponse(re, fetchErr)
		}
		if rp.canonical == "" && job.PaperID != "" {
			rp = paperResolution(requestedID, job.PaperID)
		}
		if job.State == mineru.JobStateDone {
			rp, src, err = sourceForAccess(ctx, store, catalog, requestedID, "", q.Get("version"))
			if err != nil {
				return contentAccessErrorResponse(re, err)
			}
		} else {
			applyResolutionHeaders(re.Response, rp.resolution)
			body := contentPDFJobBody(requestedID, rp, job)
			status := http.StatusAccepted
			if job.State == mineru.JobStateFailed {
				status = http.StatusServiceUnavailable
				if errors.Is(job.ErrKind, mineru.ErrFatal) || errors.Is(job.Err, mineru.ErrNoDOISource) {
					status = http.StatusNotFound
				}
			}
			re.Response.Header().Set("Retry-After", "2")
			body["status_url"] = contentPDFStatusURL(re)
			return re.JSON(status, body)
		}
	}
	setContentSourceHeaders(re, rp, src)
	if assetFormat(re) == "link" {
		// Auth and the paper-access switch are checked on every download.
		// Never hand out a bucket presign that bypasses either boundary.
		u := contentPDFLocator(rp, src, q.Get("version"))
		return re.JSON(http.StatusOK, map[string]any{"paper_id": rp.canonical, "source_id": src.SourceID, "pdf_sha256": src.Sha256, "source_origin": normalizedSourceOrigin(src.Origin), "format": "link", "pdf_url": u})
	}
	if err := serveSourcePDF(re, store, src, rp.canonical+"-"+src.SourceID+".pdf"); err != nil {
		return contentAccessErrorResponse(re, err)
	}
	return nil
}

func contentPDFStatusHandler(re *core.RequestEvent, cfg *config.Config, store objstore.Store, catalog contentCatalog, converter contentPDFConverter, requestedID string) error {
	if !contentAccessEnabled(cfg) {
		return re.JSON(http.StatusNotFound, map[string]string{"detail": "paper access disabled"})
	}
	ctx, cancel := objstore.ReadContext(re.Request.Context())
	defer cancel()
	q := re.Request.URL.Query()
	rp, src, err := sourceForAccess(ctx, store, catalog, requestedID, q.Get("source_id"), q.Get("version"))
	if err == nil {
		setContentSourceHeaders(re, rp, src)
		body := map[string]any{"paper_id": rp.canonical, "source_id": src.SourceID, "pdf_sha256": src.Sha256, "source_origin": normalizedSourceOrigin(src.Origin), "state": "cached", "phase": string(mineru.PhaseReady), "pdf_ready": true, "md_ready": false, "pdf_url": contentPDFLocator(rp, src, q.Get("version"))}
		embedResolutionInBody(body, rp.resolution)
		return re.JSON(http.StatusOK, body)
	}
	var access *contentAccessError
	if !errors.As(err, &access) || !access.AllowFetch || q.Get("source_id") != "" {
		return contentAccessErrorResponse(re, err)
	}
	if job, found := lookupContentPDFFetch(ctx, catalog, converter, rp, requestedID, q.Get("version")); found {
		body := contentPDFJobBody(requestedID, rp, job)
		// A stale process Done record is NEVER evidence that bytes exist.
		if job.State == mineru.JobStateDone {
			body["state"] = "none"
			body["phase"] = ""
		}
		body["pdf_ready"] = false
		return re.JSON(http.StatusOK, body)
	}
	return re.JSON(http.StatusOK, map[string]any{"paper_id": rp.canonical, "state": "none", "pdf_ready": false, "md_ready": false, "detail": "no selected PDF; GET /pdf may initiate a fetch-only job"})
}

// Shared acquisition seams for /pdf and lazy /read. The caller MUST have
// checked sourceForAccess's AllowFetch and must never invoke them for a source
// pin, revision pin, integrity failure, outage, or missing frozen source.
func startContentPDFFetch(ctx context.Context, catalog contentCatalog, converter contentPDFConverter, rp resolvedPaper, requestedID, version, oaURL string) (*mineru.Job, error) {
	if !contentPDFConverterPresent(converter) {
		return nil, contentMissing("PDF acquisition not configured", false)
	}
	requestedID = normalizeIDForDispatch(requestedID)
	var job *mineru.Job
	if isDOICandidate(requestedID) && version == "" {
		doi, ok := paperassets.ValidateDOI(requestedID)
		if !ok {
			return nil, &contentAccessError{Status: 400, Detail: "invalid DOI"}
		}
		fetcher, ok := converter.(interface {
			EnsurePDFByDOI(context.Context, string, string) *mineru.Job
		})
		if !ok {
			return nil, contentMissing("published PDF acquisition unavailable", false)
		}
		job = fetcher.EnsurePDFByDOI(ctx, doi, oaURL)
	} else {
		id := fetchPDFIdentity(ctx, catalog, rp, requestedID, version)
		if id == "" {
			return nil, contentMissing("no exact versioned PDF identity available for acquisition", false)
		}
		job = converter.EnsurePDF(ctx, id)
	}
	if job == nil {
		return nil, errors.New("PDF fetcher returned no job")
	}
	return job, nil
}
func lookupContentPDFFetch(ctx context.Context, catalog contentCatalog, converter contentPDFConverter, rp resolvedPaper, requestedID, version string) (*mineru.Job, bool) {
	if !contentPDFConverterPresent(converter) {
		return nil, false
	}
	requestedID = normalizeIDForDispatch(requestedID)
	if isDOICandidate(requestedID) && version == "" {
		return converter.Lookup("doi:" + registry.NormalizeDOI(requestedID))
	}
	id := fetchPDFIdentity(ctx, catalog, rp, requestedID, version)
	if id == "" {
		return nil, false
	}
	return converter.Lookup(id)
}

func fetchPDFIdentity(ctx context.Context, catalog contentCatalog, rp resolvedPaper, requestedID, version string) string {
	p, err := paperassets.Parse(requestedID)
	if err == nil && p.IsValid() {
		if version != "" {
			return registry.NormalizeArxivID(p.Canonical) + "v" + strings.TrimPrefix(version, "v")
		}
		if p.Version != "" {
			return p.Canonical
		}
	}
	if catalog == nil || rp.canonical == "" {
		return ""
	}
	d, found, err := catalog.GetWithAssets(ctx, rp.canonical)
	if err != nil || !found || d == nil || d.Paper == nil || d.Paper.ArxivID == "" {
		return ""
	}
	v := 0
	if version != "" {
		v, _ = strconv.Atoi(strings.TrimPrefix(version, "v"))
	} else {
		for _, a := range d.Assets {
			if a.Source == "arxiv" && a.ArxivVersion > v {
				v = a.ArxivVersion
			}
		}
	}
	if v <= 0 {
		return ""
	}
	return fmt.Sprintf("%sv%d", registry.NormalizeArxivID(d.Paper.ArxivID), v)
}
func contentPDFLocator(rp resolvedPaper, src registry.PaperSource, version string) string {
	v := url.Values{"source_id": {src.SourceID}, "format": {"bytes"}}
	if version == "" {
		if n := sourceOriginVersion(src.Origin); n > 0 {
			version = fmt.Sprintf("v%d", n)
		}
	}
	if version != "" {
		v.Set("version", version)
	}
	return "/api/papers/" + rp.canonical + "/pdf?" + v.Encode()
}

func contentSelectionQuery(q url.Values) string {
	v := url.Values{}
	for _, key := range []string{"source_id", "version"} {
		if value := q.Get(key); value != "" {
			v.Set(key, value)
		}
	}
	if len(v) == 0 {
		return ""
	}
	return "?" + v.Encode()
}
func contentPDFStatusURL(re *core.RequestEvent) string {
	return re.Request.URL.Path + "/status" + contentSelectionQuery(re.Request.URL.Query())
}
func contentPDFJobBody(requested string, rp resolvedPaper, job *mineru.Job) map[string]any {
	body := snapshotBody(requested, job)
	delete(body, "convert")
	delete(body, "markdown_url")
	body["paper_id"] = rp.canonical
	body["pdf_ready"] = false
	body["md_ready"] = false
	if job.SourceID != "" {
		body["source_id"] = job.SourceID
	}
	if job.SourcePDFSHA256 != "" {
		body["pdf_sha256"] = job.SourcePDFSHA256
	}
	embedResolutionInBody(body, rp.resolution)
	return body
}

// Legacy wrappers retain validation but cannot bypass the catalog/frozen gate.
// RegisterPapers calls the catalog-aware handlers above for content delivery.
func pdfHandler(re *core.RequestEvent, cfg *config.Config, store objstore.Store, converter *mineru.Converter, arxivID string) error {
	if _, ok := paperassets.ValidateUploadID(arxivID); !ok {
		return re.JSON(http.StatusBadRequest, map[string]string{"detail": "invalid versioned arXiv id for PDF"})
	}
	return contentPDFHandler(re, cfg, store, nil, converter, arxivID)
}
func pdfStatusHandler(re *core.RequestEvent, cfg *config.Config, store objstore.Store, converter *mineru.Converter, arxivID string) error {
	if _, ok := paperassets.ValidateUploadID(arxivID); !ok {
		return re.JSON(http.StatusBadRequest, map[string]string{"detail": "invalid versioned arXiv id for PDF status"})
	}
	return contentPDFStatusHandler(re, cfg, store, nil, converter, arxivID)
}
func sanitizeFilename(canonical string) string {
	return strings.Map(func(r rune) rune {
		if r == '/' || r == '\\' || r == '"' || r < 32 || r == 127 {
			return '_'
		}
		return r
	}, canonical)
}
