package mineru

import (
	"context"
	"fmt"
	"time"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/config"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/objstore"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/paperassets"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/shares"
)

// BuildPDFURL returns the URL MinerU should fetch the paper's PDF from.
// Always points at THIS edge's RustFS public endpoint (presigned, S3
// backend) so MinerU sees the exact bytes the catalog has — never
// arxiv.org's live (possibly newer / retracted) PDF.
//
// Used by both:
//
//   - internal/mineru/converter.go (server-side silent conversion path):
//     server uses its own MinerU token, downloads result, writes md +
//     images to object storage.
//   - internal/routes/papers.go::mineruClaimHandler (contributor flow):
//     external contributor with `papers:write` PAT receives a claim
//     plus this URL, runs MinerU client-side with their own token, and
//     posts the result zip back via /upload-mineru.
//
// Both paths must produce byte-identical conversions, so they need the
// same PDF URL semantics — that's why this lives in a shared helper
// rather than being a Converter method.
//
// Preference order:
//
//  1. Presigned RustFS direct link — real private bytes, served via
//     QATLAS_S3_PUBLIC_ENDPOINT (RackNerd: https://raw.<domain>;
//     Alibaba: http://<ip>:9000 plain HTTP because its self-signed TLS
//     is not MinerU-trusted). Each edge is self-contained — no
//     cross-edge dependency.
//  2. Share-token URL — fallback only when the backend can't presign
//     (local dev LocalStore). Preserves the dev workflow.
//
// pdfKey must be the canonical PDF object key (typically
// `paperassets.AssetKey("pdf", canonical)`). ttl is how long MinerU
// is given to fetch — caller decides (silent conversion uses
// MinerUTimeout + 10min margin; claim flow uses claim TTL + 10min
// margin so a slow MinerU pull doesn't expire the URL mid-fetch).
func BuildPDFURL(
	ctx context.Context,
	cfg *config.Config,
	store objstore.Store,
	shareStore *shares.Store,
	canonical, pdfKey string,
	ttl time.Duration,
) (string, error) {
	url, ok, err := store.PresignGet(ctx, pdfKey, ttl)
	if err != nil {
		return "", fmt.Errorf("presign pdf: %w", err)
	}
	if ok && url != "" {
		return url, nil
	}
	// Backend can't presign (LocalStore in dev). Fall back to a
	// share-token URL — same effective semantics (MinerU follows the
	// HTTP redirect chain from share -> stored bytes).
	return buildShareURLForPDF(cfg, shareStore, store, canonical, pdfKey)
}

// buildShareURLForPDF is the LocalStore fallback for BuildPDFURL. It
// prefers a static QATLAS_SHARE_ACCESS_TOKEN (no DB write) and only
// creates a fresh share record when the static token isn't configured.
func buildShareURLForPDF(
	cfg *config.Config,
	shareStore *shares.Store,
	store objstore.Store,
	canonical, pdfKey string,
) (string, error) {
	relSharePath := paperassets.ShareRelPathForKey(pdfKey)
	shareToken := cfg.ShareAccessToken
	shareBaseURL := ""
	if shareToken != "" {
		shareBaseURL = cfg.PublicBaseURL
	} else {
		rec, err := shares.CreateRecord(shareStore, cfg, shares.CreateOptions{
			Paths: []string{relSharePath},
			Label: "mineru pdf: " + canonical,
		}, store)
		if err != nil {
			return "", fmt.Errorf("build share URL: %w", err)
		}
		shareToken = rec.Token
	}
	return shares.BuildURL(shareToken, relSharePath, shareBaseURL), nil
}
