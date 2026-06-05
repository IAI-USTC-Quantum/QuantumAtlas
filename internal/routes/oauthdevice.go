package routes

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/oauthdevice"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/pat"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/types"
)

// RegisterOAuthDevice wires the /api/oauth/device/* surface used by
// `qatlas auth login --device`. The flow follows RFC 8628 with
// QuantumAtlas-specific tweaks documented in
// internal/oauthdevice/oauthdevice.go.
//
// Anonymous (no auth required):
//
//	POST /api/oauth/device/code   — start a flow, returns user/device codes
//	POST /api/oauth/device/token  — poll for the minted PAT plaintext
//
// sessionGuard (browser session only — PATs explicitly rejected, same
// reasoning as /api/pat: a leaked PAT must not be allowed to mint or
// approve more PATs):
//
//	GET  /api/oauth/device/code     — look up a pending request by user_code
//	POST /api/oauth/device/approve  — approve a pending request
//	POST /api/oauth/device/deny     — deny a pending request
//
// All five endpoints route through this file rather than the generic
// PocketBase collection API for the same reasons RegisterPAT bypasses
// it: the response shape needs to be stable independent of the
// underlying schema, and several transitions are atomic state
// machines that don't map cleanly to "update a record".
func RegisterOAuthDevice(se *core.ServeEvent, app core.App) {
	se.Router.POST("/api/oauth/device/code", oauthDeviceCodeHandler(app))
	se.Router.POST("/api/oauth/device/token", oauthDeviceTokenHandler(app))
	se.Router.GET("/api/oauth/device/code", sessionGuard(oauthDeviceLookupHandler(app)))
	se.Router.POST("/api/oauth/device/approve", sessionGuard(oauthDeviceApproveHandler(app)))
	se.Router.POST("/api/oauth/device/deny", sessionGuard(oauthDeviceDenyHandler(app)))
}

// oauthDeviceCodeRequest mirrors patCreateRequest field-for-field so
// the CLI can use the same JSON body shape for both /api/pat and the
// device-flow start endpoint. Required fields are validated below.
type oauthDeviceCodeRequest struct {
	Name          string   `json:"name"`
	Description   string   `json:"description,omitempty"`
	ExpiresInDays int      `json:"expires_in_days"`
	Scopes        []string `json:"scopes"`
}

// oauthDeviceCodeResponse matches RFC 8628 §3.2 wire shape, plus the
// `verification_uri_complete` extension. The `interval` and
// `expires_in` are in seconds; the SPA's /<lang>/device page renders
// `verification_uri_complete` as a deep-link so the user only needs
// to confirm a pre-filled code.
type oauthDeviceCodeResponse struct {
	DeviceCode              string `json:"device_code"`
	UserCode                string `json:"user_code"`
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete"`
	ExpiresIn               int    `json:"expires_in"`
	Interval                int    `json:"interval"`
}

