// OAuth sign-in conflict detection: when a brand-new OAuth identity arrives
// that *may* belong to an existing QuantumAtlas account, interrupt the sign-in
// with a 409 the SPA turns into a "match this account?" prompt, instead of
// silently creating a second account (username conflict) or silently grafting
// the identity onto the existing record (PocketBase's default same-email
// auto-link, which is also an unverified-email takeover vector).
//
// The prompt's "match" path is entirely client-side UX: the user goes to
// /login, signs in with their EXISTING account, and binds the new provider
// from the dashboard (the bind flow links the identity to the authenticated
// record — PocketBase's own link semantics, see the mode:"link" handling in
// web/src/lib/auth.ts). The "keep them separate" path only exists for
// username conflicts (same email can never be a separate record — the users
// email column is unique), and is implemented with a short-lived in-memory
// acknowledgement: the 409 records that this identity was offered the
// choice, and a retried exchange within the TTL passes through.
package auth

import (
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
	pbauth "github.com/pocketbase/pocketbase/tools/auth"
)

// OAuthConflictCode is the marker the SPA matches on to render the match
// prompt instead of a generic sign-in failure.
const OAuthConflictCode = "oauth_conflict"

// conflictAckTTL bounds how long a "keep separate" acknowledgement stays
// valid. Generous enough to survive the authorize round trip + a slow human,
// short enough that a stale ack can't paper over a later conflict.
const conflictAckTTL = 15 * time.Minute

// conflictAcks is the process-local acknowledgement store. qatlasd is
// single-process per edge; losing it on restart just means the user sees the
// prompt once more.
var conflictAcks = &ackStore{seen: map[string]time.Time{}}

type ackStore struct {
	mu   sync.Mutex
	seen map[string]time.Time
}

// offer records that the identity was shown the conflict prompt.
func (s *ackStore) offer(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked()
	s.seen[key] = time.Now()
}

// acknowledged reports whether the identity was offered the prompt (and the
// offer is still within the TTL).
func (s *ackStore) acknowledged(key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked()
	ts, ok := s.seen[key]
	return ok && time.Since(ts) < conflictAckTTL
}

// pruneLocked drops expired entries; caller must hold mu.
func (s *ackStore) pruneLocked() {
	for k, ts := range s.seen {
		if time.Since(ts) >= conflictAckTTL {
			delete(s.seen, k)
		}
	}
}

// oauthConflict is the flat descriptor the 409 body carries. All strings —
// PocketBase error bodies serialize nested structures poorly, so the "which
// existing account" details are pre-formatted server-side.
type oauthConflict struct {
	conflictType string // "email" | "username"
	login        string // incoming provider login (may be empty)
	existing     string // human description of the conflicting account(s)
}

