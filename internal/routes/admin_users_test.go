// HTTP-layer tests for the /api/admin/users surface (DB-flag roles).
//
// What's exercised here:
//
//   - userAdminGuard matrix: anonymous → 401; plain session → 403;
//     DB-flag admin (is_admin) → through; PAT auth → 403 (sessionGuard
//     underneath — user management is for humans)
//   - admin powers: list users, toggle availability (disabled), but
//     NOT is_admin (read-only to them), NOT self-disable, NOT
//     disabling a superadmin
//   - superadmin powers: grant/revoke is_admin on others, but never
//     on self
//   - env allowlist admin (adminTestLogin) behaves superadmin-equivalent
//   - availability enforcement at the request layer: a disabled user's
//     session 401s on a sessionGuard route, a disabled user's PAT 401s
//     on authGuard (contrast: an enabled user's PAT gets 403 from
//     sessionGuard — the 401/403 split proves the disabled check fires
//     in isAuthorized, not the handler)
package routes

import (
	"net/http"
	"testing"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/auth"

	"github.com/pocketbase/pocketbase/core"
)

// flaggedUser creates a users record with the given role/availability
// flags stamped directly (bypassing the bootstrap promotion) and
// returns it. Token minting is left to the caller so tests can also
// exercise PAT paths against the same record.
func (h *adminHarness) flaggedUser(email, login string, isAdmin, isSuper, disabled bool) *core.Record {
	h.t.Helper()
	col, err := h.app.FindCollectionByNameOrId(auth.UsersCollection)
	if err != nil {
		h.t.Fatalf("find users collection: %v", err)
	}
	rec := core.NewRecord(col)
	rec.SetEmail(email)
	rec.SetPassword("flagged-test-password")
	rec.Set(auth.GitHubLoginField, login)
	rec.Set(auth.IsAdminField, isAdmin)
	rec.Set(auth.IsSuperadminField, isSuper)
	rec.Set(auth.DisabledField, disabled)
	if err := h.app.Save(rec); err != nil {
		h.t.Fatalf("save flagged user %s: %v", email, err)
	}
	return rec
}

// sessionFor returns a fresh session token for an existing record.
func (h *adminHarness) sessionFor(rec *core.Record) string {
	h.t.Helper()
	token, err := rec.NewAuthToken()
	if err != nil {
		h.t.Fatalf("NewAuthToken for %s: %v", rec.Id, err)
	}
	return token
}

func TestAPI_AdminUsers_RejectsAnonymous(t *testing.T) {
	h := newAdminHarness(t)
	for _, tc := range []struct{ method, url string }{
		{http.MethodGet, "/api/admin/users"},
		{http.MethodPatch, "/api/admin/users/someid"},
	} {
		status, _, _ := h.do(tc.method, tc.url, "", nil)
		if status != http.StatusUnauthorized {
			t.Errorf("%s %s: status = %d, want 401", tc.method, tc.url, status)
		}
	}
}

func TestAPI_AdminUsers_RejectsPlainSession(t *testing.T) {
	h := newAdminHarness(t)
	status, _, body := h.do(http.MethodGet, "/api/admin/users", "", rawHeader(h.sessionToken()))
	if status != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body=%v", status, body)
	}
}

func TestAPI_AdminUsers_AdminListsAllUsers(t *testing.T) {
	h := newAdminHarness(t)
	status, _, body := h.do(http.MethodGet, "/api/admin/users", "", nil)
	if status != http.StatusUnauthorized {
		t.Fatalf("sanity: unauthenticated list = %d, want 401", status)
	}

	dbAdmin := h.flaggedUser("dbadmin2@example.com", "dbadmin2", true, false, false)
	victim := h.flaggedUser("victim@example.com", "victim", false, false, false)
	all, err := h.app.FindAllRecords(auth.UsersCollection)
	if err != nil {
		t.Fatalf("count users: %v", err)
	}

	status, _, body = h.do(http.MethodGet, "/api/admin/users", "", rawHeader(h.sessionFor(dbAdmin)))
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%v", status, body)
	}
	if total := body["total"]; total != float64(len(all)) { // PB test seed + dbadmin2 + victim
		t.Errorf("total = %v, want %d", total, len(all))
	}
	// The victim's entry must carry the full flag vocabulary — is_admin
	// is visible (read-only) to plain admins.
	found := false
	for _, u := range body["users"].([]any) {
		m := u.(map[string]any)
		if m["id"] == victim.Id {
			found = true
			if m["is_admin"] != false || m["is_superadmin"] != false || m["disabled"] != false {
				t.Errorf("victim flags = %v/%v/%v, want false/false/false",
					m["is_admin"], m["is_superadmin"], m["disabled"])
			}
		}
	}
	if !found {
		t.Errorf("victim %s not in list", victim.Id)
	}
}