func oauthDeviceCodeHandler(app core.App) func(re *core.RequestEvent) error {
	return func(re *core.RequestEvent) error {
		raw, err := io.ReadAll(io.LimitReader(re.Request.Body, 16<<10))
		if err != nil {
			return re.JSON(http.StatusBadRequest, map[string]string{"detail": "read body: " + err.Error()})
		}
		var body oauthDeviceCodeRequest
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
		if body.ExpiresInDays <= 0 {
			return re.JSON(http.StatusBadRequest, map[string]string{
				"detail": "expires_in_days is required and must be > 0",
			})
		}
		if body.ExpiresInDays > MaxPATExpiryDays {
			return re.JSON(http.StatusBadRequest, map[string]string{
				"detail": "expires_in_days exceeds maximum of " + itoa(MaxPATExpiryDays),
			})
		}
		if err := pat.ValidateScopes(body.Scopes); err != nil {
			return re.JSON(http.StatusBadRequest, map[string]string{"detail": err.Error()})
		}

		// Retry generation a few times to absorb the (astronomically
		// unlikely) collision on either device_code_hash or user_code.
		// We cap retries at 3 because each retry costs us a unique
		// crypto/rand draw and a DB insert attempt; 3 attempts ≈
		// 2^(-120) joint collision probability, which is way past
		// the meaningful threshold.
		collection, err := app.FindCollectionByNameOrId(oauthdevice.CollectionName)
		if err != nil {
			slog.Error("oauthdevice: collection lookup failed", "collection", oauthdevice.CollectionName, "error", err)
			return re.JSON(http.StatusInternalServerError, map[string]string{"detail": "internal server error"})
		}

		var (
			deviceCode string
			userCode   string
			rec        *core.Record
		)
		for attempt := 0; attempt < 3; attempt++ {
			dc, uc, err := oauthdevice.GenerateCodes()
			if err != nil {
				slog.Error("oauthdevice: code generation failed", "error", err)
				return re.JSON(http.StatusInternalServerError, map[string]string{"detail": "internal server error"})
			}

			candidate := core.NewRecord(collection)
			candidate.Set("device_code_hash", oauthdevice.HashDeviceCode(dc))
			candidate.Set("user_code", uc)
			candidate.Set("name", body.Name)
			if body.Description != "" {
				candidate.Set("description", body.Description)
			}
			scopesJSON, _ := json.Marshal(body.Scopes)
			candidate.Set("scopes", string(scopesJSON))
			candidate.Set("expires_in_days", body.ExpiresInDays)
			candidate.Set("status", oauthdevice.StatusPending)
			candidate.Set("poll_count", 0)
			candidate.Set("expires_at", types.NowDateTime().Add(time.Duration(oauthdevice.ExpiresInSeconds)*time.Second))

			if err := app.Save(candidate); err != nil {
				if isUniqueConstraintErr(err) {
					continue // try a fresh pair
				}
				slog.Error("oauthdevice: save record failed", "error", err)
				return re.JSON(http.StatusInternalServerError, map[string]string{"detail": "internal server error"})
			}
			deviceCode = dc
			userCode = uc
			rec = candidate
			break
		}
		if rec == nil {
			slog.Error("oauthdevice: exhausted code-generation retries")
			return re.JSON(http.StatusInternalServerError, map[string]string{"detail": "internal server error"})
		}

		base := re.Request.Header.Get("X-Forwarded-Origin")
		if base == "" {
			base = oauthDeviceCanonicalOrigin(re)
		}
		verifyURI := strings.TrimRight(base, "/") + "/device"
		verifyURIComplete := verifyURI + "?user_code=" + userCode

		return re.JSON(http.StatusOK, oauthDeviceCodeResponse{
			DeviceCode:              deviceCode,
			UserCode:                userCode,
			VerificationURI:         verifyURI,
			VerificationURIComplete: verifyURIComplete,
			ExpiresIn:               oauthdevice.ExpiresInSeconds,
			Interval:                oauthdevice.PollIntervalSeconds,
		})
	}
}

// oauthDeviceTokenRequest is the CLI's poll body. We accept JSON to
// stay consistent with the rest of the QuantumAtlas API (RFC 8628
// uses form-encoded; we don't bother). client_id is omitted because
// this server has no notion of registered OAuth clients — the device
// code IS the credential.
type oauthDeviceTokenRequest struct {
	DeviceCode string `json:"device_code"`
}

// oauthDeviceTokenResponse is the success-case body. Matches the
// /api/pat create response shape so the CLI can use the same parser
// for both flows.
type oauthDeviceTokenResponse struct {
	PATID     string   `json:"pat_id"`
	Name      string   `json:"name"`
	Prefix    string   `json:"prefix"`
	Plaintext string   `json:"plaintext"`
	Scopes    []string `json:"scopes"`
	ExpiresAt string   `json:"expires_at"`
}

