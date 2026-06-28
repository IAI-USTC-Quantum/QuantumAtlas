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
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/theorems"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/verifications"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/wiki"
	"github.com/pocketbase/pocketbase/core"
)

func RegisterCoreMethods(r *Registry, app core.App, pages *wiki.Cache, rawStore objstore.Store, bus *events.Bus) error {
	for method, handler := range map[string]Handler{
		"pages/get":            pagesGet(pages),
		"papers/getMarkdown":   papersGetMarkdown(rawStore),
		"papers/getMeta":       papersGetMeta(),
		"papers/getCitedRefs":  papersGetCitedRefs(),
		"theorems/get":         theoremsGet(app),
		"theorems/create":      theoremsCreate(app),
		"verifications/submit": verificationsSubmit(app),
		"events/publish":       eventsPublish(bus),
		"search/query":         searchQuery(pages),
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

func papersGetCitedRefs() Handler {
	return func(_ context.Context, params any) (any, error) {
		id := stringParam(params, "id", "paper_id", "arxiv_id")
		if id == "" {
			return nil, fmt.Errorf("id is required")
		}
		return map[string]any{"id": id, "refs": []any{}}, nil
	}
}

func theoremsGet(app core.App) Handler {
	return func(_ context.Context, params any) (any, error) {
		id := stringParam(params, "theorem_id", "id")
		if id == "" {
			return nil, fmt.Errorf("theorem_id is required")
		}
		rec, err := app.FindFirstRecordByFilter(theorems.CollectionName, "theorem_id = {:id}", map[string]any{"id": id})
		if err != nil {
			return nil, fmt.Errorf("theorem %s not found", id)
		}
		return theorems.FromRecord(rec), nil
	}
}

func theoremsCreate(app core.App) Handler {
	return func(_ context.Context, params any) (any, error) {
		body := struct {
			ID          string `json:"id"`
			TheoremID   string `json:"theorem_id"`
			PageID      string `json:"page_id"`
			PaperID     string `json:"paper_id"`
			Section     string `json:"section"`
			MDLines     []int  `json:"md_lines"`
			StatementNL string `json:"statement_nl"`
		}{}
		if err := decodeParams(params, &body); err != nil {
			return nil, err
		}
		id := strings.TrimSpace(firstNonEmpty(body.TheoremID, body.ID))
		if id == "" {
			return nil, fmt.Errorf("theorem_id is required")
		}
		collection, err := app.FindCollectionByNameOrId(theorems.CollectionName)
		if err != nil {
			return nil, err
		}
		rec, err := app.FindFirstRecordByFilter(theorems.CollectionName, "theorem_id = {:id}", map[string]any{"id": id})
		if err != nil {
			rec = core.NewRecord(collection)
		}
		rec.Set("theorem_id", id)
		rec.Set("page_id", body.PageID)
		rec.Set("paper_id", body.PaperID)
		rec.Set("section", body.Section)
		rec.Set("md_lines", theorems.EncodeLines(body.MDLines))
		rec.Set("statement_nl", body.StatementNL)
		if err := app.Save(rec); err != nil {
			return nil, err
		}
		return theorems.FromRecord(rec), nil
	}
}

func verificationsSubmit(app core.App) Handler {
	return func(_ context.Context, params any) (any, error) {
		body := struct {
			ID          string         `json:"id"`
			TheoremID   string         `json:"theorem_id"`
			PluginID    string         `json:"plugin_id"`
			Verdict     string         `json:"verdict"`
			EvidenceURL string         `json:"evidence_url"`
			Payload     map[string]any `json:"payload"`
			Evidence    map[string]any `json:"evidence"`
		}{}
		if err := decodeParams(params, &body); err != nil {
			return nil, err
		}
		if body.Payload == nil {
			body.Payload = body.Evidence
		}
		body.ID = strings.TrimSpace(body.ID)
		if body.ID == "" {
			body.ID = fmt.Sprintf("vrf-%d", time.Now().UnixNano())
		}
		body.TheoremID = strings.TrimSpace(body.TheoremID)
		body.PluginID = strings.TrimSpace(body.PluginID)
		body.Verdict = strings.TrimSpace(body.Verdict)
		if body.TheoremID == "" || body.PluginID == "" || body.Verdict == "" {
			return nil, fmt.Errorf("theorem_id, plugin_id and verdict are required")
		}
		collection, err := app.FindCollectionByNameOrId(verifications.CollectionName)
		if err != nil {
			return nil, err
		}
		rec, err := app.FindFirstRecordByFilter(verifications.CollectionName, "verification_id = {:id}", map[string]any{"id": body.ID})
		if err != nil {
			rec = core.NewRecord(collection)
		}
		rec.Set("verification_id", body.ID)
		rec.Set("theorem_id", body.TheoremID)
		rec.Set("plugin_id", body.PluginID)
		rec.Set("verdict", body.Verdict)
		rec.Set("evidence_url", body.EvidenceURL)
		rec.Set("payload", verifications.EncodePayload(body.Payload))
		if err := app.Save(rec); err != nil {
			return nil, err
		}
		return verifications.FromRecord(rec), nil
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

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
