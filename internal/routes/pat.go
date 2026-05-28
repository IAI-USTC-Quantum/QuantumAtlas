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

// MaxPATExpiryDays caps how long a single PAT can live. Set to one
// year, matching GitHub fine-grained PATs. The lower bound is 1 day
// (any non-zero positive integer accepted); shorter than that, just
// use a session token.
const MaxPATExpiryDays = 365

// RegisterPAT wires the /api/pat surface for Personal Access Tokens.
//
// All three endpoints sit behind sessionGuard, NOT authGuard — a
// leaked PAT must not be usable to mint or revoke other PATs. The
// only way to manage PATs is through the interactive browser session
// (sign in with GitHub, get a session token, post to /api/pat). This
// mirrors GitHub's fine-grained PAT design where PAT-management
// requires the web UI.
//
// Endpoints:
//
//	POST   /api/pat       — create new PAT, returns plaintext ONCE
//	GET    /api/pat       — list current user's PATs (no plaintext, no hash)
//	DELETE /api/pat/{id}  — revoke (hard-delete) one of current user's PATs
//
// Why a custom surface instead of the auto-generated PocketBase
// collection API? Two reasons:
//
//  1. The plaintext is generated server-side and exists only in the
//     POST response body. Going through the generic collection API
//     would either require the client to invent the plaintext
//     (insecure: the browser sees fewer crypto-quality random bits)
//     or skip it and let the create rule whitelist hash injection
//     (also bad).
//
//  2. We want a predictable response shape — `{id, name, prefix,
//     plaintext, scopes, expires_at, created}` — independent of the
//     collection schema, so future migrations don't break CLI tools
//     that consume this surface.
func RegisterPAT(se *core.ServeEvent, app core.App) {
	se.Router.POST("/api/pat", sessionGuard(patCreateHandler(app)))
	se.Router.GET("/api/pat", sessionGuard(patListHandler(app)))
	se.Router.DELETE("/api/pat/{id}", sessionGuard(patDeleteHandler(app)))

	// GET /api/pat/scopes — expose the canonical scope vocabulary to
	// the SPA so the create form can render checkboxes with current
	// labels and descriptions without hardcoding them in TypeScript.
	// Public read — knowing what scopes exist is not sensitive (the
	// strings are also visible in any error message from scopeGuard).
	se.Router.GET("/api/pat/scopes", func(re *core.RequestEvent) error {
		entries := make([]map[string]string, 0, len(pat.AllScopes))
		for _, s := range pat.AllScopes {
			entries = append(entries, map[string]string{
				"name":        s,
				"description": pat.ScopeDescription[s],
			})
		}
		return re.JSON(http.StatusOK, map[string]any{
			"scopes":           entries,
			"max_expiry_days":  MaxPATExpiryDays,
		})
	})
}

// patCreateRequest is the JSON body shape accepted by POST /api/pat.
// All three fields beyond name/description are required: an empty
// scope list still parses (means "this token can call nothing"), but
// expires_in_days must be a positive integer ≤ MaxPATExpiryDays.
type patCreateRequest struct {
	Name          string   `json:"name"`
	Description   string   `json:"description,omitempty"`
	ExpiresInDays int      `json:"expires_in_days"` // REQUIRED: 1..MaxPATExpiryDays
	Scopes        []string `json:"scopes"`          // REQUIRED: may be empty for "no permissions"
}

// patCreateResponse is what the SPA / CLI sees back. Plaintext is
// included exactly once here; subsequent GETs only return prefix.
type patCreateResponse struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Prefix      string   `json:"prefix"`
	Plaintext   string   `json:"plaintext"`
	Description string   `json:"description,omitempty"`
	Scopes      []string `json:"scopes"`
	ExpiresAt   string   `json:"expires_at"`
	Created     string   `json:"created"`
}

// patSummary is the listed shape — explicitly no plaintext, no hash.
type patSummary struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Prefix      string   `json:"prefix"`
	Description string   `json:"description,omitempty"`
	Scopes      []string `json:"scopes"`
	ExpiresAt   string   `json:"expires_at"`
	LastUsedAt  string   `json:"last_used_at,omitempty"`
	Created     string   `json:"created"`
}