// oauthDeviceTokenError carries the RFC 8628 §3.5 error strings. Used
// for every non-success outcome on /token; the HTTP status is always
// 400 per the RFC so the CLI can switch on `error` alone.
type oauthDeviceTokenError struct {
	Error string `json:"error"`
}

func oauthDeviceTokenHandler(app core.App) func(re *core.RequestEvent) error {
	return func(re *core.RequestEvent) error {
		raw, err := io.ReadAll(io.LimitReader(re.Request.Body, 4<<10))
		if err != nil {
			return re.JSON(http.StatusBadRequest, map[string]string{"detail": "read body: " + err.Error()})
		}
		var body oauthDeviceTokenRequest
		if err := json.Unmarshal(raw, &body); err != nil {
			return re.JSON(http.StatusBadRequest, map[string]string{"detail": "parse body: " + err.Error()})
		}
		if body.DeviceCode == "" {
			return re.JSON(http.StatusBadRequest, oauthDeviceTokenError{Error: "invalid_grant"})
		}

		hash := oauthdevice.HashDeviceCode(body.DeviceCode)
		rec, err := app.FindFirstRecordByFilter(
			oauthdevice.CollectionName,
			"device_code_hash = {:h}",
			map[string]any{"h": hash},
		)
		if err != nil {
			// Either not found or DB error; treat both as invalid_grant.
			// We never want to leak "this hash exists" via a different
			// status — that's the whole point of hashing the code.
			return re.JSON(http.StatusBadRequest, oauthDeviceTokenError{Error: "invalid_grant"})
		}

		now := types.NowDateTime()

		// Hard cap on polls — catches misbehaving clients and bounds
		// the cost of a leaked-but-unapproved device code.
		if rec.GetInt("poll_count") >= oauthdevice.MaxPollCount {
			_ = oauthDeviceMarkExpired(app, rec.Id)
			return re.JSON(http.StatusBadRequest, oauthDeviceTokenError{Error: "expired_token"})
		}

		// Expiry sweep: if the TTL passed, mark expired and respond
		// accordingly. We don't trust a stale `status` column — clock
		// is the source of truth.
		expiresAt := rec.GetDateTime("expires_at")
		if !expiresAt.Time().IsZero() && now.Time().After(expiresAt.Time()) {
			_ = oauthDeviceMarkExpired(app, rec.Id)
			return re.JSON(http.StatusBadRequest, oauthDeviceTokenError{Error: "expired_token"})
		}

		// Interval enforcement — `slow_down` if the caller polled
		// faster than the published interval. We compare with a 500ms
		// grace so light clock drift / network jitter doesn't false-
		// positive a polite client.
		lastPolled := rec.GetDateTime("last_polled_at").Time()
		if !lastPolled.IsZero() {
			elapsed := now.Time().Sub(lastPolled)
			// 500ms grace absorbs minor clock drift / network jitter
			// before flagging an early poll as slow_down.
			minimum := time.Duration(oauthdevice.PollIntervalSeconds)*time.Second - 500*time.Millisecond
			if elapsed < minimum {
				// Bump poll_count + last_polled_at even on slow_down
				// so an adversary can't keep polling for free; cap
				// applies to all attempts, not just legitimate ones.
				_ = oauthDeviceBumpPoll(app, rec.Id, now)
				return re.JSON(http.StatusBadRequest, oauthDeviceTokenError{Error: "slow_down"})
			}
		}
		_ = oauthDeviceBumpPoll(app, rec.Id, now)

		status := rec.GetString("status")
		switch status {
		case oauthdevice.StatusPending:
			return re.JSON(http.StatusBadRequest, oauthDeviceTokenError{Error: "authorization_pending"})
		case oauthdevice.StatusDenied:
			return re.JSON(http.StatusBadRequest, oauthDeviceTokenError{Error: "access_denied"})
		case oauthdevice.StatusExpired:
			return re.JSON(http.StatusBadRequest, oauthDeviceTokenError{Error: "expired_token"})
		case oauthdevice.StatusConsumed:
			// PAT plaintext was returned on an earlier poll. Returning
			// it again would defeat the point of the once-only
			// contract; the CLI must treat re-consumption as fatal.
			return re.JSON(http.StatusBadRequest, oauthDeviceTokenError{Error: "invalid_grant"})
		case oauthdevice.StatusApproved:
			// Fall through to the mint+consume transaction below.
		default:
			slog.Error("oauthdevice: unexpected status", "id", rec.Id, "status", status)
			return re.JSON(http.StatusInternalServerError, map[string]string{"detail": "internal server error"})
		}

		// approved → consumed: do the PAT mint inside a transaction
		// guarded by a conditional UPDATE so concurrent polls can't
		// both win. The first poll's UPDATE flips status to consumed
		// (zero rows affected for losers); the loser then sees
		// status=consumed on the re-fetch and returns invalid_grant
		// on its next poll.
		approvedUser := rec.GetString("approved_user")
		if approvedUser == "" {
			slog.Error("oauthdevice: approved row has no approved_user", "id", rec.Id)
			return re.JSON(http.StatusInternalServerError, map[string]string{"detail": "internal server error"})
		}

		var resp oauthDeviceTokenResponse
		txErr := app.RunInTransaction(func(txApp core.App) error {
			result, err := txApp.DB().NewQuery(
				"UPDATE " + oauthdevice.CollectionName +
					" SET status = {:to} WHERE id = {:id} AND status = {:from}",
			).Bind(dbx.Params{
				"to":   oauthdevice.StatusConsumed,
				"from": oauthdevice.StatusApproved,
				"id":   rec.Id,
			}).Execute()
			if err != nil {
				return fmt.Errorf("transition approved→consumed: %w", err)
			}
			affected, err := result.RowsAffected()
			if err != nil {
				return fmt.Errorf("rows affected: %w", err)
			}
			if affected == 0 {
				// Lost the race or the row already changed under us;
				// surface as a sentinel so the outer code returns
				// invalid_grant without 500ing.
				return errRaceLost
			}

			plaintext, prefix, hashStr, err := pat.Generate()
			if err != nil {
				return fmt.Errorf("pat.Generate: %w", err)
			}

			patCol, err := txApp.FindCollectionByNameOrId(pat.CollectionName)
			if err != nil {
				return fmt.Errorf("find pat collection: %w", err)
			}
			patRec := core.NewRecord(patCol)
			patRec.Set("user", approvedUser)
			patRec.Set("name", rec.GetString("name"))
			patRec.Set("prefix", prefix)
			patRec.Set("token_hash", hashStr)
			if desc := rec.GetString("description"); desc != "" {
				patRec.Set("description", desc)
			}
			scopes := decodeScopesField(rec.GetString("scopes"))
			scopesJSON, _ := json.Marshal(scopes)
			patRec.Set("scopes", string(scopesJSON))
			expires := types.NowDateTime().AddDate(0, 0, rec.GetInt("expires_in_days"))
			patRec.Set("expires_at", expires)
			if err := txApp.Save(patRec); err != nil {
				return fmt.Errorf("save pat: %w", err)
			}

			// Backfill the audit link on the device-code row. This is
			// best-effort — failing to set the relation does not
			// invalidate the already-minted PAT, but losing the link
			// would make audit-by-device-code impossible later.
			if _, err := txApp.DB().NewQuery(
				"UPDATE " + oauthdevice.CollectionName +
					" SET pat = {:pat} WHERE id = {:id}",
			).Bind(dbx.Params{
				"pat": patRec.Id,
				"id":  rec.Id,
			}).Execute(); err != nil {
				return fmt.Errorf("link pat to device row: %w", err)
			}

			if scopes == nil {
				scopes = []string{}
			}
			resp = oauthDeviceTokenResponse{
				PATID:     patRec.Id,
				Name:      patRec.GetString("name"),
				Prefix:    prefix,
				Plaintext: plaintext,
				Scopes:    scopes,
				ExpiresAt: expires.String(),
			}
			return nil
		})
		if txErr != nil {
			if errors.Is(txErr, errRaceLost) {
				return re.JSON(http.StatusBadRequest, oauthDeviceTokenError{Error: "invalid_grant"})
			}
			slog.Error("oauthdevice: mint transaction failed", "id", rec.Id, "error", txErr)
			return re.JSON(http.StatusInternalServerError, map[string]string{"detail": "internal server error"})
		}
		return re.JSON(http.StatusOK, resp)
	}
}

