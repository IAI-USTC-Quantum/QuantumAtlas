package routes

// papers_doi.go: DOI-indexed contribution handlers.
//
// upload-pdf and upload-mineru accept EITHER an arxiv id OR a DOI in the
// {arxiv_id} path slot. When the id matches the DOI shape (10.<reg>/...)
// the POST dispatcher routes here. A DOI contribution stores a PUBLISHED
// version (which may have no arXiv preprint) under the disjoint
// "<kind>/doi/..." namespace and records it in the catalog under a
// "doi:<doi>" node.
//
// Verification (the contributor's safety net against a typo'd DOI):
// the server resolves the DOI against OpenAlex and records the canonical
// title / authors / linked arxiv id on the catalog node. Title and
// author values are NEVER taken from the contributor — the only metadata
// the contributor can override is the DOI itself, and a typo'd DOI is
// caught by OpenAlex returning either a different paper's metadata or
// ErrDOINotFound.
//
// `?verify=` controls server policy when OpenAlex cannot confirm the
// DOI:
//
//   - `warn` (default) — store the bytes, record the failure status
//     (`doi-not-found` / `metadata-unavailable` / `unconfigured`),
//     and proceed.
//   - `strict` — reject. `doi-not-found` ⇒ 409 (contributor-correctable);
//     `metadata-unavailable` / `unconfigured` ⇒ 503 (server-side).

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/config"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/mineru"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/objstore"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/openalex"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/paperassets"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/papers"
	"github.com/pocketbase/pocketbase/core"
)

// verifyHeader is the response header carrying the DOI verification
// status (one of the papers.Verify* constants) on every DOI upload.
const verifyHeader = "X-QAtlas-Verification"