func patCreateHandler(app core.App) func(re *core.RequestEvent) error {
	return func(re *core.RequestEvent) error {
		user := re.Auth // sessionGuard guarantees non-nil + browser-sourced
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

		// Expiry is mandatory (GitHub fine-grained PAT semantics).
		// 0 / negative / >365 are all rejected — operators can't
		// accidentally create immortal tokens.
		if body.ExpiresInDays <= 0 {
			return re.JSON(http.StatusBadRequest, map[string]string{
				"detail": "expires_in_days is required and must be > 0 (fine-grained PATs cannot be perpetual)",
			})
		}
		if body.ExpiresInDays > MaxPATExpiryDays {
			return re.JSON(http.StatusBadRequest, map[string]string{
				"detail": "expires_in_days exceeds maximum of " + itoa(MaxPATExpiryDays) + " (one year)",
			})
		}

		// Scope validation — bogus / wildcard scopes are 400, not 500.
		// Empty scopes is allowed; the resulting PAT just can't call
		// any write endpoint (default-deny). User can still use it as
		// a read-credential placeholder if/when read endpoints get
		// gated in the future.
		if err := pat.ValidateScopes(body.Scopes); err != nil {
			return re.JSON(http.StatusBadRequest, map[string]string{"detail": err.Error()})
		}

		plaintext, prefix, hash, err := pat.Generate()
		if err != nil {
			slog.Error("pat: token generation failed", "user_id", user.Id, "error", err)
			return re.JSON(http.StatusInternalServerError, map[string]string{"detail": "internal server error"})
		}

		collection, err := app.FindCollectionByNameOrId(pat.CollectionName)
		if err != nil {
			slog.Error("pat: collection lookup failed", "collection", pat.CollectionName, "error", err)
			return re.JSON(http.StatusInternalServerError, map[string]string{"detail": "internal server error"})
		}
		rec := core.NewRecord(collection)
		rec.Set("user", user.Id)
		rec.Set("name", body.Name)
		rec.Set("prefix", prefix)
		rec.Set("token_hash", hash)
		if body.Description != "" {
			rec.Set("description", body.Description)
		}
		// Always JSON-encode scopes (even empty slice) so the column
		// always parses; decodeScopes tolerates "[]" / "null" / "" alike.
		scopesJSON, _ := json.Marshal(body.Scopes)
		rec.Set("scopes", string(scopesJSON))

		expires := types.NowDateTime().AddDate(0, 0, body.ExpiresInDays)
		rec.Set("expires_at", expires)

		if err := app.Save(rec); err != nil {
			slog.Error("pat: save record failed", "user_id", user.Id, "error", err)
			return re.JSON(http.StatusInternalServerError, map[string]string{"detail": "internal server error"})
		}

		return re.JSON(http.StatusOK, patCreateResponse{
			ID:          rec.Id,
			Name:        body.Name,
			Prefix:      prefix,
			Plaintext:   plaintext,
			Description: body.Description,
			Scopes:      body.Scopes,
			ExpiresAt:   expires.String(),
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
			slog.Error("pat: list records failed", "user_id", user.Id, "error", err)
			return re.JSON(http.StatusInternalServerError, map[string]string{"detail": "internal server error"})
		}

		summaries := make([]patSummary, 0, len(records))
		for _, rec := range records {
			// Defensive: ensure scopes is always a non-nil slice in
			// the response so the SPA can `.map` without a guard.
			scopes := decodeScopesField(rec.GetString("scopes"))
			if scopes == nil {
				scopes = []string{}
			}
			summaries = append(summaries, patSummary{
				ID:          rec.Id,
				Name:        rec.GetString("name"),
				Prefix:      rec.GetString("prefix"),
				Description: rec.GetString("description"),
				Scopes:      scopes,
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
			slog.Error("pat: delete record failed", "id", id, "user_id", user.Id, "error", err)
			return re.JSON(http.StatusInternalServerError, map[string]string{"detail": "internal server error"})
		}
		return re.JSON(http.StatusOK, map[string]bool{"ok": true})
	}
}

// decodeScopesField mirrors auth.go's decodeScopes — duplicated here
// (rather than exported and shared) because the auth path needs to be
// independent of the routes/pat surface and vice versa. Kept short
// enough that drift between the two implementations would be obvious.
func decodeScopesField(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "null" {
		return nil
	}
	var out []string
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil
	}
	return out
}

// nonZeroDate returns "" for a zero DateTime, otherwise its string
// rendering. PocketBase's DateField default is the zero value, which
// would otherwise serialize as "0001-01-01 00:00:00.000Z" —
// surprising for the SPA and harder to render conditionally.
func nonZeroDate(dt types.DateTime) string {
	if dt.Time().IsZero() {
		return ""
	}
	return dt.String()
}

// markPATUsed bumps last_used_at on the record asynchronously. It is
// called by authGuard (in auth.go) after accepting a PAT-authenticated
// request; failures are logged but never block the response. Kept
// here (next to the handlers it complements) so the auth.go file
// doesn't grow a pat-package import just for one line of bookkeeping.
func markPATUsed(app core.App, rec *core.Record) {
	go func() {
		if err := pat.MarkUsed(app, rec); err != nil {
			slog.Warn("pat: failed to mark token used", "id", rec.Id, "error", err)
		}
	}()
}

// itoa avoids pulling strconv into a file that otherwise has no
// numeric formatting needs. Three-line helper is cheaper than the
// extra import + careful Sprintf invocations.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
