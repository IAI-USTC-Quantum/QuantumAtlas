package routes

// papers_doi.go: DOI-indexed contribution + fetch handlers.
//
// upload-pdf and upload-mineru accept EITHER an arxiv id OR a DOI in the
// {arxiv_id} path slot. When the id matches the DOI shape (10.<reg>/...)
// the POST dispatcher routes here. A DOI contribution stores a PUBLISHED
// version (which may have no arXiv preprint) under the disjoint
// "<kind>/doi/..." namespace and records it in the catalog under a
// "doi:<doi>" node.
//
// The GET side (getMarkdownByDOIHandler / markdownStatusByDOIHandler)
// additionally implements the plan §A fetch semantics: any DOI can be
// completed server-side — a stored contributed PDF is converted on
// demand, and when OpenAlex surfaced an OA PDF URL for a published-only
// work the converter fetches it first (mineru.Converter.EnsureByDOI).
// Cache misses on /markdown therefore return a 202 LRO instead of a
// bare 404; 404 remains for DOIs with no stored PDF and no OA source.
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
	"net/http"
	"strings"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/config"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/mineru"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/objstore"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/openalex"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/paperassets"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/registry"
	"github.com/pocketbase/pocketbase/core"
)

// Verification statuses reported on a DOI contribution (the
// X-QAtlas-Verification response header).
//
// Title is taken from OpenAlex, never the contributor; the status records
// whether that resolution succeeded.
const (
	// VerifyVerified: OpenAlex returned a record for the DOI; Title /
	// ArxivID populated from the canonical metadata.
	VerifyVerified = "verified"
	// VerifyDOINotFound: OpenAlex confirmed the DOI does not exist.
	VerifyDOINotFound = "doi-not-found"
	// VerifyUnavailable: OpenAlex was unreachable / errored.
	VerifyUnavailable = "metadata-unavailable"
	// VerifyUnconfigured: the server has no OpenAlex mailto configured,
	// so DOI metadata enrichment is disabled.
	VerifyUnconfigured = "unconfigured"
)

// DOIVerification is the outcome of upload-time DOI metadata enrichment
// against OpenAlex. Title / Authors / ArxivID are populated only when
// Status == VerifyVerified. The verified fields feed the registry's
// PaperRef (ResolveOrMint uses them for identity backfill); the status
// itself is response-only.
type DOIVerification struct {
	Status  string   // one of the Verify* constants
	Title   string   // OpenAlex canonical title (only set on verified)
	Authors []string // OpenAlex author display names
	ArxivID string   // linked arxiv id when OpenAlex knows one, else ""
}

// verificationRef builds the registry.PaperRef a verified (or
// unverified) DOI contribution resolves/mints through. Title / authors /
// linked arXiv id are only present when OpenAlex verified the DOI.
func verificationRef(doi string, v DOIVerification) registry.PaperRef {
	return registry.PaperRef{
		DOI:     doi,
		ArxivID: v.ArxivID,
		Title:   v.Title,
		Authors: v.Authors,
	}
}

// verifyHeader is the response header carrying the DOI verification
// status (one of the Verify* constants) on every DOI upload.
const verifyHeader = "X-QAtlas-Verification"

// DOI uploads use the same immutable source and complete bundle protocol.
func uploadPDFByDOIHandler(re *core.RequestEvent, cfg *config.Config, store objstore.Store, catalog *registry.Store, resolver *openalex.Resolver, rawDOI string) error {
	return uploadFrozenPDF(re, cfg, store, catalog, resolver, rawDOI, true)
}

