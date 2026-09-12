package routes

// papers_status_batch.go: GET /api/papers/status/batch?ids=a,b,c — the
// batch asset-status probe.
//
// The single-paper .../markdown/status (and .../pdf/status) surfaces
// answer one id per round-trip; clients refreshing a dashboard of papers
// pay one request each. This endpoint folds the same agent-decision
// fields (md_ready / pdf_ready / phase / state / resolved_id, plus the
// default asset's image_count) into one response for up to 200 ids.
//
// The per-id state machine mirrors markdownStatusHandler /
// markdownStatusByDOIHandler exactly (see paperAssetStatus): store probes
// decide md/pdf readiness, an in-flight converter job wins when the
// markdown bytes are absent, and a paper the registry does not know
// reports error:"not found" instead of failing the whole batch.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/mineru"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/objstore"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/paperassets"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/registry"

	"github.com/pocketbase/pocketbase/core"
)

// maxStatusBatchIDs caps the ?ids= list (mirrors the lookup endpoint's
// 200-ref batch cap).
const maxStatusBatchIDs = 200

// paperStatusEntry is one row of the batch response.
type paperStatusEntry struct {
	RequestedID string `json:"requested_id"`
	ResolvedID  string `json:"resolved_id"`
	MdReady     bool   `json:"md_ready"`
	PdfReady    bool   `json:"pdf_ready"`
	ImageCount  int    `json:"image_count"`
	Phase       string `json:"phase"`
	State       string `json:"state"`
	Error       string `json:"error"`
}

// paperStatusBatchHandler answers GET /api/papers/status/batch?ids=...
func paperStatusBatchHandler(re *core.RequestEvent, catalog paperCatalog, store objstore.Store, converter *mineru.Converter) error {
	ids := parseStatusBatchIDs(re.Request.URL.Query().Get("ids"))
	if len(ids) == 0 {
		return re.JSON(http.StatusBadRequest, map[string]string{
			"detail": "missing ?ids=: comma-separated paper ids (qa_ surrogate, arXiv id, or DOI; max " + fmt.Sprint(maxStatusBatchIDs) + ")",
		})
	}
	if len(ids) > maxStatusBatchIDs {
		return re.JSON(http.StatusBadRequest, map[string]any{
			"detail": fmt.Sprintf("too many ids: %d (max %d)", len(ids), maxStatusBatchIDs),
			"max":    maxStatusBatchIDs,
		})
	}
	ctx := re.Request.Context()
	results := make([]paperStatusEntry, 0, len(ids))
	for _, id := range ids {
		results = append(results, paperStatusEntryFor(ctx, catalog, store, converter, id))
	}
	return re.JSON(http.StatusOK, map[string]any{"results": results})
}

// parseStatusBatchIDs splits the comma-separated ?ids= value, trims,
// drops empties and de-dupes (first-seen order).
func parseStatusBatchIDs(raw string) []string {
	seen := map[string]bool{}
	var out []string
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" || seen[part] {
			continue
		}
		seen[part] = true
		out = append(out, part)
	}
	return out
}