// oauthDeviceLookupResponse is what /<lang>/device renders to show
// the user what they're approving. Plaintext device code is never
// returned (we don't even have it on disk).
//
// `Scopes`, `Name` and `ExpiresInDays` are the CLI-seeded defaults
// (echoed back so the SPA can pre-fill the form). `AvailableScopes` +
// `ScopeDescriptions` carry the full vocabulary the user is allowed
// to choose from — the SPA renders a checkbox per entry. The user
// can edit name / scopes / expiry in the browser and the eventual
// /approve POST sends back the edited values to override the row.
type oauthDeviceLookupResponse struct {
	UserCode          string            `json:"user_code"`
	Name              string            `json:"name"`
	Description       string            `json:"description,omitempty"`
	Scopes            []string          `json:"scopes"`
	ExpiresInDays     int               `json:"expires_in_days"`
	Status            string            `json:"status"`
	AvailableScopes   []string          `json:"available_scopes"`
	ScopeDescriptions map[string]string `json:"scope_descriptions"`
	MaxExpiryDays     int               `json:"max_expiry_days"`
}

func oauthDeviceLookupHandler(app core.App) func(re *core.RequestEvent) error {
	return func(re *core.RequestEvent) error {
		raw := re.Request.URL.Query().Get("user_code")
		canonical, err := oauthdevice.NormalizeUserCode(raw)
		if err != nil {
			return re.JSON(http.StatusBadRequest, map[string]string{"detail": "invalid user_code"})
		}
		rec, err := app.FindFirstRecordByFilter(
			oauthdevice.CollectionName,
			"user_code = {:u}",
			map[string]any{"u": canonical},
		)
		if err != nil {
			return re.JSON(http.StatusNotFound, map[string]string{"detail": "not found"})
		}

		// Reflect expiry into status before serving so the SPA
		// doesn't see a "pending" row that's already dead.
		status := rec.GetString("status")
		if status == oauthdevice.StatusPending {
			expiresAt := rec.GetDateTime("expires_at")
			if !expiresAt.Time().IsZero() && expiresAt.Time().Before(types.NowDateTime().Time()) {
				_ = oauthDeviceMarkExpired(app, rec.Id)
				status = oauthdevice.StatusExpired
			}
		}

		scopes := decodeScopesField(rec.GetString("scopes"))
		if scopes == nil {
			scopes = []string{}
		}
		return re.JSON(http.StatusOK, oauthDeviceLookupResponse{
			UserCode:          canonical,
			Name:              rec.GetString("name"),
			Description:       rec.GetString("description"),
			Scopes:            scopes,
			ExpiresInDays:     rec.GetInt("expires_in_days"),
			Status:            status,
			AvailableScopes:   append([]string(nil), pat.AllScopes...),
			ScopeDescriptions: copyScopeDescriptions(),
			MaxExpiryDays:     MaxPATExpiryDays,
		})
	}
}

