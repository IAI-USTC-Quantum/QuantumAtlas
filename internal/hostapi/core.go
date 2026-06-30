package hostapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/events"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/objstore"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/paperassets"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/wiki"
)

// RegisterCoreMethods registers the genuinely host-shared capabilities an
// external (socket/stdio) plugin may call over RPC: the Paper catalog readers,
// the wiki page/search readers, and the events bus. It deliberately carries NO
// plugin-domain methods (ADR 0003) — the theorems/verifications methods that
// earlier commits put here were a domain leak and were removed (ADR 0004).
//
// Note: there are no external plugins in this iteration, so this RPC surface
// has no live consumer. The wiki-domain readers (pages/get, search/query) stay
// here for now; when an external plugin actually consumes the host API, they
// move under the wiki plugin's own RegisterHostAPI hook (ADR 0003) — deferred
// until there is a consumer to justify the churn.
func RegisterCoreMethods(r *Registry, pages *wiki.Cache, rawStore objstore.Store, bus *events.Bus) error {
	for method, handler := range map[string]Handler{
		"pages/get":           pagesGet(pages),
		"papers/getMarkdown":  papersGetMarkdown(rawStore),
		"papers/getMeta":      papersGetMeta(),
		"papers/getCitedRefs": papersGetCitedRefs(),
		"events/publish":      eventsPublish(bus),
		"search/query":        searchQuery(pages),
	} {
		if err := r.Register(method, handler); err != nil {
			return err
		}
	}
	return nil
}

func pagesGet(cache *wiki.Cache) Handler {
	return func(_ context.Context, params any) (any, error) {
		pageID := stringParam(params, "page_id", "id")
		if pageID == "" {
			return nil, fmt.Errorf("page_id is required")
		}
		page := cache.FindPage(pageID)
		if page == nil {
			return nil, fmt.Errorf("page %s not found", pageID)
		}
		return map[string]any{"frontmatter": page.Frontmatter, "body": page.Content}, nil
	}
}

func papersGetMarkdown(store objstore.Store) Handler {
	return func(ctx context.Context, params any) (any, error) {
		id := stringParam(params, "id", "paper_id", "arxiv_id")
		if id == "" {
			return nil, fmt.Errorf("id is required")
		}
		key, _, exists, err := paperassets.LocateAssetByID(ctx, store, "markdown", id)
		if err != nil {
			return nil, err
		}
		if !exists {
			return nil, fmt.Errorf("markdown for %s not found", id)
		}
		rc, _, err := store.Get(ctx, key)
		if err != nil {
			return nil, err
		}
		defer rc.Close()
		data, err := io.ReadAll(rc)
		if err != nil {
			return nil, err
		}
		return map[string]any{"markdown": string(data), "resolved_id": id, "object_key": key}, nil
	}
}

func papersGetMeta() Handler {
	return func(_ context.Context, params any) (any, error) {
		id := stringParam(params, "id", "paper_id", "arxiv_id")
		if id == "" {
			return nil, fmt.Errorf("id is required")
		}
		return map[string]any{"id": id}, nil
	}
}

// papersGetCitedRefs is the paper-centric "what works does paper X cite" query
// (ADR 0007): its result items are themselves resolvable to metadata via
// GET /api/papers/lookup. Currently a stub returning an empty ref list; wire to
// the OpenAlex corpus when the corpus tables land (ADR 0006).
func papersGetCitedRefs() Handler {
	return func(_ context.Context, params any) (any, error) {
		id := stringParam(params, "id", "paper_id", "arxiv_id")
		if id == "" {
			return nil, fmt.Errorf("id is required")
		}
		return map[string]any{"id": id, "refs": []any{}}, nil
	}
}

func eventsPublish(bus *events.Bus) Handler {
	return func(ctx context.Context, params any) (any, error) {
		body := struct {
			Type    string         `json:"type"`
			Actor   string         `json:"actor"`
			Payload map[string]any `json:"payload"`
		}{}
		if err := decodeParams(params, &body); err != nil {
			return nil, err
		}
		if strings.TrimSpace(body.Type) == "" {
			return nil, fmt.Errorf("type is required")
		}
		ev := events.Event{ID: events.NewID(), Type: body.Type, Time: time.Now().UTC(), Actor: body.Actor, Payload: body.Payload}
		errs := bus.Publish(ctx, ev)
		if len(errs) > 0 {
			return nil, errs[0]
		}
		return map[string]any{"event_id": ev.ID}, nil
	}
}

func searchQuery(cache *wiki.Cache) Handler {
	return func(_ context.Context, params any) (any, error) {
		query := stringParam(params, "q", "query")
		if query == "" {
			return nil, fmt.Errorf("query is required")
		}
		return map[string]any{"hits": cache.Search(query, 20)}, nil
	}
}

func stringParam(params any, keys ...string) string {
	m, _ := params.(map[string]any)
	for _, key := range keys {
		if v, ok := m[key].(string); ok {
			if s := strings.TrimSpace(v); s != "" {
				return s
			}
		}
	}
	return ""
}

func decodeParams(params any, out any) error {
	data, err := json.Marshal(params)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, out)
}