func uploadMinerUByDOIHandler(re *core.RequestEvent, cfg *config.Config, store objstore.Store, catalog *registry.Store, resolver *openalex.Resolver, rawDOI string) error {
	doi, ok := paperassets.ValidateDOI(rawDOI)
	if !ok {
		return re.JSON(http.StatusBadRequest, map[string]string{"detail": "invalid DOI"})
	}
	if re.Request.URL.Query().Get("verify") == "strict" {
		v := verifyDOIMetadata(re.Request.Context(), resolver, doi)
		if rejected := strictReject(v.Status); rejected != nil {
			return re.JSON(rejected.Status, doiVerificationRejectBody(rejected, doi, v))
		}
		re.Response.Header().Set(verifyHeader, v.Status)
	}
	return uploadFrozenMinerU(re, cfg, store, catalog, doi, true)
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
func verifyDOIMetadata(ctx context.Context, resolver *openalex.Resolver, doi string) DOIVerification {
	if resolver == nil || !resolver.Enabled() {
		return DOIVerification{Status: VerifyUnconfigured}
	}
	meta, err := resolver.LookupMetadata(ctx, doi)
	if err != nil {
		if errors.Is(err, openalex.ErrDOINotFound) {
			return DOIVerification{Status: VerifyDOINotFound}
		}
		return DOIVerification{Status: VerifyUnavailable}
	}
	return DOIVerification{
		Status:  VerifyVerified,
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
	case VerifyDOINotFound:
		return &uploadError{Status: http.StatusConflict, Detail: "DOI not found in OpenAlex — cannot verify the contribution under verify=strict"}
	case VerifyUnavailable, VerifyUnconfigured:
		return &uploadError{Status: http.StatusServiceUnavailable, Detail: "DOI metadata verification unavailable (" + status + ") — required by verify=strict; retry later or drop verify=strict"}
	default:
		return nil
	}
}

// verificationBody renders the verification result for the JSON response.
func verificationBody(v DOIVerification) map[string]any {
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
func doiVerificationRejectBody(rej *uploadError, doi string, v DOIVerification) map[string]any {
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

// DOI content uses source selection and immutable bundle readiness too.
func getMarkdownByDOIHandler(re *core.RequestEvent, cfg *config.Config, store objstore.Store, converter *mineru.Converter, rawDOI, oaPdfURL string) error {
	catalog, ok, err := legacyContentCatalog(re, cfg, converter)
	if !ok {
		return err
	}
	return contentDerivativeHandler(re, cfg, store, catalog, converter, rawDOI, "markdown")
}

// doiSnapshotBody renders a converter Job for the DOI markdown/status
// endpoints: the same shape as snapshotBody (state / phase / fetch /
// convert / queue sub-objects) but keyed by `doi` instead of
// `arxiv_id`, so the two poll surfaces stay symmetric.
func doiSnapshotBody(doi string, job *mineru.Job) map[string]any {
	body := snapshotBody(doi, job)
	delete(body, "arxiv_id")
	body["doi"] = doi
	return body
}

func getPDFByDOIHandler(re *core.RequestEvent, cfg *config.Config, store objstore.Store, converter *mineru.Converter, rawDOI string) error {
	catalog, ok, err := legacyContentCatalog(re, cfg, converter)
	if !ok {
		return err
	}
	return contentPDFHandler(re, cfg, store, catalog, converter, rawDOI)
}

func markdownStatusByDOIHandler(re *core.RequestEvent, store objstore.Store, converter *mineru.Converter, rawDOI string) error {
	cfg := &config.Config{PaperAccessEnabled: true}
	catalog, ok, err := legacyContentCatalog(re, cfg, converter)
	if !ok {
		return err
	}
	return contentReadStatusHandler(re, cfg, store, catalog, converter, rawDOI)
}

func pdfStatusByDOIHandler(re *core.RequestEvent, store objstore.Store, rawDOI string) error {
	if _, ok := paperassets.ValidateDOI(rawDOI); !ok {
		return re.JSON(http.StatusBadRequest, map[string]string{"detail": "invalid DOI"})
	}
	return re.JSON(http.StatusServiceUnavailable, map[string]string{"detail": "content catalog required; use authenticated PDF status route"})
}

func probeDOIAssetReadiness(ctx context.Context, store objstore.Store, doi string) (pdfReady, mdReady bool, err error) {
	if store == nil {
		return false, false, nil
	}
	if _, valid := paperassets.ValidateDOI(doi); !valid {
		return false, false, nil
	}
	_, pdfReady, err = store.Stat(ctx, paperassets.DOIAssetKey("pdf", doi))
	return pdfReady, false, err
}