func TestAPI_AdminUsers_AdminTogglesAvailability(t *testing.T) {
	h := newAdminHarness(t)
	dbAdmin := h.flaggedUser("dbadmin@example.com", "dbadmin", true, false, false)
	victim := h.flaggedUser("victim@example.com", "victim", false, false, false)

	status, _, body := h.do(http.MethodPatch, "/api/admin/users/"+victim.Id,
		`{"disabled":true}`, rawHeader(h.sessionFor(dbAdmin)))
	if status != http.StatusOK {
		t.Fatalf("disable: status = %d, want 200; body=%v", status, body)
	}
	if body["disabled"] != true {
		t.Errorf("response disabled = %v, want true", body["disabled"])
	}
	persisted, err := h.app.FindRecordById(auth.UsersCollection, victim.Id)
	if err != nil {
		t.Fatalf("refetch victim: %v", err)
	}
	if !persisted.GetBool(auth.DisabledField) {
		t.Error("disabled flag not persisted")
	}

	// And back on.
	status, _, _ = h.do(http.MethodPatch, "/api/admin/users/"+victim.Id,
		`{"disabled":false}`, rawHeader(h.sessionFor(dbAdmin)))
	if status != http.StatusOK {
		t.Fatalf("re-enable: status = %d, want 200", status)
	}
}

func TestAPI_AdminUsers_AdminCannotSetIsAdmin(t *testing.T) {
	h := newAdminHarness(t)
	dbAdmin := h.flaggedUser("dbadmin@example.com", "dbadmin", true, false, false)
	victim := h.flaggedUser("victim@example.com", "victim", false, false, false)

	status, _, body := h.do(http.MethodPatch, "/api/admin/users/"+victim.Id,
		`{"is_admin":true}`, rawHeader(h.sessionFor(dbAdmin)))
	if status != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body=%v", status, body)
	}
}

func TestAPI_AdminUsers_AdminCannotDisableSelf(t *testing.T) {
	h := newAdminHarness(t)
	dbAdmin := h.flaggedUser("dbadmin@example.com", "dbadmin", true, false, false)

	status, _, body := h.do(http.MethodPatch, "/api/admin/users/"+dbAdmin.Id,
		`{"disabled":true}`, rawHeader(h.sessionFor(dbAdmin)))
	if status != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body=%v", status, body)
	}
}

func TestAPI_AdminUsers_AdminCannotDisableSuperadmin(t *testing.T) {
	h := newAdminHarness(t)
	dbAdmin := h.flaggedUser("dbadmin@example.com", "dbadmin", true, false, false)
	super := h.flaggedUser("super@example.com", "super", false, true, false)

	status, _, body := h.do(http.MethodPatch, "/api/admin/users/"+super.Id,
		`{"disabled":true}`, rawHeader(h.sessionFor(dbAdmin)))
	if status != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body=%v", status, body)
	}
}

func TestAPI_AdminUsers_SuperadminGrantsAndRevokesIsAdmin(t *testing.T) {
	h := newAdminHarness(t)
	super := h.flaggedUser("super@example.com", "super", false, true, false)
	victim := h.flaggedUser("victim@example.com", "victim", false, false, false)

	status, _, body := h.do(http.MethodPatch, "/api/admin/users/"+victim.Id,
		`{"is_admin":true}`, rawHeader(h.sessionFor(super)))
	if status != http.StatusOK {
		t.Fatalf("grant: status = %d, want 200; body=%v", status, body)
	}
	if body["is_admin"] != true {
		t.Errorf("response is_admin = %v, want true", body["is_admin"])
	}

	status, _, _ = h.do(http.MethodPatch, "/api/admin/users/"+victim.Id,
		`{"is_admin":false}`, rawHeader(h.sessionFor(super)))
	if status != http.StatusOK {
		t.Fatalf("revoke: status = %d, want 200", status)
	}
	persisted, err := h.app.FindRecordById(auth.UsersCollection, victim.Id)
	if err != nil {
		t.Fatalf("refetch victim: %v", err)
	}
	if persisted.GetBool(auth.IsAdminField) {
		t.Error("is_admin still set after revoke")
	}
}