// copyScopeDescriptions returns a defensive copy of pat.ScopeDescription
// so callers cannot mutate the package-level map by accident.
func copyScopeDescriptions() map[string]string {
	out := make(map[string]string, len(pat.ScopeDescription))
	for k, v := range pat.ScopeDescription {
		out[k] = v
	}
	return out
}

// oauthDeviceApproveRequest accepts an optional override block so the
// browser-side user can edit the CLI-seeded name / scopes / expiry on
// the /<lang>/device approval form before clicking Approve. Any
// non-nil pointer here replaces the corresponding column on the
// device-code row inside the same conditional UPDATE that flips
// status pending→approved (i.e. you cannot edit a row that has
// already been approved by someone else).
type oauthDeviceApproveRequest struct {
	UserCode      string    `json:"user_code"`
	Name          *string   `json:"name,omitempty"`
	Scopes        *[]string `json:"scopes,omitempty"`
	ExpiresInDays *int      `json:"expires_in_days,omitempty"`
}

func oauthDeviceApproveHandler(app core.App) func(re *core.RequestEvent) error {
	return func(re *core.RequestEvent) error {
		user := re.Auth
		if user == nil {
			return re.JSON(http.StatusUnauthorized, map[string]string{"detail": "missing auth"})
		}
		raw, err := io.ReadAll(io.LimitReader(re.Request.Body, 4<<10))
		if err != nil {
			return re.JSON(http.StatusBadRequest, map[string]string{"detail": "read body: " + err.Error()})
		}
		var body oauthDeviceApproveRequest
		if err := json.Unmarshal(raw, &body); err != nil {
			return re.JSON(http.StatusBadRequest, map[string]string{"detail": "parse body: " + err.Error()})
		}
		canonical, err := oauthdevice.NormalizeUserCode(body.UserCode)
		if err != nil {
			return re.JSON(http.StatusBadRequest, map[string]string{"detail": "invalid user_code"})
		}

		// Validate any present overrides BEFORE we touch the DB. This
		// keeps the conditional UPDATE simple — by the time we run
		// it, every field we're about to set is already vetted.
		var (
			newName      *string
			newScopesStr *string
			newDays      *int
		)
		if body.Name != nil {
			trimmed := strings.TrimSpace(*body.Name)
			if trimmed == "" {
				return re.JSON(http.StatusBadRequest, map[string]string{"detail": "name cannot be empty"})
			}
			if len(trimmed) > 80 {
				return re.JSON(http.StatusBadRequest, map[string]string{"detail": "name max length is 80"})
			}
			newName = &trimmed
		}
		if body.Scopes != nil {
			scopes := *body.Scopes
			if err := pat.ValidateScopes(scopes); err != nil {
				return re.JSON(http.StatusBadRequest, map[string]string{"detail": err.Error()})
			}
			if scopes == nil {
				scopes = []string{}
			}
			encoded, _ := json.Marshal(scopes)
			s := string(encoded)
			newScopesStr = &s
		}
		if body.ExpiresInDays != nil {
			d := *body.ExpiresInDays
			if d <= 0 {
				return re.JSON(http.StatusBadRequest, map[string]string{"detail": "expires_in_days must be > 0"})
			}
			if d > MaxPATExpiryDays {
				return re.JSON(http.StatusBadRequest, map[string]string{
					"detail": "expires_in_days exceeds maximum of " + itoa(MaxPATExpiryDays),
				})
			}
			newDays = &d
		}

		rec, err := app.FindFirstRecordByFilter(
			oauthdevice.CollectionName,
			"user_code = {:u}",
			map[string]any{"u": canonical},
		)
		if err != nil {
			return re.JSON(http.StatusNotFound, map[string]string{"detail": "not found"})
		}

		now := types.NowDateTime()
		expiresAt := rec.GetDateTime("expires_at")
		if !expiresAt.Time().IsZero() && expiresAt.Time().Before(now.Time()) {
			_ = oauthDeviceMarkExpired(app, rec.Id)
			return re.JSON(http.StatusGone, map[string]string{"detail": "device code expired"})
		}

		// Single atomic conditional UPDATE: flip status pending →
		// approved AND apply any name/scopes/expires overrides in
		// the same row write. The WHERE status='pending' guard
		// closes the TOCTOU window — concurrent approves or a
		// denial that lands first result in zero rows affected and
		// a 409 conflict.
		setClauses := []string{
			"status = {:to}",
			"approved_user = {:u}",
			"approved_at = {:t}",
		}
		params := dbx.Params{
			"to":   oauthdevice.StatusApproved,
			"from": oauthdevice.StatusPending,
			"u":    user.Id,
			"t":    now,
			"id":   rec.Id,
		}
		if newName != nil {
			setClauses = append(setClauses, "name = {:name}")
			params["name"] = *newName
		}
		if newScopesStr != nil {
			setClauses = append(setClauses, "scopes = {:scopes}")
			params["scopes"] = *newScopesStr
		}
		if newDays != nil {
			setClauses = append(setClauses, "expires_in_days = {:days}")
			params["days"] = *newDays
		}
		sql := "UPDATE " + oauthdevice.CollectionName +
			" SET " + strings.Join(setClauses, ", ") +
			" WHERE id = {:id} AND status = {:from}"
		result, err := app.DB().NewQuery(sql).Bind(params).Execute()
		if err != nil {
			slog.Error("oauthdevice: approve update failed", "id", rec.Id, "error", err)
			return re.JSON(http.StatusInternalServerError, map[string]string{"detail": "internal server error"})
		}
		affected, _ := result.RowsAffected()
		if affected == 0 {
			return re.JSON(http.StatusConflict, map[string]string{"detail": "device code is not pending"})
		}

		// Re-read so the response reflects the final persisted
		// values (in particular, the scopes / name / expiry that
		// will be minted on the next /token poll).
		finalName := rec.GetString("name")
		if newName != nil {
			finalName = *newName
		}
		finalScopes := decodeScopesField(rec.GetString("scopes"))
		if newScopesStr != nil {
			_ = json.Unmarshal([]byte(*newScopesStr), &finalScopes)
		}
		if finalScopes == nil {
			finalScopes = []string{}
		}
		finalDays := rec.GetInt("expires_in_days")
		if newDays != nil {
			finalDays = *newDays
		}

		return re.JSON(http.StatusOK, map[string]any{
			"ok":              true,
			"status":          oauthdevice.StatusApproved,
			"user_code":       canonical,
			"name":            finalName,
			"scopes":          finalScopes,
			"expires_in_days": finalDays,
		})
	}
}

