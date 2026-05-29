package routes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/config"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/objstore"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/paperindex"

	"github.com/pocketbase/pocketbase/core"
)

// RegisterRustFSEvent wires POST /api/_rustfs/event — the webhook
// endpoint RustFS calls (via its notify_webhook target) every time an
// object is PUT or DELETEd in the qatlas-raw bucket.
//
// We mount this only when the deployment has both:
//   - cfg.RustFSEventToken set (the shared secret RustFS uses in its
//     Authorization header), AND
//   - a non-nil paperindex.Store (the catalog this endpoint mutates).
//
// Either missing → handler is omitted entirely. This is fail-closed:
// without the token, anyone could POST forged events and corrupt the
// catalog; without the Store, there's nothing to apply events to.
//
// See docs/architecture.md → "论文元数据索引" section for the broader
// design (why webhook instead of in-handler upsert; why no nightly
// reconciler is needed when RustFS owns the event source).
//
// Wire protocol: RustFS sends each notification as a JSON POST whose
// body shape is documented in crates/notify/src/event.rs of the
// RustFS source. We accept two shapes defensively (a single Event
// object or the `{Records: [...]}` envelope MinIO uses) because the
// webhook target's exact serialisation can vary across RustFS minor
// versions and we'd rather log a warning than 500 on shape drift.
func RegisterRustFSEvent(
	se *core.ServeEvent,
	cfg *config.Config,
	rawStore objstore.Store,
	paperIndex *paperindex.Store,
) {
	if cfg.RustFSEventToken == "" {
		slog.Warn("RegisterRustFSEvent: skipped (QATLAS_RUSTFS_EVENT_TOKEN not set)")
		return
	}
	if paperIndex == nil {
		slog.Warn("RegisterRustFSEvent: skipped (paperindex.Store is nil)")
		return
	}
	expectedHeader := "Bearer " + cfg.RustFSEventToken
	se.Router.POST("/api/_rustfs/event", func(re *core.RequestEvent) error {
		// 1. Authenticate.
		if re.Request.Header.Get("Authorization") != expectedHeader {
			return re.JSON(http.StatusUnauthorized, map[string]string{
				"error": "invalid or missing Authorization bearer",
			})
		}
		// 2. Read and parse body (cap at 4 MiB — RustFS events are
		//    typically <2 KiB each; a batch >1000 entries is anomalous).
		body, err := io.ReadAll(io.LimitReader(re.Request.Body, 4<<20))
		if err != nil {
			return re.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
		}
		events, parseErr := parseRustFSEvents(body)
		if parseErr != nil {
			// Log the body for diagnosis but return 200 anyway so RustFS
			// doesn't infinite-retry on a malformed event we can't parse.
			slog.Warn("rustfs-event: parse failed; ack-and-drop",
				"error", parseErr, "body_prefix", trimForLog(body, 512))
			return re.JSON(http.StatusOK, map[string]string{"warning": "ack-and-drop unparseable event"})
		}

		// 3. Apply each event to paperindex.
		processed := 0
		errored := 0
		ctx := re.Request.Context()
		for _, ev := range events {
			if err := applyRustFSEvent(ctx, ev, cfg, rawStore, paperIndex); err != nil {
				errored++
				slog.Warn("rustfs-event: apply failed",
					"event", ev.EventName, "key", ev.Key, "error", err)
				continue
			}
			processed++
		}
		// Always 200: RustFS treats non-2xx as "retry later" and
		// will hammer us forever on a permanent application error.
		// Per-event apply errors are logged above — they don't justify
		// pushing the whole batch back into RustFS's retry queue.
		return re.JSON(http.StatusOK, map[string]any{
			"processed": processed,
			"errored":   errored,
		})
	})
	slog.Info("rustfs-event: webhook enabled", "endpoint", "/api/_rustfs/event")
}

// rustFSEvent is the slimmed-down view of a single event we actually
// need. We tolerate the full RustFS payload shape by ignoring extra
// fields (json.Decode does so by default).
type rustFSEvent struct {
	EventName string
	Bucket    string
	Key       string
	Size      int64
	ETag      string
	EventTime time.Time
}

