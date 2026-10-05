package main

import (
	"bytes"
	"context"
	"errors"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/downloader"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/mineru"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/openalex"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/paperbundle"
)

func knownPublishedPDFURL(resolver *openalex.Resolver) func(context.Context, string) (string, error) {
	return func(ctx context.Context, doi string) (string, error) {
		if resolver == nil || !resolver.Enabled() {
			return "", mineru.ErrNoDOISource
		}
		result, err := resolver.ResolveDOI(ctx, doi)
		if errors.Is(err, openalex.ErrDOINotFound) {
			return "", mineru.ErrNoDOISource
		}
		if err != nil {
			return "", err
		}
		if result.OAPdfURL == "" {
			return "", mineru.ErrNoDOISource
		}
		return result.OAPdfURL, nil
	}
}

func verifyPublishedSource(resolver *openalex.Resolver) func(context.Context, string, []byte) error {
	return func(ctx context.Context, doi string, pdf []byte) error {
		if resolver == nil || !resolver.Enabled() {
			return downloader.ErrPDFIdentityUnproven
		}
		result, err := resolver.ResolveDOI(ctx, doi)
		if err != nil {
			return err
		}
		metadata, err := resolver.LookupMetadata(ctx, doi)
		if err != nil {
			return err
		}
		return downloader.VerifyPublishedPDF(ctx, &downloader.FetchResult{Body: bytes.NewReader(pdf), Size: int64(len(pdf)), Sha256: paperbundle.SHA256(pdf), URL: result.OAPdfURL}, downloader.PublishedIdentity{DOI: doi, Title: metadata.Title}, downloader.PDFProvenanceConfig{})
	}
}
