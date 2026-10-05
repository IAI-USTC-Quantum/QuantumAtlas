package routes

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/config"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/mineru"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/objstore"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/paperbundle"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/paperread"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/registry"
	"github.com/pocketbase/pocketbase/core"
)

// Production *mineru.Converter satisfies this small surface; tests use pure
// snapshots without submitting inference or making any provider/network calls.
type contentReadConverter interface {
	EnsureSource(context.Context, string, string) *mineru.Job
	LookupSource(string, string) (*mineru.Job, bool)
}

// Only content GET uses the full intent. The shared PDF helper's identity
// selection is reused, but its calls are forwarded to Ensure/EnsureByDOI so
// the existing converter chains freeze-PDF -> EnsureSource after fetch. Actual
// PDF GET keeps the fetch-only implementation; status never invokes this adapter.
type contentIntentConverter interface {
	contentReadConverter
	Ensure(context.Context, string) *mineru.Job
	EnsureByDOI(context.Context, string, string) *mineru.Job
	Lookup(string) (*mineru.Job, bool)
}
type contentAcquireAdapter struct{ contentIntentConverter }

func (a contentAcquireAdapter) EnsurePDF(ctx context.Context, id string) *mineru.Job {
	return a.Ensure(ctx, id)
}
func (a contentAcquireAdapter) EnsurePDFByDOI(ctx context.Context, doi, oa string) *mineru.Job {
	return a.EnsureByDOI(ctx, doi, oa)
}

func contentConverterAvailable(converter contentReadConverter) bool {
	if converter == nil {
		return false
	}
	value := reflect.ValueOf(converter)
	return value.Kind() != reflect.Pointer || !value.IsNil()
}

func validateContentQuery(re *core.RequestEvent) error {
	for _, key := range []string{"source_id", "revision", "version", "cursor", "page", "block", "limit"} {
		if len(re.Request.URL.Query()[key]) > 1 {
			return &contentAccessError{Status: http.StatusBadRequest, Detail: "duplicate query selector: " + key}
		}
	}
	return nil
}

// selectReadingSource resolves cursor pins BEFORE looking up any current
// bundle/source. Explicit revision reads require a published complete bundle;
// comments-only historical revisions are never silently upgraded or repaired.
func selectReadingSource(re *core.RequestEvent, cfg *config.Config, store objstore.Store, catalog contentCatalog, requestedID string) (rp resolvedPaper, src registry.PaperSource, pinned *registry.ParseBundle, handled bool, err error) {
	if !contentAccessEnabled(cfg) {
		return rp, src, nil, true, re.JSON(http.StatusNotFound, map[string]string{"detail": "paper content access is disabled"})
	}
	if err = validateContentQuery(re); err != nil {
		return rp, src, nil, true, contentBundleErrorResponse(re, err)
	}
	if catalog == nil || store == nil {
		return rp, src, nil, true, contentBundleErrorResponse(re, &contentAccessError{Status: 503, Detail: "content catalog or object store unavailable"})
	}
	q := re.Request.URL.Query()
	sourceID, revision := q.Get("source_id"), q.Get("revision")
	var pin *paperread.CursorIdentity
	if cursor := q.Get("cursor"); cursor != "" {
		identity, decodeErr := paperread.PeekCursor(cursor)
		if decodeErr != nil {
			return rp, src, nil, true, readingErrorResponse(re, decodeErr)
		}
		if !paperread.IsSupportedRenderer(identity.Renderer) || sourceID != "" && sourceID != identity.SourceID || revision != "" && revision != identity.Revision {
			return rp, src, nil, true, readingErrorResponse(re, paperread.ErrCursorMismatch)
		}
		sourceID, revision = identity.SourceID, identity.Revision
		pin = &identity
	}
	ctx, cancel := objstore.ReadContext(re.Request.Context())
	defer cancel()
	if revision != "" {
		var status int
		var detail string
		rp, status, detail = resolveCanonicalPaper(ctx, catalog, requestedID)
		if status != 0 {
			return rp, src, nil, true, blockResolveError(re, status, detail)
		}
		if pin != nil && pin.PaperID != rp.canonical {
			return rp, src, nil, true, readingErrorResponse(re, paperread.ErrCursorMismatch)
		}
		applyResolutionHeaders(re.Response, rp.resolution)
		b, found, getErr := catalog.GetParseBundle(ctx, rp.canonical, revision)
		if getErr != nil {
			return rp, src, nil, true, contentBundleErrorResponse(re, getErr)
		}
		if !found {
			return rp, src, nil, true, re.JSON(http.StatusNotFound, map[string]string{"detail": "no published complete bundle for the pinned revision"})
		}
		if b.PaperID != rp.canonical || b.RevisionID != revision {
			return rp, src, nil, true, contentBundleErrorResponse(re, paperbundle.ErrIntegrity)
		}
		if sourceID != "" && sourceID != b.SourceID {
			return rp, src, nil, true, re.JSON(http.StatusConflict, map[string]string{"code": "source_revision_mismatch", "detail": "revision is not bound to the requested source"})
		}
		sourceID = b.SourceID
		pinned = &b
	}
	rp, src, err = sourceForAccess(ctx, store, catalog, requestedID, sourceID, q.Get("version"))
	if err != nil {
		var access *contentAccessError
		if pinned == nil && sourceID == "" && errors.As(err, &access) && access.AllowFetch {
			return rp, src, nil, false, err
		}
		return rp, src, pinned, true, contentBundleErrorResponse(re, err)
	}
	setContentSourceHeaders(re, rp, src)
	return rp, src, pinned, false, nil
}