// uploadPDFByDOIHandler stores a PDF contributed against a DOI identity.
// Mirrors uploadPDFHandler but keys storage + catalog on the DOI and runs
// title/author verification against OpenAlex metadata.
func uploadPDFByDOIHandler(
	re *core.RequestEvent,
	cfg *config.Config,
	store objstore.Store,
	catalog *papers.Store,
	resolver *openalex.Resolver,
	rawDOI string,
) error {
	ctx := re.Request.Context()
	doi, ok := paperassets.ValidateDOI(rawDOI)
	if !ok {
		return re.JSON(http.StatusBadRequest, map[string]string{
			"detail": fmt.Sprintf("invalid DOI for upload: %q. Expected '10.<registrant>/<suffix>' (e.g. '10.1103/PhysRevLett.123.070501').", rawDOI),
		})
	}
	overwrite := re.Request.URL.Query().Get("overwrite") == "true"
	strict := re.Request.URL.Query().Get("verify") == "strict"
	expectedPdfSha := normaliseSha256Hex(re.Request.URL.Query().Get("expected_sha256"))

	pdfKey := paperassets.DOIAssetKey("pdf", doi)

	if err := re.Request.ParseMultipartForm(int64(paperassets.MaxPDFBytes) + 1<<20); err != nil {
		return re.JSON(http.StatusBadRequest, map[string]string{"detail": "parse multipart: " + err.Error()})
	}
	if _, has := re.Request.MultipartForm.File["pdf"]; !has {
		return re.JSON(http.StatusBadRequest, map[string]string{"detail": "missing 'pdf' multipart part"})
	}
	pdfPart, hdr, err := re.Request.FormFile("pdf")
	if err != nil {
		return re.JSON(http.StatusBadRequest, map[string]string{"detail": "open pdf part: " + err.Error()})
	}
	defer pdfPart.Close()

	contentType := hdr.Header.Get("Content-Type")
	if contentType != "" && !strings.Contains(strings.ToLower(contentType), "pdf") && contentType != "application/octet-stream" {
		return re.JSON(http.StatusUnsupportedMediaType, map[string]string{
			"detail": fmt.Sprintf("expected application/pdf for 'pdf' part, got %q", contentType),
		})
	}

	pdfStaged, vErr := stageToTmpFile(ctx, pdfPart, paperassets.MaxPDFBytes, "pdf", 5,
		func(head []byte) *uploadError {
			if len(head) < 5 || string(head[:5]) != "%PDF-" {
				return &uploadError{Status: http.StatusBadRequest,
					Detail: "uploaded file does not look like a PDF (missing %PDF- header)"}
			}
			return nil
		})
	if vErr != nil {
		return re.JSON(vErr.Status, map[string]string{"detail": vErr.Detail})
	}
	defer pdfStaged.Close()
	pdfSha := pdfStaged.Sha256()
	pdfSize := pdfStaged.Size()

	if expectedPdfSha != "" && expectedPdfSha != pdfSha {
		return re.JSON(http.StatusBadRequest, map[string]any{
			"detail":          "expected_sha256 mismatch — upload may be corrupt in transit",
			"expected_sha256": expectedPdfSha,
			"actual_sha256":   pdfSha,
		})
	}

	// DOI metadata enrichment.
	//
	// Title / authors / linked arxiv id are NEVER user-supplied — we
	// always resolve them from OpenAlex so the catalog stores the
	// canonical record. Timing is mode-dependent so we never pay an
	// OpenAlex round-trip for a pointless upload:
	//   - strict resolves BEFORE the write so a doi-not-found /
	//     metadata-unavailable blocks storage;
	//   - warn resolves AFTER the write, and only when bytes actually
	//     changed, so a no-op re-upload (sha matches) skips OpenAlex
	//     entirely — the catalog already holds the prior metadata.
	var verification papers.DOIVerification
	if strict {
		verification = verifyDOIMetadata(ctx, resolver, doi)
		if rejErr := strictReject(verification.Status); rejErr != nil {
			return re.JSON(rejErr.Status, doiVerificationRejectBody(rejErr, doi, verification))
		}
	}

	pdfOutcome, err := uploadOne(ctx, store, pdfKey, pdfStaged, "application/pdf", overwrite, "PDF")
	if err != nil {
		return re.JSON(http.StatusInternalServerError, map[string]string{"detail": err.Error()})
	}
	if pdfOutcome.kind == outcomeConflict {
		body := map[string]any{
			"detail":          "upload conflict; pass overwrite=true to replace (prior version preserved by bucket versioning when enabled)",
			"new_sha256":      pdfSha,
			"existing_path":   pdfKey,
			"existing_sha256": pdfOutcome.existingShaJSON(),
		}
		if pdfOutcome.existingSha == "" {
			body["note"] = "existing object has no sha256 metadata (legacy upload or LocalStore backend) — content equality cannot be verified without overwrite=true"
		}
		return re.JSON(http.StatusConflict, body)
	}

	requester := ""
	if cfg.UserHeader != "" {
		requester = re.Request.Header.Get(cfg.UserHeader)
	}

	if pdfOutcome.kind == outcomeUnchanged {
		// Identical bytes already stored; the catalog node — including
		// any prior verification — is unchanged. Acknowledge the no-op
		// without re-hitting OpenAlex or refreshing the node.
		slog.Info("uploaded pdf by doi (unchanged)",
			"doi", doi, "requester", requester, "pdf_sha256", pdfSha, "pdf_key", pdfKey)
		resp := map[string]any{
			"doi":           doi,
			"key":           pdfKey,
			"pdf_path":      pdfKey,
			"pdf_bytes":     pdfSize,
			"pdf_sha256":    pdfSha,
			"pdf_unchanged": true,
			"uploaded_by":   nil,
			"overwritten":   overwrite,
			"unchanged":     true,
		}
		if requester != "" {
			resp["uploaded_by"] = requester
		}
		re.Response.WriteHeader(http.StatusOK)
		return jsonBody(re, resp)
	}

	// outcomeWritten — record metadata. warn computes it now (post-
	// write); strict already has it from the pre-write blocking check.
	if !strict {
		verification = verifyDOIMetadata(ctx, resolver, doi)
	}

	catalogDeferred := false
	if err := catalog.UpsertPDFByDOI(ctx, doi, pdfSha, pdfSize, pdfOutcome.existingSha, verification); err != nil {
		if !errors.Is(err, papers.ErrCatalogUnavailable) {
			slog.Warn("papers: UpsertPDFByDOI write-through failed", "doi", doi, "error", err)
		}
		catalogDeferred = true
	}

	slog.Info("uploaded pdf by doi",
		"doi", doi,
		"requester", requester,
		"pdf_bytes", pdfSize,
		"pdf_sha256", pdfSha,
		"verification", verification.Status,
		"catalog_deferred", catalogDeferred,
		"pdf_key", pdfKey,
	)

	re.Response.Header().Set(verifyHeader, verification.Status)
	resp := map[string]any{
		"doi":           doi,
		"key":           pdfKey,
		"pdf_path":      pdfKey,
		"pdf_bytes":     pdfSize,
		"pdf_sha256":    pdfSha,
		"pdf_unchanged": false,
		"uploaded_by":   nil,
		"overwritten":   overwrite,
		"unchanged":     false,
		"verification":  verificationBody(verification),
	}
	if requester != "" {
		resp["uploaded_by"] = requester
	}
	if catalogDeferred {
		re.Response.Header().Set("X-Catalog-Sync", "deferred")
	}
	re.Response.WriteHeader(http.StatusCreated)
	return jsonBody(re, resp)
}

