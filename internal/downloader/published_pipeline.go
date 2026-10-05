package downloader

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"strings"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/openalex"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/registry"
)

// Title metadata is resolved independently of candidate content. A bibliography
// containing the requested DOI cannot provide expected identity evidence.
func (d *Downloader) verifyPublishedCandidate(ctx context.Context, res *FetchResult, out *FetchOutcome) error {
	if out == nil {
		return ErrPDFIdentityUnproven
	}
	if !out.titleResolved {
		out.titleResolved = true
		if strings.TrimSpace(out.PublishedTitle) == "" {
			if catalog, ok := d.reg.(interface {
				GetPaperIDByIdentity(context.Context, string, string) (string, bool, error)
				GetWithAssets(context.Context, string) (*registry.PaperDetail, bool, error)
			}); ok {
				id, found, err := catalog.GetPaperIDByIdentity(ctx, "doi", out.DOI)
				seen := map[string]bool{}
				for hop := 0; err == nil && found && hop < 8 && !seen[id]; hop++ {
					seen[id] = true
					detail, exists, readErr := catalog.GetWithAssets(ctx, id)
					if readErr != nil || !exists || detail == nil || detail.Paper == nil {
						break
					}
					if next, merged := strings.CutPrefix(detail.Paper.Status, "merged_into:"); merged {
						if !strings.HasPrefix(next, "qa_") {
							break
						}
						id = next
						continue
					}
					out.PublishedTitle = strings.TrimSpace(detail.Paper.Title)
					break
				}
			}
		}
		if strings.TrimSpace(out.PublishedTitle) == "" {
			if resolver, ok := d.oa.(interface {
				LookupMetadata(context.Context, string) (openalex.Metadata, error)
			}); ok {
				if metadata, err := resolver.LookupMetadata(ctx, out.DOI); err == nil && metadata.DOI == out.DOI {
					out.PublishedTitle = strings.TrimSpace(metadata.Title)
				}
			}
		}
	}
	if err := VerifyPublishedPDF(ctx, res, PublishedIdentity{DOI: out.DOI, Title: out.PublishedTitle}, d.cfg.Provenance); err != nil {
		return err
	}
	// The verifier returns a fresh rewindable byte reader. Register actual
	// bytes/hash/size, never an older worker's claimed candidate metadata.
	reader, ok := res.Body.(io.ReadSeeker)
	if !ok {
		res.Body = nil
		return fmt.Errorf("%w: verified PDF is not rewindable", ErrPDFIdentityUnproven)
	}
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(reader, DefaultMaxPDFBytes+1))
	if err != nil || n > DefaultMaxPDFBytes {
		res.Body = nil
		return fmt.Errorf("%w: verified PDF hash/size failure", ErrPDFIdentityUnproven)
	}
	if _, err := reader.Seek(0, io.SeekStart); err != nil {
		res.Body = nil
		return fmt.Errorf("%w: verified PDF rewind failure", ErrPDFIdentityUnproven)
	}
	res.Size, res.Sha256 = n, hex.EncodeToString(h.Sum(nil))
	return nil
}