// parseRustFSEvents accepts either of two shapes:
//
//  1. The MinIO-compatible envelope:
//     `{"EventName": "...", "Key": "...", "Records": [{ ... }, ...]}`
//     where `Records` is the list of actual S3 events and the
//     top-level `EventName/Key` are convenience copies of `Records[0]`.
//
//  2. A single Event object at the top level (matches the Rust struct
//     in crates/notify/src/event.rs::Event):
//     `{"eventName": "...", "s3": {"bucket": {...}, "object": {...}}}`
//
// Both shapes are documented in RustFS source / commits at various
// points; rather than pinning one and breaking on minor-version
// drift, this parser sniffs both.
func parseRustFSEvents(body []byte) ([]rustFSEvent, error) {
	// First try as an envelope.
	var env struct {
		Records []recordJSON `json:"Records"`
	}
	if err := json.Unmarshal(body, &env); err == nil && len(env.Records) > 0 {
		out := make([]rustFSEvent, 0, len(env.Records))
		for _, r := range env.Records {
			ev, ok := r.toEvent()
			if ok {
				out = append(out, ev)
			}
		}
		if len(out) > 0 {
			return out, nil
		}
	}

	// Fallback: single Event at top level. RustFS Event has fields
	// at top level matching recordJSON (event_name, s3, etc.).
	var single recordJSON
	if err := json.Unmarshal(body, &single); err != nil {
		return nil, fmt.Errorf("body is neither envelope nor single Event: %w", err)
	}
	if ev, ok := single.toEvent(); ok {
		return []rustFSEvent{ev}, nil
	}
	return nil, errors.New("body parsed but contained no usable event records")
}

// recordJSON mirrors the camelCase field tags from RustFS's
// crates/notify/src/event.rs::Event. We only declare fields we read;
// json.Decode silently ignores the rest.
type recordJSON struct {
	EventName string `json:"eventName"`
	EventTime string `json:"eventTime"`
	S3        struct {
		Bucket struct {
			Name string `json:"name"`
		} `json:"bucket"`
		Object struct {
			Key  string `json:"key"`
			Size *int64 `json:"size"`
			ETag string `json:"eTag"`
		} `json:"object"`
	} `json:"s3"`
}

func (r recordJSON) toEvent() (rustFSEvent, bool) {
	if r.EventName == "" || r.S3.Bucket.Name == "" || r.S3.Object.Key == "" {
		return rustFSEvent{}, false
	}
	// RustFS URL-encodes object keys per S3 spec. Decode so downstream
	// path parsing sees plain "pdf/2401/2401.0001v1.pdf" and not
	// "pdf%2F2401%2F...".
	decodedKey, err := url.QueryUnescape(r.S3.Object.Key)
	if err != nil {
		decodedKey = r.S3.Object.Key // best-effort fallback
	}
	ev := rustFSEvent{
		EventName: r.EventName,
		Bucket:    r.S3.Bucket.Name,
		Key:       decodedKey,
		ETag:      strings.Trim(r.S3.Object.ETag, `"`),
	}
	if r.S3.Object.Size != nil {
		ev.Size = *r.S3.Object.Size
	}
	if r.EventTime != "" {
		if t, err := time.Parse(time.RFC3339Nano, r.EventTime); err == nil {
			ev.EventTime = t
		}
	}
	if ev.EventTime.IsZero() {
		ev.EventTime = time.Now().UTC()
	}
	return ev, true
}

// applyRustFSEvent routes one event to the right paperindex.Store method.
// Returns nil for events that are intentionally ignored (e.g. writes
// to the index/papers.parquet object itself — which we triggered).
func applyRustFSEvent(
	ctx context.Context,
	ev rustFSEvent,
	cfg *config.Config,
	rawStore objstore.Store,
	paperIndex *paperindex.Store,
) error {
	// Wrong bucket → defensive ignore (shouldn't happen if mc event
	// add was scoped correctly, but defensive guard avoids polluting
	// the index if the operator binds the webhook to a wider scope).
	if ev.Bucket != cfg.S3Bucket {
		return nil
	}
	// Skip events about the catalog object itself: we generated them
	// via flush(), and applying them back would just be churn.
	if ev.Key == paperindex.DefaultParquetKey {
		return nil
	}

	kind, arxivID, yymm, ok := parseAssetKey(ev.Key)
	if !ok {
		// Unknown key shape (e.g. user uploaded something to a
		// non-standard prefix). Log once at debug level via the
		// caller's log; don't error.
		return nil
	}

	created := strings.HasPrefix(ev.EventName, "s3:ObjectCreated:")
	removed := strings.HasPrefix(ev.EventName, "s3:ObjectRemoved:")
	if !created && !removed {
		// Tagging / lifecycle / replication events — out of scope.
		return nil
	}

	switch kind {
	case "pdf":
		if removed {
			return paperIndex.RemoveAsset(ctx, arxivID, "pdf")
		}
		return paperIndex.UpsertPDFAsset(ctx, arxivID, yymm, ev.Size, ev.EventTime, ev.ETag)
	case "markdown":
		if removed {
			return paperIndex.RemoveAsset(ctx, arxivID, "md")
		}
		return paperIndex.UpsertMDAsset(ctx, arxivID, yymm, ev.Size, ev.EventTime, ev.ETag)
	case "json":
		if removed {
			return paperIndex.RemoveAsset(ctx, arxivID, "json")
		}
		// Best-effort metadata enrichment: pull the just-uploaded
		// json from the bucket and parse its title/abstract/etc.
		// On fetch failure (transient S3 hiccup), fall back to the
		// no-metadata upsert so at least has_json + sizes get set.
		md, mdOK := fetchArxivMetadata(ctx, rawStore, ev.Key)
		if mdOK {
			return paperIndex.UpsertJSONMetadata(ctx, arxivID, yymm,
				ev.Size, ev.EventTime, ev.ETag, md)
		}
		return paperIndex.UpsertJSONAsset(ctx, arxivID, yymm, ev.Size, ev.EventTime, ev.ETag)
	case "images":
		delta := 1
		if removed {
			delta = -1
		}
		return paperIndex.AdjustImageCount(ctx, arxivID, yymm, delta)
	}
	return nil
}

