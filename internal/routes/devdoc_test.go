// HTTP-layer tests for the dev-docs ticket gate (/devdoc/*).
//
// What's exercised here:
//
//   - POST /api/admin/devdoc/ticket: anonymous → 401, non-admin session
//     → 403 "admin only", admin session → 200 {url}
//   - GET /devdoc/*: no cookie/ticket → 403; valid ticket → 302 +
//     Set-Cookie; follow-up with the cookie → 200 static content
//   - Token hygiene: garbage cookie → 403; a TICKET presented as a
//     cookie → 403 (purposes are not interchangeable)
//
// The static tree is an in-memory fstest.MapFS standing in for the
// embedded web/dist/devdoc subtree.
package routes

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/auth"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/config"

	"github.com/pocketbase/pocketbase/apis"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tests"
)

// devdocHarness mirrors meHarness/adminHarness: one mux built from
// OnServe with the devdoc routes mounted behind a cfg allowlisting
// adminTestLogin.
type devdocHarness struct {
	*patHarness
	cfg *config.Config
}

func newDevdocHarness(t testing.TB) *devdocHarness {
	t.Helper()
	h := &devdocHarness{
		patHarness: &patHarness{t: t},
		cfg:        &config.Config{AdminGitHubLogins: []string{adminTestLogin}},
	}

	app, err := tests.NewTestApp()
	if err != nil {
		t.Fatalf("NewTestApp: %v", err)
	}
	t.Cleanup(app.Cleanup)
	h.app = app

	dist := fstest.MapFS{
		"devdoc/dev/index.html": &fstest.MapFile{Data: []byte("<h1>dev docs marker</h1>")},
		"devdoc/_static/x.css":  &fstest.MapFile{Data: []byte("body{}")},
	}

	baseRouter, err := apis.NewRouter(app)
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	se := new(core.ServeEvent)
	se.App = app
	se.Router = baseRouter

	var built http.Handler
	err = app.OnServe().Trigger(se, func(e *core.ServeEvent) error {
		RegisterDevdoc(e, h.cfg, dist)
		m, mErr := e.Router.BuildMux()
		if mErr != nil {
			return mErr
		}
		built = m
		return nil
	})
	if err != nil {
		t.Fatalf("OnServe trigger: %v", err)
	}
	if built == nil {
		t.Fatal("mux not built by OnServe trigger")
	}
	h.mux = built
	return h
}

// adminToken creates an admin-allowlisted user (github_login stamped,
// exactly the state the OAuth hook produces) and returns its session
// token. Same construction as adminHarness.adminSessionToken.
func (h *devdocHarness) adminToken() string {
	h.t.Helper()
	col, err := h.app.FindCollectionByNameOrId(auth.UsersCollection)
	if err != nil {
		h.t.Fatalf("find users collection: %v", err)
	}
	rec := core.NewRecord(col)
	rec.SetEmail("devdoc-admin@example.com")
	rec.SetPassword("devdoc-admin-password")
	rec.Set(auth.GitHubLoginField, adminTestLogin)
	if err := h.app.Save(rec); err != nil {
		h.t.Fatalf("save admin user: %v", err)
	}
	token, err := rec.NewAuthToken()
	if err != nil {
		h.t.Fatalf("NewAuthToken: %v", err)
	}
	return token
}

// doRaw issues one request without decoding the body, so tests can
// inspect status, headers (Set-Cookie, Location) and raw content.
func (h *devdocHarness) doRaw(req *http.Request) *httptest.ResponseRecorder {
	h.t.Helper()
	rec := httptest.NewRecorder()
	h.mux.ServeHTTP(rec, req)
	return rec
}

