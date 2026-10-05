package routes

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/config"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/objstore"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/openalex"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/paperassets"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/paperbundle"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/registry"
	"github.com/pocketbase/pocketbase/core"
)

// Frozen uploads require durable catalog identity. Unlike the old writer, they
// cannot acknowledge a deferred catalog write or replace an immutable alias.
type frozenPDFUploadCatalog interface {
	ResolveOrMint(context.Context, registry.PaperRef) (string, bool, error)
	GetImportedPaperSource(context.Context, string, string) (registry.PaperSource, bool, error)
	FreezePaperSource(context.Context, objstore.Store, registry.PaperSource) (registry.PaperSource, error)
	RegisterFrozenPDF(context.Context, objstore.Store, string, string, []byte) (registry.PaperSource, error)
	BindPaperSourceImport(context.Context, objstore.Store, string, string, string) (registry.PaperSource, error)
	UpsertPDF(context.Context, registry.PaperRef, int, string, int64, string) (string, int64, error)
	UpsertPDFByDOI(context.Context, registry.PaperRef, string, int64, string) (string, int64, error)
}

func uploadFrozenPDF(re *core.RequestEvent, cfg *config.Config, store objstore.Store, catalog frozenPDFUploadCatalog, resolver *openalex.Resolver, id string, doi bool) error {
	var ok bool
	if doi {
		id, ok = paperassets.ValidateDOI(id)
	} else {
		id, ok = paperassets.ValidateUploadID(id)
	}
	if !ok {
		return re.JSON(http.StatusBadRequest, map[string]string{"detail": "invalid paper identity; arXiv uploads require an explicit version"})
	}
	ctx := re.Request.Context()
	if err := re.Request.ParseMultipartForm(int64(paperassets.MaxPDFBytes) + (1 << 20)); err != nil {
		return re.JSON(http.StatusBadRequest, map[string]string{"detail": "parse multipart: " + err.Error()})
	}
	if re.Request.MultipartForm != nil {
		defer re.Request.MultipartForm.RemoveAll()
	}
	part, hdr, err := re.Request.FormFile("pdf")
	if err != nil {
		return re.JSON(http.StatusBadRequest, map[string]string{"detail": "missing pdf multipart part"})
	}
	defer part.Close()
	ct := hdr.Header.Get("Content-Type")
	if ct != "" && !strings.Contains(strings.ToLower(ct), "pdf") && ct != "application/octet-stream" {
		return re.JSON(http.StatusUnsupportedMediaType, map[string]string{"detail": "expected application/pdf"})
	}
	staged, uploadErr := stageToTmpFile(ctx, part, paperassets.MaxPDFBytes, "pdf", 5, func(head []byte) *uploadError {
		if len(head) < 5 || string(head[:5]) != "%PDF-" {
			return &uploadError{Status: http.StatusBadRequest, Detail: "uploaded file does not look like a PDF (missing %PDF- header)"}
		}
		return nil
	})
	if uploadErr != nil {
		return re.JSON(uploadErr.Status, map[string]string{"detail": uploadErr.Detail})
	}
	defer staged.Close()
	sha := staged.Sha256()
	if expected := normaliseSha256Hex(re.Request.URL.Query().Get("expected_sha256")); expected != "" && expected != sha {
		return re.JSON(http.StatusBadRequest, map[string]any{"detail": "expected_sha256 mismatch", "expected_sha256": expected, "actual_sha256": sha})
	}
	verification := DOIVerification{Status: VerifyUnconfigured}
	ref := registry.PaperRef{ArxivID: id}
	origin, alias := "arxiv:v"+fmt.Sprint(registry.ArxivVersionOf(id)), paperassets.AssetKey("pdf", id)
	if doi {
		verification = verifyDOIMetadata(ctx, resolver, id)
		if re.Request.URL.Query().Get("verify") == "strict" {
			if reject := strictReject(verification.Status); reject != nil {
				return re.JSON(reject.Status, doiVerificationRejectBody(reject, id, verification))
			}
		}
		ref = verificationRef(id, verification)
		origin, alias = "doi:"+id, paperassets.DOIAssetKey("pdf", id)
	}
	if catalog == nil || store == nil {
		return re.JSON(http.StatusServiceUnavailable, map[string]string{"detail": "content catalog or object store unavailable"})
	}
	paperID, _, err := catalog.ResolveOrMint(ctx, ref)
	if err != nil {
		return contentAccessErrorResponse(re, err)
	}
	prior, found, err := catalog.GetImportedPaperSource(ctx, paperID, alias)
	if err != nil {
		return contentAccessErrorResponse(re, err)
	}
	var src registry.PaperSource
	if found {
		if prior.Sha256 != sha {
			return re.JSON(http.StatusConflict, map[string]any{"detail": "this identifier is bound to a frozen PDF; overwrite cannot replace it", "paper_id": paperID, "source_id": prior.SourceID, "existing_sha256": prior.Sha256, "new_sha256": sha})
		}
		src, err = catalog.FreezePaperSource(ctx, store, prior)
	} else {
		reader, openErr := staged.Open()
		if openErr != nil {
			return contentAccessErrorResponse(re, openErr)
		}
		pdf, readErr := io.ReadAll(reader)
		_ = reader.Close()
		if readErr != nil {
			return contentAccessErrorResponse(re, readErr)
		}
		src, err = catalog.RegisterFrozenPDF(ctx, store, paperID, origin, pdf)
		if err == nil {
			src, err = catalog.BindPaperSourceImport(ctx, store, paperID, src.SourceID, alias)
		}
	}
	if errors.Is(err, paperbundle.ErrIntegrity) {
		// Another upload may have won the canonical alias while this request
		// persisted its separate immutable source. Never overwrite the winner.
		if bound, ok, lookupErr := catalog.GetImportedPaperSource(ctx, paperID, alias); lookupErr == nil && ok && bound.Sha256 != sha {
			return re.JSON(http.StatusConflict, map[string]any{"detail": "this identifier is bound to a frozen PDF; overwrite cannot replace it", "paper_id": paperID, "source_id": bound.SourceID, "existing_sha256": bound.Sha256, "new_sha256": sha})
		}
	}
	if err != nil {
		return contentAccessErrorResponse(re, err)
	}
	// Metadata is a locator only; outputs are never synthesized into old buckets.
	if doi {
		_, _, err = catalog.UpsertPDFByDOI(ctx, ref, src.Sha256, src.SizeBytes, src.ObjstoreKey)
	} else {
		_, _, err = catalog.UpsertPDF(ctx, ref, registry.ArxivVersionOf(id), src.Sha256, src.SizeBytes, src.ObjstoreKey)
	}
	if err != nil {
		return contentAccessErrorResponse(re, err)
	}
	setContentSourceHeaders(re, paperResolution(id, paperID), src)
	body := map[string]any{"paper_id": paperID, "source_id": src.SourceID, "pdf_path": src.ObjstoreKey, "pdf_bytes": src.SizeBytes, "pdf_sha256": src.Sha256, "pdf_unchanged": found, "unchanged": found, "overwritten": false, "pdf_url": "/api/papers/" + paperID + "/sources/" + src.SourceID + "/pdf"}
	if doi {
		body["doi"] = id
		body["verification"] = verificationBody(verification)
		re.Response.Header().Set(verifyHeader, verification.Status)
	} else {
		body["arxiv_id"] = id
	}
	if cfg != nil && cfg.UserHeader != "" {
		body["uploaded_by"] = re.Request.Header.Get(cfg.UserHeader)
	}
	status := http.StatusCreated
	if found {
		status = http.StatusOK
	}
	return re.JSON(status, body)
}