// uploadMinerUByDOIHandler stores a MinerU bundle (converted published
// PDF) contributed against a DOI identity. Mirrors uploadMinerUHandler
// with DOI-namespaced keys + DOI metadata verification.
func uploadMinerUByDOIHandler(
	re *core.RequestEvent,
	cfg *config.Config,
	store objstore.Store,
	catalog *papers.Store,
	resolver *openalex.Resolver,
	rawDOI string,
) error {
	ctx := re.Request.Context()
	doi, ok := paperassets.ValidateDOI(rawDOI)
	if !ok {
		return re.JSON(http.StatusBadRequest, map[string]string{
			"detail": fmt.Sprintf("invalid DOI for upload: %q. Expected '10.<registrant>/<suffix>'.", rawDOI),
		})
	}
	overwrite := re.Request.URL.Query().Get("overwrite") == "true"
	strict := re.Request.URL.Query().Get("verify") == "strict"
	expectedSha := normaliseSha256Hex(re.Request.URL.Query().Get("expected_sha256"))
	claimedPDFSha := normaliseSha256Hex(re.Request.URL.Query().Get("pdf_sha256"))
	source := re.Request.URL.Query().Get("source")
	if len(source) > 64 {
		source = source[:64]
	}

	// Cross-check the contributor's claimed source-PDF sha256 against the
	// DOI PDF currently stored. Mismatch ⇒ they converted a different PDF.
	if claimedPDFSha != "" {
		if stored := storedSha256AtKey(ctx, store, paperassets.DOIAssetKey("pdf", doi)); stored != "" && stored != claimedPDFSha {
			return re.JSON(http.StatusBadRequest, map[string]any{
				"detail":             "pdf_sha256 mismatch — the PDF you converted does not match the one stored for this DOI.",
				"claimed_pdf_sha256": claimedPDFSha,
				"catalog_pdf_sha256": stored,
			})
		}
	}

	if err := re.Request.ParseMultipartForm(int64(paperassets.MaxMineruZipBytes) + 1<<20); err != nil {
		return re.JSON(http.StatusBadRequest, map[string]string{"detail": "parse multipart: " + err.Error()})
	}
	zipPart, _, err := re.Request.FormFile("mineru_zip")
	if err != nil {
		return re.JSON(http.StatusBadRequest, map[string]string{"detail": "missing 'mineru_zip' multipart part: " + err.Error()})
	}
	defer zipPart.Close()

	zipStaged, vErr := stageInMemory(ctx, zipPart, paperassets.MaxMineruZipBytes, "mineru_zip",
		func(b []byte) *uploadError {
			if len(b) < 4 || b[0] != 'P' || b[1] != 'K' {
				return &uploadError{Status: http.StatusBadRequest, Detail: "payload is not a zip archive (missing PK signature)"}
			}
			return nil
		})
	if vErr != nil {
		return re.JSON(vErr.Status, map[string]string{"detail": vErr.Detail})
	}
	defer zipStaged.Close()
	zipSha := zipStaged.Sha256()
	zipSize := zipStaged.Size()
	if expectedSha != "" && expectedSha != zipSha {
		return re.JSON(http.StatusBadRequest, map[string]any{
			"detail":          "expected_sha256 mismatch — upload may be corrupt in transit",
			"expected_sha256": expectedSha,
			"actual_sha256":   zipSha,
		})
	}

	zipR, err := zipStaged.Open()
	if err != nil {
		return re.JSON(http.StatusInternalServerError, map[string]string{"detail": "open zip: " + err.Error()})
	}
	zipBytes, err := io.ReadAll(zipR)
	_ = zipR.Close()
	if err != nil {
		return re.JSON(http.StatusInternalServerError, map[string]string{"detail": "read zip: " + err.Error()})
	}
	result, err := mineru.ExtractResult(zipBytes)
	if err != nil {
		return re.JSON(http.StatusBadRequest, map[string]string{"detail": err.Error()})
	}
	zipBytes = nil
	_ = zipStaged.Close()

	// DOI metadata enrichment. Title / authors / linked arxiv id come
	// from OpenAlex — never from the contributor. strict resolves
	// BEFORE the writes so a doi-not-found / metadata-unavailable
	// blocks storage; warn resolves AFTER, and only when the bundle
	// actually changed, so a no-op re-upload skips OpenAlex.
	var verification papers.DOIVerification
	if strict {
		verification = verifyDOIMetadata(ctx, resolver, doi)
		if rejErr := strictReject(verification.Status); rejErr != nil {
			return re.JSON(rejErr.Status, doiVerificationRejectBody(rejErr, doi, verification))
		}
	}

	requester := ""
	if cfg.UserHeader != "" {
		requester = re.Request.Header.Get(cfg.UserHeader)
	}

	// Images first, markdown last (so any reader that sees the markdown
	// also sees every referenced image).
	imgZipKey := paperassets.DOIAssetKey("images", doi)
	// When the bundle has no images, the "images zip" is trivially
	// unchanged (nothing to upload). Setting this to true here makes
	// `allUnchanged` reduce to `md unchanged` so a no-op re-upload of
	// an image-free bundle doesn't trigger a spurious OpenAlex call.
	imgZipUnchanged := true
	imageCount := 0
	for rel := range result.Images {
		name := strings.TrimPrefix(rel, "images/")
		if name == "" || strings.Contains(name, "..") {
			continue
		}
		imageCount++
	}
	if imageCount > 0 {
		imgZipBytes, err := mineru.BuildImagesZip(result.Images)
		if err != nil {
			return re.JSON(http.StatusInternalServerError, map[string]string{"detail": "build images zip: " + err.Error()})
		}
		imgBody := newInMemoryBodyFromBytes(imgZipBytes)
		imgOutcome, err := uploadOne(ctx, store, imgZipKey, imgBody, "application/zip", overwrite, "images-zip")
		if err != nil {
			_ = imgBody.Close()
			return re.JSON(http.StatusInternalServerError, map[string]string{"detail": "upload images zip: " + err.Error()})
		}
		if imgOutcome.kind == outcomeConflict {
			body := map[string]any{
				"detail":          "images zip already exists at " + imgZipKey + " with different content; pass overwrite=true to replace",
				"existing_path":   imgZipKey,
				"new_sha256":      imgBody.Sha256(),
				"existing_sha256": imgOutcome.existingShaJSON(),
			}
			_ = imgBody.Close()
			return re.JSON(http.StatusConflict, body)
		}
		_ = imgBody.Close()
		imgZipUnchanged = imgOutcome.kind == outcomeUnchanged
	}

	mdKey := paperassets.DOIAssetKey("markdown", doi)
	mdBody := newInMemoryBodyFromBytes(result.Markdown)
	defer mdBody.Close()
	mdSha := mdBody.Sha256()
	mdSize := mdBody.Size()
	mdOutcome, err := uploadOne(ctx, store, mdKey, mdBody, "text/markdown; charset=utf-8", overwrite, "markdown")
	if err != nil {
		return re.JSON(http.StatusInternalServerError, map[string]string{"detail": err.Error()})
	}
	if mdOutcome.kind == outcomeConflict {
		body := map[string]any{
			"detail":          "markdown already exists at " + mdKey + " with different content; pass overwrite=true to replace",
			"existing_path":   mdKey,
			"new_sha256":      mdSha,
			"existing_sha256": mdOutcome.existingShaJSON(),
		}
		if mdOutcome.existingSha == "" {
			body["note"] = "existing object has no sha256 metadata (legacy upload or LocalStore backend) — content equality cannot be verified without overwrite=true"
		}
		return re.JSON(http.StatusConflict, body)
	}

	allUnchanged := mdOutcome.kind == outcomeUnchanged && imgZipUnchanged

	if allUnchanged {
		// Bundle already stored byte-for-byte; the catalog node —
		// including any prior verification — is unchanged. Skip
		// OpenAlex + the write-through.
		slog.Info("uploaded mineru bundle by doi (unchanged)",
			"doi", doi, "requester", requester, "source", source,
			"md_sha256", mdSha, "image_count", imageCount)
		resp := map[string]any{
			"doi":                  doi,
			"key":                  mdKey,
			"markdown_path":        mdKey,
			"markdown_bytes":       mdSize,
			"markdown_sha256":      mdSha,
			"markdown_unchanged":   true,
			"image_count":          imageCount,
			"zip_bytes":            zipSize,
			"zip_sha256":           zipSha,
			"source":               nil,
			"uploaded_by":          nil,
			"overwritten":          overwrite,
		}
		if imageCount > 0 {
			resp["images_zip_path"] = imgZipKey
			resp["images_zip_unchanged"] = true
		}
		if source != "" {
			resp["source"] = source
		}
		if requester != "" {
			resp["uploaded_by"] = requester
		}
		re.Response.WriteHeader(http.StatusOK)
		return jsonBody(re, resp)
	}

	// Something was written — record metadata. warn computes it now
	// (post-write); strict already has it from the pre-write check.
	if !strict {
		verification = verifyDOIMetadata(ctx, resolver, doi)
	}

	catalogDeferred := false
	if err := catalog.UpsertMDByDOI(ctx, doi, mdSha, mdSize, mdOutcome.existingSha, imageCount, verification); err != nil {
		if !errors.Is(err, papers.ErrCatalogUnavailable) {
			slog.Warn("papers: UpsertMDByDOI write-through failed", "doi", doi, "error", err)
		}
		catalogDeferred = true
	}

	slog.Info("uploaded mineru bundle by doi",
		"doi", doi,
		"requester", requester,
		"source", source,
		"md_sha256", mdSha,
		"image_count", imageCount,
		"verification", verification.Status,
		"catalog_deferred", catalogDeferred,
	)

	re.Response.Header().Set(verifyHeader, verification.Status)
	resp := map[string]any{
		"doi":                  doi,
		"key":                  mdKey,
		"markdown_path":        mdKey,
		"markdown_bytes":       mdSize,
		"markdown_sha256":      mdSha,
		"markdown_unchanged":   mdOutcome.kind == outcomeUnchanged,
		"image_count":          imageCount,
		"zip_bytes":            zipSize,
		"zip_sha256":           zipSha,
		"source":               nil,
		"uploaded_by":          nil,
		"overwritten":          overwrite,
		"verification":         verificationBody(verification),
	}
	if imageCount > 0 {
		resp["images_zip_path"] = imgZipKey
		resp["images_zip_unchanged"] = imgZipUnchanged
	}
	if source != "" {
		resp["source"] = source
	}
	if requester != "" {
		resp["uploaded_by"] = requester
	}
	if catalogDeferred {
		re.Response.Header().Set("X-Catalog-Sync", "deferred")
	}
	re.Response.WriteHeader(http.StatusCreated)
	return jsonBody(re, resp)
}

