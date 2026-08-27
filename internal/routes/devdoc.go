// Dev-docs hosting (/devdoc/*) — the sphinx-built developer site served
// behind an admin gate.
//
// The dev docs (docsite/dev) are static HTML, but unlike the public /doc
// site they are admin-only. The browser navigates to /devdoc with a plain
// page load, which cannot carry the PocketBase bearer token the rest of
// the admin API uses, so the gate is a two-step ticket → cookie flow:
//
//  1. POST /api/admin/devdoc/ticket — adminGuard (session + GitHub admin
//     allowlist, same as every other admin surface). Returns a short-
//     lived signed URL: /devdoc/?ticket=<hmac>.
//  2. GET /devdoc/* — the gate first looks for a valid auth cookie; on a
//     valid ticket it instead SETS the cookie (HttpOnly, 12h) and 302s to
//     the ticket-free URL, so every subsequent page / asset request is
//     authorized by the cookie. Anything else gets 403.
//
// Tickets and cookies are HMAC-SHA256 signed with a random in-memory key
// generated at boot: a restart simply invalidates outstanding tickets and
// cookies (admins re-open the docs from the SPA), no secret management
// needed.
package routes

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io/fs"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/config"

	"github.com/pocketbase/pocketbase/core"
)

const (
	// devdocCookieName is the HttpOnly cookie the ticket exchange sets.
	devdocCookieName = "qatlas_devdoc"
	// devdocTicketTTL bounds the one-shot ticket in the signed URL.
	devdocTicketTTL = 2 * time.Minute
	// devdocCookieTTL bounds the cookie the ticket exchange mints.
	devdocCookieTTL = 12 * time.Hour
)

// devdocGate holds the signing key and the static dev-docs filesystem.
type devdocGate struct {
	key    []byte
	static http.Handler
}

// RegisterDevdoc wires the dev-docs ticket endpoint and the gated static
// host. distFS is the embedded web/dist filesystem; the dev site lives in
// its devdoc/ subtree (web/public/devdoc → dist/devdoc at build time).
func RegisterDevdoc(se *core.ServeEvent, cfg *config.Config, distFS fs.FS) {
	sub, err := fs.Sub(distFS, "devdoc")
	if err != nil {
		// The devdocs build is missing (e.g. a local build without the
		// sphinx step): register nothing rather than crash boot — the SPA
		// entry point surfaces the 404 on use.
		sub = nil
	}
	g := &devdocGate{key: make([]byte, 32)}
	if _, err := rand.Read(g.key); err != nil {
		panic("devdoc: read random key: " + err.Error())
	}
	if sub != nil {
		g.static = http.FileServer(http.FS(sub))
	}

	se.Router.POST("/api/admin/devdoc/ticket", adminGuard(cfg, func(re *core.RequestEvent) error {
		if g.static == nil {
			return re.JSON(http.StatusNotFound, map[string]string{
				"detail": "dev docs not bundled in this build",
			})
		}
		ticket := g.mint("ticket", devdocTicketTTL)
		return re.JSON(http.StatusOK, map[string]string{
			"url": "/devdoc/?ticket=" + ticket,
		})
	}))

	se.Router.GET("/devdoc/{path...}", func(re *core.RequestEvent) error {
		if g.static == nil {
			return re.JSON(http.StatusNotFound, map[string]string{
				"detail": "dev docs not bundled in this build",
			})
		}
		return g.serve(re)
	})
}

// serve implements the cookie-first, ticket-second gate.
func (g *devdocGate) serve(re *core.RequestEvent) error {
	r := re.Request

	if c, err := r.Cookie(devdocCookieName); err == nil && g.valid(c.Value, "cookie") {
		g.serveFile(re)
		return nil
	}

	if ticket := r.URL.Query().Get("ticket"); ticket != "" && g.valid(ticket, "ticket") {
		http.SetCookie(re.Response, &http.Cookie{
			Name:     devdocCookieName,
			Value:    g.mint("cookie", devdocCookieTTL),
			Path:     "/devdoc",
			MaxAge:   int(devdocCookieTTL.Seconds()),
			HttpOnly: true,
			SameSite: http.SameSiteLaxMode,
		})
		// Strip the ticket from the URL before landing on the docs.
		clean := *r.URL
		q := clean.Query()
		q.Del("ticket")
		clean.RawQuery = q.Encode()
		http.Redirect(re.Response, r, clean.String(), http.StatusFound)
		return nil
	}

	return re.JSON(http.StatusForbidden, map[string]string{
		"detail": "admin only",
	})
}

// serveFile maps the wildcard path onto the dev-docs file server. The
// site root redirects to the sphinx master page (the dev build's master
// doc is dev/index, which sphinx writes to dev/index.html). Trailing
// "index.html" is canonicalized to the directory form up front because
// http.FileServer 301s ".../index.html" → "./" anyway — better to land
// there directly.
func (g *devdocGate) serveFile(re *core.RequestEvent) {
	path := re.Request.PathValue("path")
	if path == "" || path == "/" {
		http.Redirect(re.Response, re.Request, "/devdoc/dev/", http.StatusFound)
		return
	}
	path = strings.TrimPrefix(path, "/")
	if strings.HasSuffix(path, "index.html") {
		path = strings.TrimSuffix(path, "index.html")
	}
	// FileServer expects a leading-slash request path within its root.
	re.Request.URL.Path = "/" + path
	g.static.ServeHTTP(re.Response, re.Request)
}

// mint builds "<purpose>|<expiryUnix>.<base64url hmac>".
func (g *devdocGate) mint(purpose string, ttl time.Duration) string {
	expiry := time.Now().Add(ttl).Unix()
	payload := fmt.Sprintf("%s|%d", purpose, expiry)
	mac := hmac.New(sha256.New, g.key)
	mac.Write([]byte(payload))
	return payload + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// valid reports whether token is a well-formed, correctly-signed,
// unexpired token for the given purpose ("ticket" or "cookie"). Purposes
// are not interchangeable — a ticket must never work as a cookie.
func (g *devdocGate) valid(token, purpose string) bool {
	payload, sig, ok := strings.Cut(token, ".")
	if !ok {
		return false
	}
	wantPurpose, expiryStr, ok := strings.Cut(payload, "|")
	if !ok || wantPurpose != purpose {
		return false
	}
	expiry, err := strconv.ParseInt(expiryStr, 10, 64)
	if err != nil || time.Now().Unix() > expiry {
		return false
	}
	mac := hmac.New(sha256.New, g.key)
	mac.Write([]byte(payload))
	want, err := base64.RawURLEncoding.DecodeString(sig)
	if err != nil {
		return false
	}
	return hmac.Equal(want, mac.Sum(nil))
}