func TestAPI_Devdoc_TicketGate(t *testing.T) {
	h := newDevdocHarness(t)

	// Anonymous → 401.
	req := httptest.NewRequest(http.MethodPost, "/api/admin/devdoc/ticket", nil)
	if rec := h.doRaw(req); rec.Code != http.StatusUnauthorized {
		t.Errorf("anonymous: status = %d, want 401", rec.Code)
	}

	// Non-admin session → 403.
	req = httptest.NewRequest(http.MethodPost, "/api/admin/devdoc/ticket", nil)
	req.Header.Set("Authorization", h.sessionToken())
	if rec := h.doRaw(req); rec.Code != http.StatusForbidden {
		t.Errorf("non-admin: status = %d, want 403", rec.Code)
	}

	// Admin session → 200 {url}.
	req = httptest.NewRequest(http.MethodPost, "/api/admin/devdoc/ticket", nil)
	req.Header.Set("Authorization", h.adminToken())
	rec := h.doRaw(req)
	if rec.Code != http.StatusOK {
		t.Fatalf("admin: status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "/devdoc/?ticket=") {
		t.Errorf("response missing signed url: %s", rec.Body.String())
	}
}

func TestAPI_Devdoc_StaticForbiddenWithoutAuth(t *testing.T) {
	h := newDevdocHarness(t)
	for _, url := range []string{"/devdoc/", "/devdoc/dev/index.html", "/devdoc/_static/x.css"} {
		req := httptest.NewRequest(http.MethodGet, url, nil)
		if rec := h.doRaw(req); rec.Code != http.StatusForbidden {
			t.Errorf("GET %s: status = %d, want 403", url, rec.Code)
		}
	}
}

// TestAPI_Devdoc_TicketCookieFlow walks the whole gate: mint a ticket as
// admin, open the signed URL (expect 302 + Set-Cookie), then fetch the
// docs with the cookie alone.
func TestAPI_Devdoc_TicketCookieFlow(t *testing.T) {
	h := newDevdocHarness(t)

	// 1. Mint the ticket URL as admin.
	req := httptest.NewRequest(http.MethodPost, "/api/admin/devdoc/ticket", nil)
	req.Header.Set("Authorization", h.adminToken())
	rec := h.doRaw(req)
	if rec.Code != http.StatusOK {
		t.Fatalf("ticket: status = %d; body=%s", rec.Code, rec.Body.String())
	}
	url := strings.Trim(strings.Split(rec.Body.String(), `"`)[3], " ")
	if !strings.HasPrefix(url, "/devdoc/?ticket=") {
		t.Fatalf("unexpected url shape: %q (body %s)", url, rec.Body.String())
	}

	// 2. Open the signed URL: 302 to the ticket-free URL + Set-Cookie.
	req = httptest.NewRequest(http.MethodGet, url, nil)
	rec = h.doRaw(req)
	if rec.Code != http.StatusFound {
		t.Fatalf("signed open: status = %d, want 302; body=%s", rec.Code, rec.Body.String())
	}
	if loc := rec.Header().Get("Location"); strings.Contains(loc, "ticket=") {
		t.Errorf("redirect still carries the ticket: %q", loc)
	}
	cookies := rec.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != devdocCookieName {
		t.Fatalf("expected one %s cookie, got %v", devdocCookieName, cookies)
	}
	if !cookies[0].HttpOnly {
		t.Error("devdoc cookie must be HttpOnly")
	}

	// 3. Fetch the docs with just the cookie.
	req = httptest.NewRequest(http.MethodGet, "/devdoc/dev/index.html", nil)
	req.AddCookie(cookies[0])
	rec = h.doRaw(req)
	if rec.Code != http.StatusOK {
		t.Fatalf("cookie fetch: status = %d, want 200", rec.Code)
	}
	body, _ := io.ReadAll(rec.Result().Body)
	if !strings.Contains(string(body), "dev docs marker") {
		t.Errorf("cookie fetch returned wrong content: %s", body)
	}

	// 4. The site root redirects to the sphinx master page.
	req = httptest.NewRequest(http.MethodGet, "/devdoc/", nil)
	req.AddCookie(cookies[0])
	rec = h.doRaw(req)
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/devdoc/dev/" {
		t.Errorf("root: status = %d location = %q, want 302 → /devdoc/dev/",
			rec.Code, rec.Header().Get("Location"))
	}
}

// TestAPI_Devdoc_RejectsBadCredentials: a garbage cookie and a TICKET
// reused as a cookie (purpose mismatch) are both rejected.
func TestAPI_Devdoc_RejectsBadCredentials(t *testing.T) {
	h := newDevdocHarness(t)

	// Garbage cookie.
	req := httptest.NewRequest(http.MethodGet, "/devdoc/dev/index.html", nil)
	req.AddCookie(&http.Cookie{Name: devdocCookieName, Value: "cookie|9999999999.deadbeef"})
	if rec := h.doRaw(req); rec.Code != http.StatusForbidden {
		t.Errorf("garbage cookie: status = %d, want 403", rec.Code)
	}

	// A real ticket minted by the server must NOT work as a cookie —
	// the gate signs purpose-specific tokens.
	req = httptest.NewRequest(http.MethodPost, "/api/admin/devdoc/ticket", nil)
	req.Header.Set("Authorization", h.adminToken())
	rec := h.doRaw(req)
	url := strings.Trim(strings.Split(rec.Body.String(), `"`)[3], " ")
	ticket := strings.TrimPrefix(url, "/devdoc/?ticket=")

	req = httptest.NewRequest(http.MethodGet, "/devdoc/dev/index.html", nil)
	req.AddCookie(&http.Cookie{Name: devdocCookieName, Value: ticket})
	if rec := h.doRaw(req); rec.Code != http.StatusForbidden {
		t.Errorf("ticket-as-cookie: status = %d, want 403 (purpose mismatch)", rec.Code)
	}
}