// oauthDeviceUserCodeBody parses {"user_code": "..."} for /deny.
type oauthDeviceUserCodeBody struct {
	UserCode string `json:"user_code"`
}

func oauthDeviceDenyHandler(app core.App) func(re *core.RequestEvent) error {
	return func(re *core.RequestEvent) error {
		canonical, err := oauthDeviceReadUserCodeBody(re)
		if err != nil {
			return re.JSON(http.StatusBadRequest, map[string]string{"detail": err.Error()})
		}
		rec, err := app.FindFirstRecordByFilter(
			oauthdevice.CollectionName,
			"user_code = {:u}",
			map[string]any{"u": canonical},
		)
		if err != nil {
			return re.JSON(http.StatusNotFound, map[string]string{"detail": "not found"})
		}
		result, err := app.DB().NewQuery(
			"UPDATE " + oauthdevice.CollectionName +
				" SET status = {:to} WHERE id = {:id} AND status = {:from}",
		).Bind(dbx.Params{
			"to":   oauthdevice.StatusDenied,
			"from": oauthdevice.StatusPending,
			"id":   rec.Id,
		}).Execute()
		if err != nil {
			slog.Error("oauthdevice: deny update failed", "id", rec.Id, "error", err)
			return re.JSON(http.StatusInternalServerError, map[string]string{"detail": "internal server error"})
		}
		affected, _ := result.RowsAffected()
		if affected == 0 {
			return re.JSON(http.StatusConflict, map[string]string{"detail": "device code is not pending"})
		}
		return re.JSON(http.StatusOK, map[string]any{
			"ok":        true,
			"status":    oauthdevice.StatusDenied,
			"user_code": canonical,
		})
	}
}

