package openalex

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/objstore"
)

// StreamWorks reads a gzipped JSONL OpenAlex works object from store and
// invokes fn for each decoded Work. It streams (constant memory) so a
// multi-GB part file never lands fully in RAM. A decode error on one
// line is returned immediately (fail-loud — the snapshot is supposed to
// be byte-faithful, a malformed line means upstream corruption).
func StreamWorks(ctx context.Context, store objstore.Store, key string, fn func(Work) error) error {
	rc, _, err := store.Get(ctx, key)
	if err != nil {
		return fmt.Errorf("openalex: get %s: %w", key, err)
	}
	defer rc.Close()

	gz, err := gzip.NewReader(rc)
	if err != nil {
		return fmt.Errorf("openalex: gunzip %s: %w", key, err)
	}
	defer gz.Close()

	dec := json.NewDecoder(gz)
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		var w Work
		if err := dec.Decode(&w); err != nil {
			if err == io.EOF {
				return nil
			}
			return fmt.Errorf("openalex: decode %s: %w", key, err)
		}
		if err := fn(w); err != nil {
			return err
		}
	}
}

// ListPartKeys returns the object keys of every works part under the
// OpenAlex snapshot prefix. The OpenAlex layout mirrors upstream:
//
//	works/updated_date=YYYY-MM-DD/part_NNN.jsonl.gz
func ListPartKeys(ctx context.Context, store objstore.Store, prefix string) ([]string, error) {
	objs, err := store.ListPrefix(ctx, prefix, 0)
	if err != nil {
		return nil, fmt.Errorf("openalex: list %s: %w", prefix, err)
	}
	keys := make([]string, 0, len(objs))
	for _, o := range objs {
		keys = append(keys, o.Key)
	}
	return keys, nil
}