// ---------------------------------------------------------------------------
// Verification helpers
// ---------------------------------------------------------------------------

// verifyDOIMetadata resolves the DOI's OpenAlex metadata and returns it for
// catalog enrichment. Title / authors / linked arxiv id are NEVER taken from
// the contributor — they always come from OpenAlex, so a contributor cannot
// override the recorded metadata. The function never errors: every failure
// mode is encoded as a Status (so the outcome is always recordable). Caller
// decides whether a given status blocks under strict mode (see strictReject).
//
// Statuses returned:
//   - VerifyVerified       — OpenAlex returned a record; Title/Authors/ArxivID
//     populated.
//   - VerifyDOINotFound    — OpenAlex confirmed the DOI does not exist.
//   - VerifyUnavailable    — OpenAlex was unreachable / errored.
//   - VerifyUnconfigured   — server has no OpenAlex mailto, lookups disabled.
func verifyDOIMetadata(ctx context.Context, resolver *openalex.Resolver, doi string) papers.DOIVerification {
	if resolver == nil || !resolver.Enabled() {
		return papers.DOIVerification{Status: papers.VerifyUnconfigured}
	}
	meta, err := resolver.LookupMetadata(ctx, doi)
	if err != nil {
		if errors.Is(err, openalex.ErrDOINotFound) {
			return papers.DOIVerification{Status: papers.VerifyDOINotFound}
		}
		return papers.DOIVerification{Status: papers.VerifyUnavailable}
	}
	return papers.DOIVerification{
		Status:  papers.VerifyVerified,
		Title:   meta.Title,
		Authors: meta.Authors,
		ArxivID: meta.ArxivID,
	}
}