// readyContent is shared by read/markdown/images. handled=true means an error
// or lazy-acquisition 202 has already been written; callers return err without
// writing again. handled=false guarantees a newly published, verified bundle.
func readyContent(re *core.RequestEvent, cfg *config.Config, store objstore.Store, catalog contentCatalog, converter contentReadConverter, requestedID string) (rp resolvedPaper, src registry.PaperSource, b registry.ParseBundle, m paperbundle.Manifest, handled bool, err error) {
	var pinned *registry.ParseBundle
	rp, src, pinned, handled, err = selectReadingSource(re, cfg, store, catalog, requestedID)
	if handled {
		return
	}
	ctx, cancel := objstore.ReadContext(re.Request.Context())
	defer cancel()
	if err != nil {
		acquirer, ok := converter.(contentIntentConverter)
		if !ok || !contentConverterAvailable(converter) {
			return rp, src, b, m, true, contentBundleErrorResponse(re, err)
		}
		job, fetchErr := startContentPDFFetch(re.Request.Context(), catalog, contentAcquireAdapter{acquirer}, rp, requestedID, re.Request.URL.Query().Get("version"), "")
		if fetchErr != nil {
			return rp, src, b, m, true, contentBundleErrorResponse(re, fetchErr)
		}
		if rp.requested == "" {
			rp.requested = requestedID
		}
		if rp.canonical == "" && job.PaperID != "" {
			rp = paperResolution(requestedID, job.PaperID)
		}
		if job.State == mineru.JobStateFailed {
			return rp, src, b, m, true, contentJobFailure(re, src, job)
		}
		if job.State != mineru.JobStateDone {
			return rp, src, b, m, true, contentPendingResponse(re, rp, src, job)
		}
		// Eager cache completion is still verified against durable frozen bytes.
		rp, src, err = sourceForAccess(ctx, store, catalog, requestedID, job.SourceID, re.Request.URL.Query().Get("version"))
		if err != nil {
			return rp, src, b, m, true, contentBundleErrorResponse(re, err)
		}
		setContentSourceHeaders(re, rp, src)
	}
	if pinned != nil {
		b = *pinned
		m, err = verifiedContentBundle(ctx, store, b, src)
		if err != nil {
			return rp, src, b, m, true, contentBundleErrorResponse(re, err)
		}
		return rp, src, b, m, false, nil
	}
	var found bool
	b, found, err = catalog.GetReadyParseBundle(ctx, store, rp.canonical, src.SourceID)
	if err != nil {
		return rp, src, b, m, true, contentBundleErrorResponse(re, err)
	}
	if found {
		m, err = verifiedContentBundle(ctx, store, b, src)
		if err != nil {
			return rp, src, b, m, true, contentBundleErrorResponse(re, err)
		}
		return rp, src, b, m, false, nil
	}
	if !contentConverterAvailable(converter) {
		return rp, src, b, m, true, contentBundleErrorResponse(re, &contentAccessError{Status: 503, Detail: "no complete content bundle and converter is unavailable"})
	}
	job := converter.EnsureSource(re.Request.Context(), rp.canonical, src.SourceID)
	if job != nil && job.State == mineru.JobStateDone {
		// A worker may have published between the first probe and EnsureSource.
		// Process Done itself is never evidence of readiness.
		b, found, err = catalog.GetReadyParseBundle(ctx, store, rp.canonical, src.SourceID)
		if err != nil {
			return rp, src, b, m, true, contentBundleErrorResponse(re, err)
		}
		if found {
			m, err = verifiedContentBundle(ctx, store, b, src)
			if err != nil {
				return rp, src, b, m, true, contentBundleErrorResponse(re, err)
			}
			return rp, src, b, m, false, nil
		}
	}
	if job != nil && job.State == mineru.JobStateFailed {
		return rp, src, b, m, true, contentJobFailure(re, src, job)
	}
	return rp, src, b, m, true, contentPendingResponse(re, rp, src, job)
}

