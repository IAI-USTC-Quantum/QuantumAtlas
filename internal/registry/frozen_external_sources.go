package registry

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/objstore"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/paperbundle"
	"github.com/jackc/pgx/v5"
)

// RegisterFrozenExternalSource preserves the external-URL identity/metadata
// policy while persisting fresh bytes directly in source-pinned content storage.
// The caller must validate public DNS/redirect ownership before calling it.
// No title-only dedup, legacy staging key, or old PDF bucket write is permitted.
func (s *Store) RegisterFrozenExternalSource(ctx context.Context, objects objstore.Store, ref ExternalSourceRef, pdf []byte) (ExternalSourceRegistration, error) {
	if !s.ensure(ctx) {
		return ExternalSourceRegistration{}, ErrCatalogUnavailable
	}
	ref, normalized, externalID, kind, err := validateFrozenExternalSource(ref, pdf)
	if err != nil {
		return ExternalSourceRegistration{}, err
	}
	if objects == nil {
		return ExternalSourceRegistration{}, errors.New("registry: content storage unavailable")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return ExternalSourceRegistration{}, catalogUnavailable("registry: begin frozen external registration", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	out, err := registerFrozenExternalSource(ctx, tx, objects, ref, pdf, normalized, externalID, kind)
	if err != nil {
		return ExternalSourceRegistration{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return ExternalSourceRegistration{}, catalogUnavailable("registry: commit frozen external registration", err)
	}
	return out, nil
}

func validateFrozenExternalSource(ref ExternalSourceRef, pdf []byte) (ExternalSourceRef, string, string, string, error) {
	normalized, externalID, kind, err := ExternalSourceIdentity(ref.SourceURL)
	if err != nil {
		return ref, "", "", "", err
	}
	src := ref.Source
	if strings.TrimSpace(ref.Title) == "" || ref.Year < 1 || ref.Year > 9999 || src.ObjstoreKey != "" || src.SourceID != "" || src.PaperID != "" ||
		src.Sha256 != paperbundle.SHA256(pdf) || src.SizeBytes != int64(len(pdf)) || !bytes.HasPrefix(pdf, []byte("%PDF-")) ||
		(!bytes.Contains(pdf[max(0, len(pdf)-2048):], []byte("%%EOF")) && !bytes.Contains(pdf, []byte("startxref"))) {
		return ref, "", "", "", fmt.Errorf("%w: title/year or exact PDF metadata invalid (no staging key/IDs allowed)", ErrExternalSourceInvalid)
	}
	if src.SourceURL != "" {
		declaredURL, err := NormalizeExternalSourceURL(src.SourceURL)
		if err != nil || declaredURL != normalized {
			return ref, "", "", "", fmt.Errorf("%w: conflicting source URL metadata", ErrExternalSourceInvalid)
		}
	}
	if src.RetrievedURL == "" {
		src.RetrievedURL = normalized
	}
	src.RetrievedURL, err = NormalizeExternalSourceURL(src.RetrievedURL)
	if err != nil {
		return ref, "", "", "", err
	}
	if src.RetrievedAt.IsZero() {
		src.RetrievedAt = time.Now().UTC()
	}
	src.SourceURL = normalized
	ref.SourceURL, ref.Source = normalized, src
	return ref, normalized, externalID, kind, nil
}

func registerFrozenExternalSource(ctx context.Context, tx contentTx, objects objstore.Store, ref ExternalSourceRef, pdf []byte, normalized, externalID, kind string) (ExternalSourceRegistration, error) {
	out := ExternalSourceRegistration{ExternalID: externalID}
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, externalID); err != nil {
		return out, catalogUnavailable("registry: lock external identity", err)
	}
	err := tx.QueryRow(ctx, `SELECT paper_id FROM paper_identities WHERE identity_key=$1`, externalID).Scan(&out.PaperID)
	if errors.Is(err, pgx.ErrNoRows) {
		err = tx.QueryRow(ctx, `SELECT paper_id FROM papers WHERE external_id=$1`, externalID).Scan(&out.PaperID)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		out.PaperID, out.Created = newPaperID(), true
		_, err = tx.Exec(ctx, `INSERT INTO papers (paper_id,external_id,title,authors,publication_date) VALUES ($1,$2,$3,$4,make_date($5,1,1))`, out.PaperID, externalID, strings.TrimSpace(ref.Title), ref.Authors, ref.Year)
	}
	if err != nil {
		return out, catalogUnavailable("registry: resolve external work", err)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO paper_identities (identity_key,paper_id,kind) VALUES ($1,$2,$3) ON CONFLICT (identity_key) DO NOTHING`, externalID, out.PaperID, kind); err != nil {
		return out, catalogUnavailable("registry: insert external identity", err)
	}
	if err := lockContentPaper(ctx, tx, out.PaperID); err != nil {
		return out, err
	}
	src, err := scanPaperSource(tx.QueryRow(ctx, `SELECT `+sourceColumns+` FROM paper_sources WHERE paper_id=$1 AND sha256=$2 ORDER BY created_at,source_id LIMIT 1`, out.PaperID, ref.Source.Sha256))
	if errors.Is(err, pgx.ErrNoRows) {
		src = ref.Source
		src.SourceID, src.PaperID, src.Origin, src.SourceURL = NewSourceID(), out.PaperID, externalID, normalized
		frozen, freezeErr := paperbundle.New(objects).FreezePDF(ctx, src.PaperID, src.SourceID, pdf, src.Sha256)
		if freezeErr != nil {
			return out, freezeErr
		}
		src.ObjstoreKey = frozen.Key
		_, err = tx.Exec(ctx, `INSERT INTO paper_sources (source_id,paper_id,origin,sha256,objstore_key,size_bytes,source_url,retrieved_url,retrieved_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
			src.SourceID, src.PaperID, src.Origin, src.Sha256, src.ObjstoreKey, src.SizeBytes, src.SourceURL, src.RetrievedURL, src.RetrievedAt)
		if err != nil {
			return out, catalogUnavailable("registry: append frozen external source", err)
		}
	} else if err != nil {
		return out, catalogUnavailable("registry: reuse external PDF SHA", err)
	} else {
		// Keep historical source IDs/origin and first-known provenance intact.
		// Legacy location can move only using exact same-SHA supplied bytes;
		// already frozen missing/corrupt bytes must fail, never be recreated.
		src, err = freezeSourceBytes(ctx, tx, objects, src, pdf)
		if err != nil {
			return out, err
		}
		_, err = tx.Exec(ctx, `UPDATE paper_sources SET source_url=coalesce(nullif(source_url,''),$1),retrieved_url=coalesce(nullif(retrieved_url,''),$2),retrieved_at=coalesce(retrieved_at,$3) WHERE paper_id=$4 AND source_id=$5 AND sha256=$6`,
			normalized, ref.Source.RetrievedURL, ref.Source.RetrievedAt, src.PaperID, src.SourceID, src.Sha256)
		if err != nil {
			return out, catalogUnavailable("registry: preserve first external provenance", err)
		}
	}
	out.Source, err = scanPaperSource(tx.QueryRow(ctx, `SELECT `+sourceColumns+` FROM paper_sources WHERE paper_id=$1 AND source_id=$2`, out.PaperID, src.SourceID))
	if err != nil {
		return out, catalogUnavailable("registry: read frozen external source", err)
	}
	return out, nil
}
