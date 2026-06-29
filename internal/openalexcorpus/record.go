package openalexcorpus

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strings"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/objstore"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/openalex"
)

// RawWork pairs a verbatim OpenAlex record (the bytes we persist as jsonb,
// only filtered never modified) with the small decoded projection the
// ingester needs for the indexed columns + child tables. Keeping the raw
// bytes avoids a re-marshal (which would not be byte-faithful).
type RawWork struct {
	// Record is the verbatim JSON object for one work, as it appeared in
	// the snapshot line. Stored as openalex_works.record (jsonb).
	Record json.RawMessage
	// Meta is the decoded subset (id, doi, locations, referenced_works…)
	// used to derive openalex_id / arxiv_id / citation edges.
	Meta openalex.Work
}

// OpenAlexID returns the bare "W…" id (URL prefix stripped) used as the
// primary key, or "" when the record has no id.
func (rw RawWork) OpenAlexID() string {
	return shortID(rw.Meta.ID)
}

// ArxivID returns the canonical arxiv id mined from the work's locations,
// or "" when the work has no arxiv presence. Reuses the tested extraction
// in internal/openalex.
func (rw RawWork) ArxivID() string {
	return openalex.ExtractArxivID(rw.Meta)
}

// ReferencedIDs returns the bare "W…" ids this work cites (URL prefixes
// stripped), for the work_referenced child table.
func (rw RawWork) ReferencedIDs() []string {
	if len(rw.Meta.ReferencedWorks) == 0 {
		return nil
	}
	out := make([]string, 0, len(rw.Meta.ReferencedWorks))
	for _, r := range rw.Meta.ReferencedWorks {
		if id := shortID(r); id != "" {
			out = append(out, id)
		}
	}
	return out
}

// StreamRawWorks reads a gzipped JSONL OpenAlex works object from store
// and invokes fn for each record, exposing BOTH the verbatim bytes and the
// decoded projection. It streams (constant memory) so a multi-hundred-MB
// part never lands fully in RAM. A decode error on one line is returned
// immediately (fail-loud — the snapshot is supposed to be byte-faithful).
//
// This mirrors openalex.StreamWorks but additionally captures the raw
// record bytes, which the PG corpus persists verbatim as jsonb.
func StreamRawWorks(ctx context.Context, store objstore.Store, key string, fn func(RawWork) error) error {
	rc, _, err := store.Get(ctx, key)
	if err != nil {
		return fmt.Errorf("openalexcorpus: get %s: %w", key, err)
	}
	defer rc.Close()

	gz, err := gzip.NewReader(rc)
	if err != nil {
		return fmt.Errorf("openalexcorpus: gunzip %s: %w", key, err)
	}
	defer gz.Close()

	dec := json.NewDecoder(gz)
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			if err == io.EOF {
				return nil
			}
			return fmt.Errorf("openalexcorpus: decode %s: %w", key, err)
		}
		var meta openalex.Work
		if err := json.Unmarshal(raw, &meta); err != nil {
			return fmt.Errorf("openalexcorpus: project %s: %w", key, err)
		}
		if err := fn(RawWork{Record: raw, Meta: meta}); err != nil {
			return err
		}
	}
}

// shortID strips the OpenAlex URL prefix from a W/A/S/T id, leaving the
// bare "W2741809807" form used as the corpus primary key. Mirrors the
// unexported helper in internal/openalex (duplicated to keep the packages
// decoupled).
func shortID(openalexURL string) string {
	u := strings.TrimSpace(openalexURL)
	if i := strings.LastIndexByte(u, '/'); i >= 0 {
		return u[i+1:]
	}
	return u
}

// partitionDateRE captures the YYYY-MM-DD from an OpenAlex snapshot part
// key, whose layout mirrors upstream:
//
//	works/updated_date=YYYY-MM-DD/part_NNNN.gz
var partitionDateRE = regexp.MustCompile(`updated_date=(\d{4}-\d{2}-\d{2})`)

// PartitionDate extracts the updated_date partition (YYYY-MM-DD) from a
// part key, or "" when the key doesn't carry one. This is the authoritative
// updated_date for every row in the part (the per-record updated_date field
// is a full timestamp; the partition is the snapshot's own grouping and is
// what incremental refresh keys on).
func PartitionDate(key string) string {
	m := partitionDateRE.FindStringSubmatch(key)
	if m == nil {
		return ""
	}
	return m[1]
}
