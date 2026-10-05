package routes

import (
	"encoding/json"
	"fmt"
	"net/http"
	"path"
	"strings"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/mineru"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/objstore"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/paperassets"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/paperbundle"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/registry"
	"github.com/pocketbase/pocketbase/core"
)

type mineruIngestCatalog interface{ frozenUploadCatalog }

var _ mineruIngestCatalog = (*registry.Store)(nil)

// Intake shares the same complete-package publication protocol as hosted V1
// parsing. Every original member is retained verbatim; the ZIP container need
// not be stored. MD-only output is not a usable source-bound parse revision.
func ingestMinerUNewFormat(re *core.RequestEvent, store objstore.Store, catalog mineruIngestCatalog, canonical string, zipBytes []byte, claimedPDFSha, tierParam, requester, source string) error {
	ctx := re.Request.Context()
	result, err := mineru.ExtractPackage(zipBytes)
	if err != nil {
		return re.JSON(http.StatusUnprocessableEntity, map[string]string{"detail": "complete supported Middle + markdown package required: " + err.Error()})
	}
	doc, err := mineru.ParseMiddleJSON(result.MiddleJSON)
	if err != nil {
		return re.JSON(http.StatusUnprocessableEntity, map[string]string{"detail": "Middle rejected: " + err.Error()})
	}
	if catalog == nil || store == nil {
		return re.JSON(http.StatusServiceUnavailable, map[string]string{"detail": "content catalog or object store unavailable"})
	}
	scheme, origin, alias := "arxiv", "arxiv:v"+fmt.Sprint(registry.ArxivVersionOf(canonical)), paperassets.AssetKey("pdf", canonical)
	if isDOICandidate(canonical) {
		scheme, origin, alias = "doi", "doi:"+canonical, paperassets.DOIAssetKey("pdf", canonical)
	}
	paperID, found, err := catalog.GetPaperIDByIdentity(ctx, scheme, canonical)
	if err != nil {
		return frozenUploadError(re, err)
	}
	if !found {
		return re.JSON(http.StatusNotFound, map[string]string{"detail": "upload the exact PDF before contributing parse output"})
	}
	sourceID := re.Request.URL.Query().Get("source_id")
	var src registry.PaperSource
	if sourceID != "" {
		src, found, err = catalog.GetPaperSource(ctx, paperID, sourceID)
		if err != nil {
			return frozenUploadError(re, err)
		}
		if !found {
			return re.JSON(http.StatusNotFound, map[string]string{"detail": "source not found under this paper"})
		}
		if scheme == "arxiv" && !sourceVersionMatches(src.Origin, "v"+fmt.Sprint(registry.ArxivVersionOf(canonical))) {
			imported, proven, getErr := catalog.GetImportedPaperSource(ctx, paperID, alias)
			if getErr != nil {
				return frozenUploadError(re, getErr)
			}
			if !proven || imported.SourceID != src.SourceID || imported.Sha256 != src.Sha256 {
				return re.JSON(http.StatusConflict, map[string]string{"detail": "source does not match requested arXiv version; refusing substitution"})
			}
		}
		src, err = catalog.FreezePaperSource(ctx, store, src)
	} else {
		src, found, err = catalog.GetImportedPaperSource(ctx, paperID, alias)
		if err != nil {
			return frozenUploadError(re, err)
		}
		if found {
			src, err = catalog.FreezePaperSource(ctx, store, src)
		} else {
			// First reuse an already frozen source for this semantic edition; legacy
			// PDF discovery is the last resort, never a readiness/output cache.
			sources, listErr := catalog.ListPaperSources(ctx, paperID)
			if listErr != nil {
				return frozenUploadError(re, listErr)
			}
			for i := len(sources) - 1; i >= 0; i-- {
				candidate := sources[i]
				matches := scheme == "arxiv" && sourceVersionMatches(candidate.Origin, "v"+fmt.Sprint(registry.ArxivVersionOf(canonical))) || scheme == "doi" && (candidate.Origin == origin || strings.HasPrefix(candidate.Origin, "published"))
				if matches {
					src, err = catalog.FreezePaperSource(ctx, store, candidate)
					found = true
					break
				}
			}
			if !found {
				key, exists := alias, false
				if scheme == "arxiv" {
					key, _, exists, err = paperassets.LocateAssetByID(ctx, store, "pdf", canonical)
				} else {
					_, exists, err = store.Stat(ctx, key)
				}
				if err != nil {
					return frozenUploadError(re, err)
				}
				if !exists {
					return re.JSON(http.StatusNotFound, map[string]string{"detail": "source PDF missing; upload it first"})
				}
				src, err = catalog.RegisterFrozenPaperSource(ctx, store, paperID, origin, key)
			}
			if err == nil {
				src, err = catalog.BindPaperSourceImport(ctx, store, paperID, src.SourceID, alias)
			}
		}
	}
	if err != nil {
		return frozenUploadError(re, err)
	}
	if claimedPDFSha != "" && claimedPDFSha != src.Sha256 {
		return re.JSON(http.StatusBadRequest, map[string]any{"detail": "pdf_sha256 mismatch for the exact frozen source", "claimed_pdf_sha256": claimedPDFSha, "catalog_pdf_sha256": src.Sha256})
	}
	tier := tierParam
	if raw, ok := result.Members[path.Join(path.Dir(result.MiddlePath), "metadata.json")]; ok {
		var metadata struct {
			Tier string `json:"tier"`
		}
		if json.Unmarshal(raw, &metadata) == nil && metadata.Tier != "" {
			tier = metadata.Tier
		}
	}
	tier = registry.NormalizeParseTier(tier)
	revisionID := registry.NewParseRevisionID()
	manifest, err := paperbundle.New(store).WriteBundle(ctx, paperbundle.Input{PaperID: paperID, SourceID: src.SourceID, RevisionID: revisionID, SourcePDFSHA256: src.Sha256, Files: result.Members, MiddlePath: result.MiddlePath, MarkdownPath: result.MarkdownPath})
	if err != nil {
		return frozenUploadError(re, err)
	}
	bundle, err := catalog.PublishBundle(ctx, store, registry.ParseRevision{RevisionID: revisionID, PaperID: paperID, SourceID: src.SourceID, Schema: doc.Schema, SchemaVersion: doc.SchemaVersion, Tier: tier}, true)
	if err != nil {
		return frozenUploadError(re, err)
	}
	setContentSourceHeaders(re, paperResolution(canonical, paperID), src)
	return re.JSON(http.StatusCreated, map[string]any{"paper_id": paperID, "source_id": src.SourceID, "source_sha256": src.Sha256, "revision_id": revisionID, "revision": revisionID, "is_current": true, "tier": tier, "schema": doc.Schema, "schema_version": doc.SchemaVersion, "artifact_sha256": bundle.ArtifactSha256, "manifest_sha256": bundle.ManifestSHA256, "manifest": manifest, "uploaded_by": requester, "source": source, "blocks_endpoint": "/api/papers/" + paperID + "/parses/" + revisionID + "/blocks", "read_endpoint": "/api/papers/" + paperID + "/read?source_id=" + src.SourceID + "&revision=" + revisionID})
}

func normaliseTierParam(v string) string { return strings.ToLower(strings.TrimSpace(v)) }