// paperStatusEntryFor resolves one requested id onto its serving
// identity and probes its assets. It never fails: lookup / probe errors
// land in the entry's Error field so one bad id cannot blank the batch.
func paperStatusEntryFor(ctx context.Context, catalog paperCatalog, store objstore.Store, converter *mineru.Converter, requestedID string) paperStatusEntry {
	e := paperStatusEntry{RequestedID: requestedID}
	ident := normalizeIDForDispatch(strings.TrimSpace(requestedID))

	var (
		doi     string
		arxivID string
		detail  *registry.PaperDetail
	)
	switch {
	case isQASurrogate(ident):
		target, terr := resolvePaperAssetTarget(ctx, catalog, ident)
		if terr != nil {
			e.Error = statusErrText(terr)
			return e
		}
		if target.NotFound {
			e.Error = "not found"
			return e
		}
		if target.DOI != "" {
			doi = target.DOI
		} else if target.ArxivVersioned != "" {
			arxivID = target.ArxivVersioned
		} else {
			arxivID = target.ArxivBare
		}
		if d, found, derr := catalog.GetWithAssets(ctx, ident); derr == nil && found {
			detail = d
		}
	case isDOICandidate(ident):
		doi = ident
		pid, found, lerr := catalog.GetPaperIDByIdentity(ctx, "doi", doi)
		if lerr != nil {
			e.Error = statusErrText(lerr)
			return e
		}
		if !found {
			e.Error = "not found"
			return e
		}
		if d, found, derr := catalog.GetWithAssets(ctx, pid); derr == nil && found {
			detail = d
		}
	default:
		parsed, perr := paperassets.Parse(ident)
		if perr != nil || !parsed.IsValid() {
			e.Error = "invalid paper id"
			return e
		}
		pid, found, lerr := catalog.GetPaperIDByIdentity(ctx, "arxiv", registry.NormalizeArxivID(ident))
		if lerr != nil {
			e.Error = statusErrText(lerr)
			return e
		}
		if !found {
			e.Error = "not found"
			return e
		}
		if d, found, derr := catalog.GetWithAssets(ctx, pid); derr == nil && found {
			detail = d
		}
		// Version-default rule, mirroring the GET dispatcher's
		// catalog-first inference: a bare id pins to the highest arXiv
		// asset version the registry holds; explicit vN ids stay as
		// typed. (No arxiv.org scrape here — a batch must stay
		// store-bound.)
		arxivID = ident
		if parsed.Version == "" {
			if v, found, verr := catalog.LatestArxivAssetVersion(ctx, paperassets.StripVersion(ident)); verr == nil && found && v > 0 {
				arxivID = fmt.Sprintf("%sv%d", paperassets.StripVersion(ident), v)
			}
		}
	}

	switch {
	case doi != "":
		e.ResolvedID = doi
		pdfReady, mdReady := probeDOIAssetReadiness(ctx, store, doi)
		st := paperAssetStatus{MdReady: mdReady, PdfReady: pdfReady, State: "missing"}
		if mdReady {
			st.State, st.Phase = "cached", string(mineru.PhaseReady)
		} else {
			hasDOIJob(converter, doi, &st)
		}
		e.MdReady, e.PdfReady, e.State, e.Phase = st.MdReady, st.PdfReady, st.State, st.Phase
	default:
		e.ResolvedID = arxivID
		e.MdReady, e.PdfReady, e.State, e.Phase = arxivAssetStatus(ctx, store, converter, arxivID)
	}
	if detail != nil && len(detail.Assets) > 0 {
		e.ImageCount = detail.Assets[0].ImageCount
	}
	return e
}

// statusErrText renders a lookup failure for the per-entry error field.
func statusErrText(err error) string {
	if errors.Is(err, registry.ErrCatalogUnavailable) {
		return "catalog unavailable"
	}
	return err.Error()
}

// paperAssetStatus is the agent-decision core of the .../markdown/status
// surface: the readiness booleans plus the state-machine labels. It is
// extracted here so the batch endpoint reuses the exact decision order
// of the single-paper status handlers (store probe → cached → in-flight
// job → none / missing).
type paperAssetStatus struct {
	MdReady  bool
	PdfReady bool
	State    string
	Phase    string
}

// arxivAssetStatus mirrors markdownStatusHandler's state machine for one
// canonical (versioned when known) arXiv id: cached when markdown bytes
// exist, the converter job's state when one is in flight or recently
// failed, else none / unavailable.
func arxivAssetStatus(ctx context.Context, store objstore.Store, converter *mineru.Converter, canonical string) (mdReady, pdfReady bool, state, phase string) {
	pdfReady, mdReady = probeAssetReadiness(ctx, store, canonical)
	if mdReady {
		return mdReady, pdfReady, "cached", string(mineru.PhaseReady)
	}
	if converter != nil {
		if job, ok := converter.Lookup(canonical); ok {
			state, phase = string(job.State), string(job.Phase)
			if job.State == mineru.JobStateDone {
				// Job finished but the bytes are gone (race with delete)
				// — same cached answer the single-id handler gives.
				state, phase = "cached", string(mineru.PhaseReady)
			}
			return mdReady, pdfReady || job.Phase == mineru.PhaseConvertingMD || job.State == mineru.JobStateDone, state, phase
		}
	}
	if pdfReady && converter != nil && !converter.Enabled() {
		return mdReady, pdfReady, "unavailable", phase
	}
	return mdReady, pdfReady, "none", phase
}

// hasDOIJob folds an in-flight DOI fetch+convert job into st; it reports
// whether a job was found (mirroring markdownStatusByDOIHandler).
func hasDOIJob(converter *mineru.Converter, doi string, st *paperAssetStatus) bool {
	if converter == nil {
		return false
	}
	job, ok := converter.LookupDOI(doi)
	if !ok {
		return false
	}
	st.State, st.Phase = string(job.State), string(job.Phase)
	if job.State == mineru.JobStateDone {
		st.State, st.Phase = "cached", string(mineru.PhaseReady)
		st.MdReady = true
	}
	st.PdfReady = st.PdfReady || job.Phase == mineru.PhaseConvertingMD || job.State == mineru.JobStateDone
	return true
}