// parseAssetKey decodes a bucket object key into the (kind, arxivID,
// yymm) triple expected by paperindex Upsert methods. Returns ok=false
// for any key shape we don't recognise (the catch-all so noise like
// index/* or operator-uploaded files at the bucket root doesn't enter
// the catalog).
//
// Recognised shapes (matching internal/paperassets.AssetKey):
//
//	pdf/<yymm>/<id>.pdf                         → kind=pdf
//	markdown/<yymm>/<id>.md                     → kind=markdown
//	json/<yymm>/<id>.json                       → kind=json
//	images/<yymm>/<id>/<anything>               → kind=images
func parseAssetKey(key string) (kind, arxivID, yymm string, ok bool) {
	parts := strings.Split(key, "/")
	if len(parts) < 3 {
		return "", "", "", false
	}
	switch parts[0] {
	case "pdf", "markdown", "json":
		if len(parts) != 3 {
			return "", "", "", false
		}
		yymm = parts[1]
		base := parts[2]
		stem := strings.TrimSuffix(base, path.Ext(base))
		if stem == "" {
			return "", "", "", false
		}
		return parts[0], stem, yymm, true
	case "images":
		// images/<yymm>/<arxiv_id>/<file>
		if len(parts) < 4 {
			return "", "", "", false
		}
		return "images", parts[2], parts[1], true
	}
	return "", "", "", false
}

// fetchArxivMetadata downloads json/<…>.json from the bucket and
// extracts title/abstract/etc. Returns ok=false on any failure — the
// caller falls back to the metadata-less UpsertJSONAsset path.
//
// The schema here mirrors what was observed in the production bucket
// (see PoC peek_json.py): top-level fields title / abstract / authors
// / authors_parsed / categories / submitter / update_date / etc.
// Missing fields produce empty strings / zero-times, which the
// paperindex Upsert path COALESCEs against existing values rather
// than clobbering them.
func fetchArxivMetadata(ctx context.Context, store objstore.Store, key string) (paperindex.JSONMetadata, bool) {
	rc, _, err := store.Get(ctx, key)
	if err != nil {
		return paperindex.JSONMetadata{}, false
	}
	defer rc.Close()
	body, err := io.ReadAll(io.LimitReader(rc, 2<<20)) // arxiv metadata jsons are <50KB; 2 MiB is paranoid
	if err != nil {
		return paperindex.JSONMetadata{}, false
	}
	var parsed struct {
		Title      string `json:"title"`
		Abstract   string `json:"abstract"`
		Authors    string `json:"authors"`
		Categories string `json:"categories"`
		Submitter  string `json:"submitter"`
		UpdateDate string `json:"update_date"`
		// authors_parsed is [[lastName, firstName, suffix], ...]; we
		// don't need it when `authors` (the joined string) is already
		// present in the same record, which the PoC scrape confirmed.
		// json.Unmarshal silently ignores unknown fields by default,
		// so listing this here as a documenting comment is enough.
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return paperindex.JSONMetadata{}, false
	}
	md := paperindex.JSONMetadata{
		Title:      strings.TrimSpace(parsed.Title),
		Abstract:   strings.TrimSpace(parsed.Abstract),
		Authors:    strings.TrimSpace(parsed.Authors),
		Categories: strings.TrimSpace(parsed.Categories),
		Submitter:  strings.TrimSpace(parsed.Submitter),
	}
	if parsed.UpdateDate != "" {
		// arxiv update_date is "YYYY-MM-DD".
		if t, err := time.Parse("2006-01-02", parsed.UpdateDate); err == nil {
			md.UpdateDate = t
		}
	}
	return md, true
}

// trimForLog truncates a byte slice for log inclusion, replacing
// non-printable bytes so a corrupt body doesn't smash terminal output.
func trimForLog(b []byte, max int) string {
	if len(b) > max {
		b = b[:max]
	}
	return strings.ToValidUTF8(string(b), "?")
}