// checkOAuthConflicts runs inside the OnRecordAuthWithOAuth2Request hook,
// BEFORE PocketBase matches/creates the users record (see apis/record_auth_
// with_oauth2.go: the handler performs the externalAuth + email lookups and
// hands us the result via e.Record; the actual create/link happens in
// e.Next()). Returns the result of e.JSON(409, ...) — i.e. nil — when the
// sign-in is interrupted, which stops the hook chain without an error.
//
// Cases, in order:
//
//   - bind / self re-login (the authenticated caller IS e.Record): never
//     prompt — this is the dashboard "绑定" flow linking to the caller.
//   - returning identity (externalAuth row exists for provider+providerId):
//     never prompt — that's just a plain re-login.
//   - e.Record non-nil without an externalAuth row: PocketBase matched an
//     existing record by EMAIL. Interrupt: same-email cross-provider signins
//     must be an explicit choice, not a silent auto-link.
//   - fresh sign-up (e.Record nil) whose provider login equals an existing
//     github_login / gitea_login: interrupt once (username conflict); a
//     retried exchange within the ack TTL passes and creates the separate
//     account the user asked for.
func checkOAuthConflicts(e *core.RecordAuthWithOAuth2RequestEvent) error {
	if e == nil || e.OAuth2User == nil || e.Collection == nil || e.Collection.Name != UsersCollection {
		return nil
	}
	if e.ProviderName != pbauth.NameGithub && e.ProviderName != pbauth.NameGitea {
		return nil // only the two providers the matching UX knows about
	}

	// Bind / self flow: the authenticated caller links an identity to their
	// own record. PocketBase resolved e.Record to the caller (its
	// fallbackAuthRecord case) — never a conflict.
	if e.Auth != nil && e.Record != nil && e.Auth.Id == e.Record.Id &&
		e.Auth.Collection().Id == e.Record.Collection().Id {
		return nil
	}

	ackKey := e.ProviderName + ":" + e.OAuth2User.Id

	// Returning login: the identity is already linked — plain re-sign-in
	// (PocketBase's handler already resolved e.Record from this relation).
	rel, err := e.App.FindFirstExternalAuthByExpr(dbx.HashExp{
		"collectionRef": e.Collection.Id,
		"provider":      e.ProviderName,
		"providerId":    e.OAuth2User.Id,
	})
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return e.InternalServerError("Failed OAuth2 relation check.", err)
	}
	if rel != nil {
		return nil
	}

	login := strings.TrimSpace(e.OAuth2User.Username)

	// Email match: PocketBase would silently attach this new identity to the
	// existing record. Prompt instead.
	if e.Record != nil {
		return respondOAuthConflict(e, oauthConflict{
			conflictType: "email",
			login:        login,
			existing:     describeExistingAccount(e.Record),
		})
	}

	// Fresh sign-up: username conflicts only (an email conflict cannot reach
	// this branch — the lookup above would have set e.Record).
	if login == "" {
		return nil
	}
	matches := findLoginMatches(e.App, login)
	if len(matches) == 0 {
		return nil
	}
	if conflictAcks.acknowledged(ackKey) {
		slog.Info("oauth: conflict acknowledged; creating an independent account",
			"provider", e.ProviderName,
			"login", login,
		)
		return nil
	}
	conflictAcks.offer(ackKey)
	return respondOAuthConflict(e, oauthConflict{
		conflictType: "username",
		login:        login,
		existing:     strings.Join(matches, ", "),
	})
}

// respondOAuthConflict writes the 409 body the SPA matches on and stops the
// hook chain (writing the response directly instead of returning an
// apis.ApiError because PocketBase mangles non-validation data maps in
// ApiError.Data). Returns e.JSON's nil so the caller can propagate it.
func respondOAuthConflict(e *core.RecordAuthWithOAuth2RequestEvent, c oauthConflict) error {
	return e.JSON(http.StatusConflict, map[string]any{
		"status":  http.StatusConflict,
		"message": "This sign-in may match an existing QuantumAtlas account.",
		"data": map[string]any{
			"code":     OAuthConflictCode,
			"provider": e.ProviderName,
			"login":    c.login,
			"conflict": c.conflictType,
			"existing": c.existing,
		},
	})
}

// describeExistingAccount formats the matched record for the prompt — the
// logins and email the user needs to recognize their own account.
func describeExistingAccount(rec *core.Record) string {
	var logins []string
	if l := strings.TrimSpace(rec.GetString(GitHubLoginField)); l != "" {
		logins = append(logins, GitHubLoginField+":"+l)
	}
	if l := strings.TrimSpace(rec.GetString(GiteaLoginField)); l != "" {
		logins = append(logins, GiteaLoginField+":"+l)
	}
	loginPart := "no provider login"
	if len(logins) > 0 {
		loginPart = strings.Join(logins, ", ")
	}
	email := strings.TrimSpace(rec.Email())
	if email == "" {
		email = "no email"
	}
	return fmt.Sprintf("%s (%s)", loginPart, email)
}

// findLoginMatches returns "field:login" strings for every users record
// whose github_login or gitea_login equals the given login
// (case-insensitive). The users table is members-only small; a full scan is
// fine and avoids filter-expression quoting games.
func findLoginMatches(app core.App, login string) []string {
	want := strings.ToLower(strings.TrimSpace(login))
	if want == "" {
		return nil
	}
	records, err := app.FindAllRecords(UsersCollection)
	if err != nil {
		slog.Warn("oauth: conflict scan failed", "error", err)
		return nil
	}
	var out []string
	for _, rec := range records {
		if l := strings.ToLower(strings.TrimSpace(rec.GetString(GitHubLoginField))); l == want {
			out = append(out, GitHubLoginField+":"+rec.GetString(GitHubLoginField))
			continue
		}
		if l := strings.ToLower(strings.TrimSpace(rec.GetString(GiteaLoginField))); l == want {
			out = append(out, GiteaLoginField+":"+rec.GetString(GiteaLoginField))
		}
	}
	return out
}