func uploadFrozenMinerU(re *core.RequestEvent, cfg *config.Config, store objstore.Store, catalog mineruIngestCatalog, id string, doi bool) error {
	var ok bool
	if doi {
		id, ok = paperassets.ValidateDOI(id)
	} else {
		id, ok = paperassets.ValidateUploadID(id)
	}
	if !ok {
		return re.JSON(http.StatusBadRequest, map[string]string{"detail": "invalid paper identity; arXiv uploads require an explicit version"})
	}
	ctx := re.Request.Context()
	if err := re.Request.ParseMultipartForm(int64(paperassets.MaxMineruZipBytes) + (1 << 20)); err != nil {
		return re.JSON(http.StatusBadRequest, map[string]string{"detail": "parse multipart: " + err.Error()})
	}
	if re.Request.MultipartForm != nil {
		defer re.Request.MultipartForm.RemoveAll()
	}
	part, _, err := re.Request.FormFile("mineru_zip")
	if err != nil {
		return re.JSON(http.StatusBadRequest, map[string]string{"detail": "missing mineru_zip multipart part"})
	}
	defer part.Close()
	staged, uploadErr := stageInMemory(ctx, part, paperassets.MaxMineruZipBytes, "mineru_zip", func(data []byte) *uploadError {
		if len(data) < 4 || string(data[:2]) != "PK" {
			return &uploadError{Status: http.StatusBadRequest, Detail: "payload is not a zip archive (missing PK signature)"}
		}
		return nil
	})
	if uploadErr != nil {
		return re.JSON(uploadErr.Status, map[string]string{"detail": uploadErr.Detail})
	}
	defer staged.Close()
	if expected := normaliseSha256Hex(re.Request.URL.Query().Get("expected_sha256")); expected != "" && expected != staged.Sha256() {
		return re.JSON(http.StatusBadRequest, map[string]string{"detail": "expected_sha256 mismatch", "actual_sha256": staged.Sha256()})
	}
	r, err := staged.Open()
	if err != nil {
		return contentAccessErrorResponse(re, err)
	}
	archive, err := io.ReadAll(r)
	_ = r.Close()
	if err != nil {
		return contentAccessErrorResponse(re, err)
	}
	requester := ""
	if cfg != nil && cfg.UserHeader != "" {
		requester = re.Request.Header.Get(cfg.UserHeader)
	}
	return ingestMinerUNewFormat(re, store, catalog, id, archive, normaliseSha256Hex(re.Request.URL.Query().Get("pdf_sha256")), normaliseTierParam(re.Request.URL.Query().Get("tier")), requester, re.Request.URL.Query().Get("source"))
}

// frozenUploadCatalog is intentionally narrow enough for offline intake tests.
type frozenUploadCatalog interface {
	GetPaperIDByIdentity(context.Context, string, string) (string, bool, error)
	GetPaperSource(context.Context, string, string) (registry.PaperSource, bool, error)
	GetImportedPaperSource(context.Context, string, string) (registry.PaperSource, bool, error)
	ListPaperSources(context.Context, string) ([]registry.PaperSource, error)
	FreezePaperSource(context.Context, objstore.Store, registry.PaperSource) (registry.PaperSource, error)
	RegisterFrozenPaperSource(context.Context, objstore.Store, string, string, string) (registry.PaperSource, error)
	BindPaperSourceImport(context.Context, objstore.Store, string, string, string) (registry.PaperSource, error)
	PublishBundle(context.Context, objstore.Store, registry.ParseRevision, bool) (registry.ParseBundle, error)
}

func frozenUploadError(re *core.RequestEvent, err error) error {
	if errors.Is(err, registry.ErrCatalogUnavailable) {
		return re.JSON(http.StatusServiceUnavailable, map[string]string{"detail": "content catalog unavailable"})
	}
	return contentAccessErrorResponse(re, err)
}