func contentReadHandler(re *core.RequestEvent, cfg *config.Config, store objstore.Store, catalog contentCatalog, converter contentReadConverter, requestedID string) error {
	if !contentAccessEnabled(cfg) {
		return re.JSON(http.StatusNotFound, map[string]string{"detail": "paper content access is disabled"})
	}
	page, block, limit, err := readingSelectors(re)
	if err != nil {
		return readingErrorResponse(re, err)
	}
	rp, src, b, m, handled, err := readyContent(re, cfg, store, catalog, converter, requestedID)
	if handled || err != nil {
		return err
	}
	ctx, cancel := objstore.ReadContext(re.Request.Context())
	defer cancel()
	middle, err := verifiedBundleMember(ctx, store, b, m, m.MiddlePath)
	if err != nil {
		return contentBundleErrorResponse(re, err)
	}
	response, err := paperread.Read(middle, paperread.Options{
		PaperID: rp.canonical, SourceID: src.SourceID, Revision: b.RevisionID, SourceSHA256: src.Sha256,
		BundleSHA256: b.ManifestSHA256, Tier: b.Tier, Page: page, Block: block, Limit: limit, Cursor: re.Request.URL.Query().Get("cursor"),
		ImageURL: func(member string) string {
			if image, ok := bundleImageMember(m, m.MiddlePath, member); ok {
				return bundleMemberURL(rp.canonical, b.RevisionID, image.Path)
			}
			return ""
		},
	})
	if err != nil {
		return readingErrorResponse(re, err)
	}
	setBundleReadHeaders(re, b)
	return re.JSON(http.StatusOK, response)
}

func readingSelectors(re *core.RequestEvent) (page, block, limit int, err error) {
	if err = validateContentQuery(re); err != nil {
		return
	}
	for _, field := range []struct {
		name string
		out  *int
		max  int
	}{{"page", &page, 0}, {"block", &block, 0}, {"limit", &limit, paperread.MaxLimit}} {
		if values, present := re.Request.URL.Query()[field.name]; present {
			if len(values) != 1 || values[0] == "" {
				err = paperread.ErrInvalidRequest
				return
			}
			value, parseErr := strconv.Atoi(values[0])
			if parseErr != nil || value < 1 || field.max > 0 && value > field.max {
				err = paperread.ErrInvalidRequest
				return
			}
			*field.out = value
		}
	}
	if block > 0 && page == 0 && re.Request.URL.Query().Get("cursor") == "" {
		err = paperread.ErrInvalidRequest
	}
	return
}

