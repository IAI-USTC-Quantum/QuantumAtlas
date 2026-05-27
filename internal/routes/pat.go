package routes

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/pat"

	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/types"
)

// RegisterPAT wires the /api/pat surface for Personal Access Tokens.
//
// Endpoints (all behind authGuard so only the signed-in user — never
// anonymous, never another user — can mint or revoke their own PATs):
//
//	POST   /api/pat       — create new PAT, returns plaintext ONCE
//	GET    /api/pat       — list current user's PATs (no plaintext, no hash)
//	DELETE /api/pat/{id}  — revoke (hard-delete) one of current user's PATs
//
// Why not let the SPA hit PocketBase's auto-generated collection API
// directly? Two reasons:
//
//  1. The plaintext is generated server-side and exists only in the
//     POST response body. Going through the generic collection API
//     would require the client to invent the plaintext (insecure: the
//     browser sees fewer crypto-quality random bits) or skip it and
//     the create rule would have to whitelist hash injection (bad).
//
//  2. We want a predictable shape — `{id, name, prefix, plaintext,
//     expires_at, created}` — independent of the collection schema, so
//     future migrations don't break CLI tooling that uses this surface.
func RegisterPAT(se *core.ServeEvent, app core.App) {
	se.Router.POST("/api/pat", authGuard(patCreateHandler(app)))
	se.Router.GET("/api/pat", authGuard(patListHandler(app)))
	se.Router.DELETE("/api/pat/{id}", authGuard(patDeleteHandler(app)))
}

// patCreateRequest is the JSON body shape accepted by POST /api/pat.
type patCreateRequest struct {
	Name          string `json:"name"`
	Description   string `json:"description,omitempty"`
	ExpiresInDays int    `json:"expires_in_days,omitempty"` // 0 = never expires
}

// patCreateResponse is what the SPA / CLI sees back. Plaintext is
// included exactly once here; subsequent GETs only return prefix.
type patCreateResponse struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Prefix      string `json:"prefix"`
	Plaintext   string `json:"plaintext"`
	Description string `json:"description,omitempty"`
	ExpiresAt   string `json:"expires_at,omitempty"`
	Created     string `json:"created"`
}

// patSummary is the listed shape — explicitly no plaintext, no hash.
type patSummary struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Prefix      string `json:"prefix"`
	Description string `json:"description,omitempty"`
	ExpiresAt   string `json:"expires_at,omitempty"`
	LastUsedAt  string `json:"last_used_at,omitempty"`
	Created     string `json:"created"`
}

func patCreateHandler(app core.App) func(re *core.RequestEvent) error {
	return func(re *core.RequestEvent) error {
		user := re.Auth // authGuard guarantees non-nil
		if user == nil {
			return re.JSON(http.StatusUnauthorized, map[string]string{"detail": "missing auth"})
		}

		raw, err := io.ReadAll(re.Request.Body)
		if err != nil {
			return re.JSON(http.StatusBadRequest, map[string]string{"detail": "read body: " + err.Error()})
		}
		var body patCreateRequest
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &body); err != nil {
				return re.JSON(http.StatusBadRequest, map[string]string{"detail": "parse body: " + err.Error()})
			}
		}
		body.Name = strings.TrimSpace(body.Name)
		if body.Name == "" {
			return re.JSON(http.StatusBadRequest, map[string]string{"detail": "name required"})
		}
		if len(body.Name) > 80 {
			return re.JSON(http.StatusBadRequest, map[string]string{"detail": "name max length is 80"})
		}
		if len(body.Description) > 200 {
			return re.JSON(http.StatusBadRequest, map[string]string{"detail": "description max length is 200"})
		}

		plaintext, prefix, hash, err := pat.Generate()
		if err != nil {
			return re.JSON(http.StatusInternalServerError, map[string]string{"detail": "generate token: " + err.Error()})
		}

		collection, err := app.FindCollectionByNameOrId(pat.CollectionName)
		if err != nil {
			return re.JSON(http.StatusInternalServerError, map[string]string{"detail": "collection not found: " + err.Error()})
		}
		rec := core.NewRecord(collection)
		rec.Set("user", user.Id)
		rec.Set("name", body.Name)
		rec.Set("prefix", prefix)
		rec.Set("token_hash", hash)
		if body.Description != "" {
			rec.Set("description", body.Description)
		}
		var expiresStr string
		if body.ExpiresInDays > 0 {
			expires := types.NowDateTime().AddDate(0, 0, body.ExpiresInDays)
			rec.Set("expires_at", expires)
			expiresStr = expires.String()
		}
		if err := app.Save(rec); err != nil {
			return re.JSON(http.StatusInternalServerError, map[string]string{"detail": "save token: " + err.Error()})
		}

		return re.JSON(http.StatusOK, patCreateResponse{
			ID:          rec.Id,
			Name:        body.Name,
			Prefix:      prefix,
			Plaintext:   plaintext,
			Description: body.Description,
			ExpiresAt:   expiresStr,
			Created:     rec.GetDateTime("created").String(),
		})
	}
}