func TestAPI_AdminUsers_SuperadminCannotChangeOwnAdminFlag(t *testing.T) {
	h := newAdminHarness(t)
	super := h.flaggedUser("super@example.com", "super", false, true, false)

	status, _, body := h.do(http.MethodPatch, "/api/admin/users/"+super.Id,
		`{"is_admin":true}`, rawHeader(h.sessionFor(super)))
	if status != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body=%v", status, body)
	}
}

func TestAPI_AdminUsers_EnvAdminIsSuperadminEquivalent(t *testing.T) {
	h := newAdminHarness(t)
	victim := h.flaggedUser("victim@example.com", "victim", false, false, false)

	// adminSessionToken = env-allowlisted adminTestLogin — must be able
	// to grant is_admin even though it holds no DB flags.
	status, _, body := h.do(http.MethodPatch, "/api/admin/users/"+victim.Id,
		`{"is_admin":true}`, rawHeader(h.adminSessionToken()))
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%v", status, body)
	}
}

func TestAPI_AdminUsers_PATRejected(t *testing.T) {
	h := newAdminHarness(t)
	dbAdmin := h.flaggedUser("dbadmin@example.com", "dbadmin", true, false, false)
	pat := h.mintPATForAuth(h.sessionFor(dbAdmin))

	status, _, body := h.do(http.MethodGet, "/api/admin/users", "", bearerHeader(pat))
	if status != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (sessionGuard refuses PAT); body=%v", status, body)
	}
}

func TestAPI_AdminUsers_BadRequests(t *testing.T) {
	h := newAdminHarness(t)
	super := h.flaggedUser("super@example.com", "super", false, true, false)
	hdr := rawHeader(h.sessionFor(super))

	for _, tc := range []struct{ name, body string }{
		{"empty object", `{}`},
		{"invalid json", `{not json`},
	} {
		status, _, _ := h.do(http.MethodPatch, "/api/admin/users/someid", tc.body, hdr)
		if status != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", tc.name, status)
		}
	}

	status, _, _ := h.do(http.MethodPatch, "/api/admin/users/nosuchuser00", `{"disabled":true}`, hdr)
	if status != http.StatusNotFound {
		t.Errorf("unknown id: status = %d, want 404", status)
	}
}

// ---------------------------------------------------------------------------
// Availability enforcement at the request layer
// ---------------------------------------------------------------------------

func TestAPI_DisabledUser_SessionRejected(t *testing.T) {
	h := newAdminHarness(t)
	disabled := h.flaggedUser("gone@example.com", "gone", false, false, true)

	// whoami is sessionGuard-gated (authGuard → sessionGuard). A valid
	// JWT for a disabled record must die at authGuard with 401 — not
	// pass through to 200.
	status, _, body := h.do(http.MethodGet, "/api/admin/whoami", "", rawHeader(h.sessionFor(disabled)))
	if status != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401; body=%v", status, body)
	}
}

func TestAPI_DisabledUser_PATRejected(t *testing.T) {
	h := newAdminHarness(t)
	disabled := h.flaggedUser("gone@example.com", "gone", false, false, true)
	enabled := h.flaggedUser("alive@example.com", "alive", true, false, false)

	// Disabled owner: isAuthorized's PAT path fails → 401 from authGuard.
	status, _, body := h.do(http.MethodGet, "/api/admin/users", "", bearerHeader(h.mintPATForAuth(h.sessionFor(disabled))))
	if status != http.StatusUnauthorized {
		t.Fatalf("disabled PAT: status = %d, want 401; body=%v", status, body)
	}

	// Enabled owner control: authGuard passes, sessionGuard rejects the
	// PAT with 403. The 401-vs-403 split proves the disabled check is
	// what fired above, not the sessionGuard.
	status, _, _ = h.do(http.MethodGet, "/api/admin/users", "", bearerHeader(h.mintPATForAuth(h.sessionFor(enabled))))
	if status != http.StatusForbidden {
		t.Fatalf("enabled PAT control: status = %d, want 403", status)
	}
}