// strictReject returns the uploadError to emit when the verification
// status warrants blocking under strict mode, or nil to proceed.
//
//   - doi-not-found → 409 (contributor-correctable: typo'd DOI)
//   - metadata-unavailable / unconfigured → 503 (server-side, can't verify)
//   - verified → proceed
//
// The strict-mode gate lives at the call site: this function is only
// called when `strict` is true (the upload's `?verify=strict` flag).
// Keeping the bool out of the signature forces that gate to be explicit
// and makes "did we mean to reject this?" grep-able.
func strictReject(status string) *uploadError {
	switch status {
	case papers.VerifyDOINotFound:
		return &uploadError{Status: http.StatusConflict, Detail: "DOI not found in OpenAlex — cannot verify the contribution under verify=strict"}
	case papers.VerifyUnavailable, papers.VerifyUnconfigured:
		return &uploadError{Status: http.StatusServiceUnavailable, Detail: "DOI metadata verification unavailable (" + status + ") — required by verify=strict; retry later or drop verify=strict"}
	default:
		return nil
	}
}

// verificationBody renders the verification result for the JSON response.
func verificationBody(v papers.DOIVerification) map[string]any {
	body := map[string]any{
		"status":   v.Status,
		"title":    nil,
		"authors":  nil,
		"arxiv_id": nil,
	}
	if v.Title != "" {
		body["title"] = v.Title
	}
	if len(v.Authors) > 0 {
		body["authors"] = v.Authors
	}
	if v.ArxivID != "" {
		body["arxiv_id"] = v.ArxivID
	}
	return body
}