// oauthDeviceReadUserCodeBody parses {"user_code": "..."} and applies
// NormalizeUserCode. Used by both /approve and /deny.
func oauthDeviceReadUserCodeBody(re *core.RequestEvent) (string, error) {
	raw, err := io.ReadAll(io.LimitReader(re.Request.Body, 4<<10))
	if err != nil {
		return "", fmt.Errorf("read body: %w", err)
	}
	var body oauthDeviceUserCodeBody
	if err := json.Unmarshal(raw, &body); err != nil {
		return "", fmt.Errorf("parse body: %w", err)
	}
	canonical, err := oauthdevice.NormalizeUserCode(body.UserCode)
	if err != nil {
		return "", fmt.Errorf("invalid user_code")
	}
	return canonical, nil
}

// oauthDeviceMarkExpired flips a row to expired iff it's still in a
// non-terminal state. Best-effort: failures are logged and swallowed
// because every caller is on the rejection path already.
func oauthDeviceMarkExpired(app core.App, id string) error {
	_, err := app.DB().NewQuery(
		"UPDATE " + oauthdevice.CollectionName +
			" SET status = {:to} WHERE id = {:id} AND status IN ({:p}, {:a})",
	).Bind(dbx.Params{
		"to": oauthdevice.StatusExpired,
		"id": id,
		"p":  oauthdevice.StatusPending,
		"a":  oauthdevice.StatusApproved,
	}).Execute()
	if err != nil {
		slog.Warn("oauthdevice: failed to mark row expired", "id", id, "error", err)
	}
	return err
}