func patListHandler(app core.App) func(re *core.RequestEvent) error {
	return func(re *core.RequestEvent) error {
		user := re.Auth
		if user == nil {
			return re.JSON(http.StatusUnauthorized, map[string]string{"detail": "missing auth"})
		}
		records, err := app.FindRecordsByFilter(
			pat.CollectionName,
			"user = {:user}",
			"-created",
			0, // no limit — heavy users have <100 PATs in practice
			0,
			map[string]any{"user": user.Id},
		)
		if err != nil {
			return re.JSON(http.StatusInternalServerError, map[string]string{"detail": "list tokens: " + err.Error()})
		}

		summaries := make([]patSummary, 0, len(records))
		for _, rec := range records {
			summaries = append(summaries, patSummary{
				ID:          rec.Id,
				Name:        rec.GetString("name"),
				Prefix:      rec.GetString("prefix"),
				Description: rec.GetString("description"),
				ExpiresAt:   nonZeroDate(rec.GetDateTime("expires_at")),
				LastUsedAt:  nonZeroDate(rec.GetDateTime("last_used_at")),
				Created:     rec.GetDateTime("created").String(),
			})
		}
		return re.JSON(http.StatusOK, map[string]any{"tokens": summaries})
	}
}

func patDeleteHandler(app core.App) func(re *core.RequestEvent) error {
	return func(re *core.RequestEvent) error {
		user := re.Auth
		if user == nil {
			return re.JSON(http.StatusUnauthorized, map[string]string{"detail": "missing auth"})
		}
		id := strings.TrimSpace(re.Request.PathValue("id"))
		if id == "" {
			return re.JSON(http.StatusBadRequest, map[string]string{"detail": "missing id"})
		}
		rec, err := app.FindRecordById(pat.CollectionName, id)
		if err != nil {
			// Don't distinguish "not found" vs "other user's PAT" —
			// both leak existence. authentik does the same.
			return re.JSON(http.StatusNotFound, map[string]string{"detail": "token not found"})
		}
		if rec.GetString("user") != user.Id {
			// Same opaque response as not-found to avoid enumeration.
			return re.JSON(http.StatusNotFound, map[string]string{"detail": "token not found"})
		}
		if err := app.Delete(rec); err != nil {
			return re.JSON(http.StatusInternalServerError, map[string]string{"detail": "delete: " + err.Error()})
		}
		return re.JSON(http.StatusOK, map[string]bool{"ok": true})
	}
}

// nonZeroDate returns "" for a zero DateTime, otherwise its string
// rendering. PocketBase's DateField default is the zero value, which
// would otherwise serialize as "0001-01-01 00:00:00.000Z" — surprising
// for the SPA and harder to render conditionally.
func nonZeroDate(dt types.DateTime) string {
	if dt.Time().IsZero() {
		return ""
	}
	return dt.String()
}

// markPATUsed bumps last_used_at on the record asynchronously. It is
// called by authGuard after accepting a PAT-authenticated request;
// failures are logged but never block the response. Kept here (next to
// the handlers it complements) so the auth.go file doesn't grow a
// pat-package import just for one line of bookkeeping.
func markPATUsed(app core.App, rec *core.Record) {
	go func() {
		if err := pat.MarkUsed(app, rec); err != nil {
			slog.Warn("pat: failed to mark token used", "id", rec.Id, "error", err)
		}
	}()
}