// doiVerificationRejectBody builds the 409/503 body for a strict-mode
// rejection. We expose the DOI and the resolution status; there is no
// "expected" anything to surface because the contributor never supplies
// metadata — the check is purely "does OpenAlex resolve this DOI?".
func doiVerificationRejectBody(rej *uploadError, doi string, v papers.DOIVerification) map[string]any {
	body := map[string]any{
		"detail":              rej.Detail,
		"doi":                 doi,
		"verification_status": v.Status,
	}
	if v.Title != "" {
		body["found_title"] = v.Title
	}
	if len(v.Authors) > 0 {
		body["found_authors"] = v.Authors
	}
	return body
}

// storedSha256AtKey returns the lower-cased sha256 user-metadata of the
// object at key, or "" when absent / unreadable.
func storedSha256AtKey(ctx context.Context, store objstore.Store, key string) string {
	if key == "" {
		return ""
	}
	info, ok, err := store.Stat(ctx, key)
	if err != nil || !ok || info.Metadata == nil {
		return ""
	}
	return strings.ToLower(info.Metadata["sha256"])
}

// getMarkdownByDOIHandler answers GET /api/papers/<doi>/markdown for
// a DOI-only contribution (published paper with no arxiv presence).
// Resolved to this handler when the OpenAlex resolver returns
// ErrDOINotFound but the local catalog has a DOI node — meaning the
// stored bytes are the only source of truth. Streams the markdown
// object directly from the "<kind>/doi/<reg>/<suffix>" bucket layout.
func getMarkdownByDOIHandler(
	re *core.RequestEvent,
	cfg *config.Config,
	store objstore.Store,
	converter *mineru.Converter,
	rawDOI string,
) error {
	doi, ok := paperassets.ValidateDOI(rawDOI)
	if !ok {
		return re.JSON(http.StatusBadRequest, map[string]string{
			"detail": fmt.Sprintf("invalid DOI for markdown: %q", rawDOI),
		})
	}
	ctx := re.Request.Context()
	mdKey := paperassets.DOIAssetKey("markdown", doi)
	if mdKey == "" {
		return re.JSON(http.StatusInternalServerError, map[string]string{
			"detail": "could not compute markdown key for DOI",
		})
	}
	rc, info, err := store.Get(ctx, mdKey)
	if err != nil {
		if errors.Is(err, objstore.ErrNotFound) {
			return re.JSON(http.StatusNotFound, map[string]string{
				"detail": fmt.Sprintf("markdown not found for DOI %s", doi),
				"doi":    doi,
			})
		}
		return re.JSON(http.StatusInternalServerError, map[string]string{
			"detail": "fetch markdown: " + err.Error(),
		})
	}
	defer rc.Close()
	re.Response.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	if info.Size > 0 {
		re.Response.Header().Set("Content-Length", strconv.FormatInt(info.Size, 10))
	}
	re.Response.Header().Set("X-QAtlas-DOI", doi)
	re.Response.WriteHeader(http.StatusOK)
	if _, err := io.Copy(re.Response, rc); err != nil {
		slog.Warn("doi markdown: stream copy failed", "doi", doi, "error", err)
	}
	return nil
}