// oauthDeviceBumpPoll increments poll_count and stamps last_polled_at.
// Called on every /token call — including rejected ones — to enforce
// the global poll cap regardless of behaviour.
func oauthDeviceBumpPoll(app core.App, id string, now types.DateTime) error {
	_, err := app.DB().NewQuery(
		"UPDATE " + oauthdevice.CollectionName +
			" SET poll_count = poll_count + 1, last_polled_at = {:t} WHERE id = {:id}",
	).Bind(dbx.Params{
		"t":  now,
		"id": id,
	}).Execute()
	if err != nil {
		slog.Warn("oauthdevice: failed to bump poll counters", "id", id, "error", err)
	}
	return err
}

// oauthDeviceCanonicalOrigin returns the scheme+host the request hit,
// honouring X-Forwarded-Proto/Host so that requests reaching us
// through the Caddy reverse proxy report the public origin rather
// than the loopback bind. The result is used to build the
// verification URL we hand back to the CLI.
func oauthDeviceCanonicalOrigin(re *core.RequestEvent) string {
	proto := re.Request.Header.Get("X-Forwarded-Proto")
	if proto == "" {
		if re.Request.TLS != nil {
			proto = "https"
		} else {
			proto = "http"
		}
	}
	host := re.Request.Header.Get("X-Forwarded-Host")
	if host == "" {
		host = re.Request.Host
	}
	return proto + "://" + host
}

// errRaceLost is a sentinel passed out of the mint transaction when
// our conditional UPDATE didn't see the expected `approved` status.
// The caller maps it to invalid_grant. It is NOT user-facing.
var errRaceLost = errors.New("oauthdevice: race lost on approved→consumed")

// isUniqueConstraintErr matches the SQLite "UNIQUE constraint
// failed: …" error so the /code handler can transparently retry on
// the (vanishingly rare) collision of either device_code_hash or
// user_code without bubbling a 500 to the CLI.
func isUniqueConstraintErr(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "UNIQUE constraint failed")
}