func contentReadStatusHandler(re *core.RequestEvent, cfg *config.Config, store objstore.Store, catalog contentCatalog, converter contentReadConverter, requestedID string) error {
	rp, src, pinned, handled, err := selectReadingSource(re, cfg, store, catalog, requestedID)
	if handled {
		return err
	}
	ctx, cancel := objstore.ReadContext(re.Request.Context())
	defer cancel()
	if err != nil {
		if rp.requested == "" {
			rp.requested = requestedID
		}
		if fetcher, ok := converter.(contentPDFConverter); ok && contentConverterAvailable(converter) {
			if job, found := lookupContentPDFFetch(ctx, catalog, fetcher, rp, requestedID, re.Request.URL.Query().Get("version")); found && job != nil {
				if rp.canonical == "" && job.PaperID != "" {
					rp = paperResolution(requestedID, job.PaperID)
				}
				if job.State == mineru.JobStateFailed {
					body := contentJobBody(rp, src, job)
					body["detail"] = errString(job.Err)
					return re.JSON(http.StatusOK, body)
				}
				return contentPendingResponse(re, rp, src, job)
			}
		}
		return re.JSON(http.StatusOK, map[string]any{"paper_id": rp.canonical, "source_id": "", "revision": "", "state": "none", "ready": false, "md_ready": false, "pdf_ready": false, "detail": "no frozen PDF or complete bundle; GET content to acquire"})
	}
	var b registry.ParseBundle
	found := pinned != nil
	if pinned != nil {
		b = *pinned
	} else {
		b, found, err = catalog.GetReadyParseBundle(ctx, store, rp.canonical, src.SourceID)
	}
	if err != nil {
		return contentBundleErrorResponse(re, err)
	}
	if found {
		if _, err := verifiedContentBundle(ctx, store, b, src); err != nil {
			return contentBundleErrorResponse(re, err)
		}
		setBundleReadHeaders(re, b)
		selection := url.Values{"source_id": {src.SourceID}, "revision": {b.RevisionID}}
		for _, key := range []string{"page", "block", "limit"} {
			if value := re.Request.URL.Query().Get(key); value != "" {
				selection.Set(key, value)
			}
		}
		query := selection.Encode()
		base := "/api/papers/" + url.PathEscape(rp.canonical)
		return re.JSON(http.StatusOK, map[string]any{"paper_id": rp.canonical, "source_id": src.SourceID, "source_sha256": src.Sha256, "revision": b.RevisionID, "state": "cached", "phase": "ready", "ready": true, "md_ready": true, "pdf_ready": true, "markdown_url": base + "/markdown?" + query, "read_url": base + "/read?" + query, "manifest_sha256": b.ManifestSHA256, "renderer": supportedBundleRenderer(b)})
	}
	var job *mineru.Job
	if contentConverterAvailable(converter) {
		job, _ = converter.LookupSource(rp.canonical, src.SourceID)
	}
	if job == nil {
		return re.JSON(http.StatusOK, map[string]any{"paper_id": rp.canonical, "source_id": src.SourceID, "source_sha256": src.Sha256, "revision": "", "state": "none", "ready": false, "md_ready": false, "pdf_ready": true})
	}
	if job.State == mineru.JobStateFailed {
		body := contentJobBody(rp, src, job)
		body["state"] = "failed"
		body["detail"] = errString(job.Err)
		return re.JSON(http.StatusOK, body)
	}
	return contentPendingResponse(re, rp, src, job)
}

func supportedBundleRenderer(bundle registry.ParseBundle) string {
	version, _ := paperread.RendererForProfile(bundle.Schema, bundle.SchemaVersion)
	return version
}