// getPDFByDOIHandler answers GET /api/papers/<doi>/pdf for a DOI-only
// contribution. Same dispatch rationale as getMarkdownByDOIHandler.
// Streams the PDF bytes directly from the "<kind>/doi/<reg>/<suffix>"
// bucket layout.
func getPDFByDOIHandler(
	re *core.RequestEvent,
	cfg *config.Config,
	store objstore.Store,
	converter *mineru.Converter,
	rawDOI string,
) error {
	doi, ok := paperassets.ValidateDOI(rawDOI)
	if !ok {
		return re.JSON(http.StatusBadRequest, map[string]string{
			"detail": fmt.Sprintf("invalid DOI for pdf: %q", rawDOI),
		})
	}
	ctx := re.Request.Context()
	pdfKey := paperassets.DOIAssetKey("pdf", doi)
	if pdfKey == "" {
		return re.JSON(http.StatusInternalServerError, map[string]string{
			"detail": "could not compute pdf key for DOI",
		})
	}
	rc, info, err := store.Get(ctx, pdfKey)
	if err != nil {
		if errors.Is(err, objstore.ErrNotFound) {
			return re.JSON(http.StatusNotFound, map[string]string{
				"detail": fmt.Sprintf("pdf not found for DOI %s", doi),
				"doi":    doi,
			})
		}
		return re.JSON(http.StatusInternalServerError, map[string]string{
			"detail": "fetch pdf: " + err.Error(),
		})
	}
	defer rc.Close()
	re.Response.Header().Set("Content-Type", "application/pdf")
	if info.Size > 0 {
		re.Response.Header().Set("Content-Length", strconv.FormatInt(info.Size, 10))
	}
	re.Response.Header().Set("X-QAtlas-DOI", doi)
	re.Response.WriteHeader(http.StatusOK)
	if _, err := io.Copy(re.Response, rc); err != nil {
		slog.Warn("doi pdf: stream copy failed", "doi", doi, "error", err)
	}
	return nil
}

// probeDOIAssetReadiness mirrors probeAssetReadiness for the DOI bucket
// layout. A DOI's PDF / markdown live at deterministic paths
// (paperassets.DOIAssetKey), so a single Stat per kind tells us whether
// the bytes are on disk. Unlike the arxiv path there is no MinerU job
// state to surface — DOI uploads come pre-converted via the
// upload-mineru zip endpoint, so the paper is either fully cached or
// not present yet.
//
// Returns (pdfReady, mdReady). A nil store argument or invalid DOI both
// report (false, false); callers validate the DOI separately and emit
// 400 / 500 there.
func probeDOIAssetReadiness(ctx context.Context, store objstore.Store, doi string) (pdfReady, mdReady bool) {
	if store == nil {
		return false, false
	}
	if pdfKey := paperassets.DOIAssetKey("pdf", doi); pdfKey != "" {
		if _, exists, err := store.Stat(ctx, pdfKey); err == nil && exists {
			pdfReady = true
		}
	}
	if mdKey := paperassets.DOIAssetKey("markdown", doi); mdKey != "" {
		if _, exists, err := store.Stat(ctx, mdKey); err == nil && exists {
			mdReady = true
		}
	}
	return pdfReady, mdReady
}

// markdownStatusByDOIHandler answers GET /api/papers/<doi>/markdown/status
// for a DOI-indexed contribution. It mirrors markdownStatusHandler's
// arxiv contract — same JSON shape with `doi` replacing `arxiv_id` —
// so a generic client that swaps an arxiv id for a DOI in the same URL
// template gets symmetric behaviour. There is no MinerU job state to
// report (DOI uploads arrive pre-converted), so the response is a
// straightforward "cached" / "missing" snapshot derived from the
// bucket layout.
func markdownStatusByDOIHandler(re *core.RequestEvent, store objstore.Store, rawDOI string) error {
	doi, ok := paperassets.ValidateDOI(rawDOI)
	if !ok {
		return re.JSON(http.StatusBadRequest, map[string]string{
			"detail": fmt.Sprintf("invalid DOI for markdown/status: %q", rawDOI),
		})
	}
	ctx := re.Request.Context()
	pdfReady, mdReady := probeDOIAssetReadiness(ctx, store, doi)
	re.Response.Header().Set("X-QAtlas-DOI", doi)
	if mdReady {
		return re.JSON(http.StatusOK, map[string]any{
			"doi":          doi,
			"state":        "cached",
			"phase":        string(mineru.PhaseReady),
			"pdf_ready":    pdfReady,
			"md_ready":     true,
			"markdown_url": "/api/papers/" + doi + "/markdown",
		})
	}
	// No markdown bytes: this DOI hasn't had a MinerU bundle uploaded
	// yet. Distinct from arxiv, the server can't kick off a fetch
	// itself (no PDF source URL and no autoconvert path for DOIs).
	return re.JSON(http.StatusOK, map[string]any{
		"doi":       doi,
		"state":     "missing",
		"pdf_ready": pdfReady,
		"md_ready":  false,
	})
}

// pdfStatusByDOIHandler answers GET /api/papers/<doi>/pdf/status. Same
// rationale as markdownStatusByDOIHandler — keep symmetry with the
// arxiv pdfStatusHandler so URL templates that swap an id for a DOI
// keep working.
func pdfStatusByDOIHandler(re *core.RequestEvent, store objstore.Store, rawDOI string) error {
	doi, ok := paperassets.ValidateDOI(rawDOI)
	if !ok {
		return re.JSON(http.StatusBadRequest, map[string]string{
			"detail": fmt.Sprintf("invalid DOI for pdf/status: %q", rawDOI),
		})
	}
	ctx := re.Request.Context()
	pdfReady, mdReady := probeDOIAssetReadiness(ctx, store, doi)
	re.Response.Header().Set("X-QAtlas-DOI", doi)
	if pdfReady {
		return re.JSON(http.StatusOK, map[string]any{
			"doi":       doi,
			"state":     "cached",
			"phase":     string(mineru.PhaseReady),
			"pdf_ready": true,
			"md_ready":  mdReady,
			"pdf_url":   "/api/papers/" + doi + "/pdf",
		})
	}
	return re.JSON(http.StatusOK, map[string]any{
		"doi":       doi,
		"state":     "missing",
		"pdf_ready": false,
		"md_ready":  mdReady,
	})
}