func contentJobBody(rp resolvedPaper, src registry.PaperSource, job *mineru.Job) map[string]any {
	body := map[string]any{"paper_id": rp.canonical, "source_id": src.SourceID, "source_sha256": src.Sha256, "revision": "", "state": "pending", "ready": false, "md_ready": false, "pdf_ready": src.SourceID != "" && src.Sha256 != ""}
	if job != nil {
		body["revision"] = job.RevisionID
		if job.State != mineru.JobStateDone {
			body["state"] = string(job.State)
			body["phase"] = string(job.Phase)
		}
		if job.Fetch != nil {
			body["fetch"] = job.Fetch
		}
		if job.Convert != nil {
			body["convert"] = job.Convert
		}
		if !job.CooldownUntil.IsZero() {
			body["retry_after_iso"] = job.CooldownUntil.UTC().Format(time.RFC3339)
		}
	}
	return body
}
func contentPendingResponse(re *core.RequestEvent, rp resolvedPaper, src registry.PaperSource, job *mineru.Job) error {
	target := rp.canonical
	query := url.Values{}
	if src.SourceID != "" {
		query.Set("source_id", src.SourceID)
	} else if rp.requested != "" {
		target = rp.requested
	}
	for _, key := range []string{"version", "page", "block", "limit"} {
		if value := re.Request.URL.Query().Get(key); value != "" {
			query.Set(key, value)
		}
	}
	location := "/api/papers/" + url.PathEscape(target) + "/read/status"
	if len(query) > 0 {
		location += "?" + query.Encode()
	}
	applyResolutionHeaders(re.Response, rp.resolution)
	re.Response.Header().Set("Operation-Location", location)
	re.Response.Header().Set("Retry-After", "5")
	body := contentJobBody(rp, src, job)
	body["operation"] = map[string]any{"status_url": location, "next_poll_after_iso": time.Now().Add(5 * time.Second).UTC().Format(time.RFC3339)}
	return re.JSON(http.StatusAccepted, body)
}
func contentJobFailure(re *core.RequestEvent, src registry.PaperSource, job *mineru.Job) error {
	if errors.Is(job.Err, objstore.ErrUnavailable) || errors.Is(job.Err, registry.ErrCatalogUnavailable) {
		return contentBundleErrorResponse(re, job.Err)
	}
	status := http.StatusBadGateway
	if errors.Is(job.Err, mineru.ErrNoDOISource) || errors.Is(job.Err, objstore.ErrNotFound) {
		return re.JSON(http.StatusNotFound, map[string]any{"paper_id": src.PaperID, "source_id": src.SourceID, "state": "failed", "ready": false, "md_ready": false, "pdf_ready": false, "detail": errString(job.Err)})
	}
	if errors.Is(job.ErrKind, mineru.ErrDailyLimit) || errors.Is(job.ErrKind, mineru.ErrRetryable) || errors.Is(job.ErrKind, mineru.ErrFatal) {
		status = http.StatusServiceUnavailable
		re.Response.Header().Set("Retry-After", "5")
	}
	return re.JSON(status, map[string]any{"paper_id": src.PaperID, "source_id": src.SourceID, "state": "failed", "ready": false, "md_ready": false, "pdf_ready": src.SourceID != "" && src.Sha256 != "", "detail": errString(job.Err), "kind": jobKindLabel(job.ErrKind)})
}
func readingErrorResponse(re *core.RequestEvent, err error) error {
	switch {
	case errors.Is(err, paperread.ErrCursorMismatch):
		return re.JSON(http.StatusConflict, map[string]string{"code": "cursor_pin_mismatch", "detail": "cursor source/revision/artifact/renderer or explicit selectors do not match"})
	case errors.Is(err, paperread.ErrInvalidCursor), errors.Is(err, paperread.ErrInvalidRequest):
		return re.JSON(http.StatusBadRequest, map[string]string{"code": "invalid_read_request", "detail": err.Error()})
	case errors.Is(err, paperread.ErrNotFound):
		return re.JSON(http.StatusNotFound, map[string]string{"detail": "page or exact block does not exist in the pinned revision"})
	case errors.Is(err, mineru.ErrNotMiddleJSON), errors.Is(err, mineru.ErrUnsupportedVer), errors.Is(err, mineru.ErrBadBlocks), errors.Is(err, mineru.ErrBadBBox):
		return re.JSON(http.StatusUnprocessableEntity, map[string]string{"code": "invalid_middle_artifact", "detail": "published Middle JSON cannot be rendered"})
	default:
		return contentBundleErrorResponse(re, err)
	}
}
func bundleMemberURL(paperID, revision, member string) string {
	parts := strings.Split(member, "/")
	for i := range parts {
		parts[i] = url.PathEscape(parts[i])
	}
	return "/api/papers/" + url.PathEscape(paperID) + "/parses/" + url.PathEscape(revision) + "/files/" + strings.Join(parts, "/")
}
