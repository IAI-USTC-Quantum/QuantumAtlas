package routes

import (
	"encoding/json"
	"fmt"
	"html"
	"io"
	"mime"
	"net/http"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/config"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/objstore"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/shares"

	"github.com/casbin/casbin/v2"
	"github.com/pocketbase/pocketbase/core"
)

// presignTTL is the lifetime of presigned GET URLs handed out for
// share/{token}/{path} downloads. Kept short so a leaked URL ages out
// quickly; the inner share-token TTL is the user-facing lifetime.
const presignTTL = 5 * time.Minute

// RegisterShares wires the /api/shares CRUD plus the public
// /share/{token}* serving routes.
//
// rawStore is the abstracted asset backend (LocalStore or S3Store) —
// the public download handlers ask it to either presign a direct URL
// (S3) or stream bytes back themselves (local).
func RegisterShares(
	se *core.ServeEvent,
	cfg *config.Config,
	store *shares.Store,
	rawStore objstore.Store,
	enforcer *casbin.Enforcer,
) {
	// --- /api/shares CRUD --------------------------------------------------

	se.Router.POST("/api/shares/", scopeGuard(enforcer, "shares", "write", func(re *core.RequestEvent) error {
		var body struct {
			Paths     []string `json:"paths"`
			Label     string   `json:"label"`
			ExpiresIn int      `json:"expires_in"`
		}
		raw, err := io.ReadAll(re.Request.Body)
		if err != nil {
			return re.JSON(http.StatusBadRequest, map[string]string{"detail": "read body: " + err.Error()})
		}
		if err := json.Unmarshal(raw, &body); err != nil {
			return re.JSON(http.StatusBadRequest, map[string]string{"detail": "parse body: " + err.Error()})
		}
		if len(body.Paths) == 0 {
			return re.JSON(http.StatusBadRequest, map[string]string{"detail": "paths required"})
		}
		created := ""
		if cfg.UserHeader != "" {
			created = re.Request.Header.Get(cfg.UserHeader)
		}
		rec, err := shares.CreateRecord(store, cfg, shares.CreateOptions{
			Paths:     body.Paths,
			Label:     body.Label,
			ExpiresIn: body.ExpiresIn,
			CreatedBy: created,
		}, rawStore)
		if err != nil {
			return re.JSON(http.StatusBadRequest, map[string]string{"detail": err.Error()})
		}
		return re.JSON(http.StatusOK, map[string]any{
			"token":      rec.Token,
			"url_prefix": shares.BuildURL(rec.Token, "", ""),
			"paths":      rec.Paths,
			"created_at": rec.CreatedAt,
			"expires_at": rec.ExpiresAt,
			"label":      rec.Label,
		})
	}))

	se.Router.GET("/api/shares/", scopeGuard(enforcer, "shares", "read", func(re *core.RequestEvent) error {
		records, err := store.ListAll()
		if err != nil {
			return re.JSON(http.StatusInternalServerError, map[string]string{"detail": err.Error()})
		}
		return re.JSON(http.StatusOK, map[string]any{"shares": records})
	}))

	se.Router.DELETE("/api/shares/{token}", scopeGuard(enforcer, "shares", "write", func(re *core.RequestEvent) error {
		token := re.Request.PathValue("token")
		ok, err := store.Delete(token)
		if err != nil {
			return re.JSON(http.StatusInternalServerError, map[string]string{"detail": err.Error()})
		}
		if !ok {
			return re.JSON(http.StatusNotFound, map[string]string{"detail": "share not found"})
		}
		return re.JSON(http.StatusOK, map[string]bool{"ok": true})
	}))

	// --- Public /share/{token}{,/{path...}} -------------------------------

	se.Router.GET("/share/{token}", func(re *core.RequestEvent) error {
		return publicShareEntry(re, cfg, store, rawStore)
	})

	// Catch-all handles both the trailing-slash index ("path" empty) and
	// any nested subpath.
	se.Router.GET("/share/{token}/{path...}", func(re *core.RequestEvent) error {
		if strings.Trim(re.Request.PathValue("path"), "/") == "" {
			return publicShareIndex(re, cfg, store, rawStore)
		}
		return publicShareFile(re, cfg, store, rawStore)
	})
}

// shareOr410 fetches the share record (permanent or persisted) and
// returns the appropriate 404 / 410 response when missing or expired.
func shareOr410(re *core.RequestEvent, cfg *config.Config, store *shares.Store, token string) (*shares.Record, error) {
	if perm := shares.PermanentRecord(cfg); perm != nil && perm.Token == token {
		return perm, nil
	}
	rec, err := store.Get(token)
	if err != nil {
		_ = re.JSON(http.StatusInternalServerError, map[string]string{"detail": err.Error()})
		return nil, err
	}
	if rec == nil {
		_ = re.JSON(http.StatusNotFound, map[string]string{"detail": "share not found"})
		return nil, fmt.Errorf("not found")
	}
	if rec.IsExpired() {
		_ = re.JSON(http.StatusGone, map[string]string{"detail": "该分享链接已过期"})
		return nil, fmt.Errorf("expired")
	}
	return rec, nil
}

func publicShareEntry(re *core.RequestEvent, cfg *config.Config, store *shares.Store, rawStore objstore.Store) error {
	token := re.Request.PathValue("token")
	rec, err := shareOr410(re, cfg, store, token)
	if err != nil {
		return nil
	}
	if len(rec.Paths) == 1 {
		if served := serveSharedKey(re, rawStore, rec.Paths[0]); served {
			return nil
		}
	}
	http.Redirect(re.Response, re.Request, shares.URLPrefix+"/"+token+"/", http.StatusTemporaryRedirect)
	return nil
}

func publicShareIndex(re *core.RequestEvent, cfg *config.Config, store *shares.Store, rawStore objstore.Store) error {
	ctx := re.Request.Context()
	token := re.Request.PathValue("token")
	rec, err := shareOr410(re, cfg, store, token)
	if err != nil {
		return nil
	}
	var items []string
	for _, rel := range rec.Paths {
		key, err := shares.ResolveKey(rel)
		if err != nil {
			items = append(items, fmt.Sprintf("<li>%s (invalid)</li>", html.EscapeString(rel)))
			continue
		}
		if info, exists, _ := rawStore.Stat(ctx, key); exists {
			items = append(items, fmt.Sprintf(`<li><a href="%s">%s</a> (%d bytes)</li>`,
				html.EscapeString(rel), html.EscapeString(rel), info.Size))
			continue
		}
		if children, _ := rawStore.ListPrefix(ctx, key+"/", 1); len(children) > 0 {
			items = append(items, fmt.Sprintf(`<li><a href="%s/">%s/</a> (directory)</li>`,
				html.EscapeString(rel), html.EscapeString(rel)))
			continue
		}
		items = append(items, fmt.Sprintf("<li>%s (missing)</li>", html.EscapeString(rel)))
	}
	return writeHTML(re, "share", "<h1>Share</h1><ul>"+strings.Join(items, "")+"</ul>")
}

func publicShareFile(re *core.RequestEvent, cfg *config.Config, store *shares.Store, rawStore objstore.Store) error {
	ctx := re.Request.Context()
	token := re.Request.PathValue("token")
	rel := strings.Trim(re.Request.PathValue("path"), "/")
	rec, err := shareOr410(re, cfg, store, token)
	if err != nil {
		return nil
	}
	if !shares.IsUnderShare(rel, rec.Paths) {
		return re.JSON(http.StatusForbidden, map[string]string{"detail": "not allowed for this share"})
	}
	key, err := shares.ResolveKey(rel)
	if err != nil {
		return re.JSON(http.StatusForbidden, map[string]string{"detail": err.Error()})
	}

	if served := serveSharedKey(re, rawStore, rel); served {
		return nil
	}

	// Fall back to directory listing when the path isn't a single
	// object but covers ≥1 nested object — S3 doesn't have real
	// directories, ListPrefix is the analogue of os.ReadDir.
	children, listErr := rawStore.ListPrefix(ctx, key+"/", 0)
	if listErr != nil {
		return re.JSON(http.StatusInternalServerError, map[string]string{"detail": listErr.Error()})
	}
	if len(children) == 0 {
		return re.JSON(http.StatusNotFound, map[string]string{"detail": "not found"})
	}
	return renderDirectoryListing(re, rec, rel, key, children)
}

// serveSharedKey tries to serve a single object at the share-relative
// path `rel`. Returns true when the response was fully written (either
// presigned redirect or streamed body), false when the underlying
// object doesn't exist and the caller should fall back to directory
// listing.
//
// All asset kinds — PDF, markdown, images — serve their real stored bytes.
// Share links are PRIVATE: they are minted for MinerU's crawler and for
// internal team sharing, never as a public redistribution surface (the
// short presign TTL and per-token share lifetime bound exposure). The
// "jump to the canonical arxiv.org page" affordance is a frontend/client
// concern (the paper API exposes the arxiv URL as data) — the server does
// NOT rewrite share/raw paths into arxiv redirects.
//
// Dispatch order:
//  1. Stat — confirm the object exists. Presign is happy to sign URLs
//     for absent keys (S3 only validates on GET) so a pre-flight Stat
//     prevents 404-via-redirect surprises.
//  2. PresignGet — when the backend supports it (S3), 307 to a direct
//     presigned URL. No bytes flow through us, saves WAN egress.
//  3. Get — stream the body. Used by LocalStore and as a fallback
//     for any S3 PresignGet failure.
func serveSharedKey(re *core.RequestEvent, store objstore.Store, rel string) bool {
	ctx := re.Request.Context()
	key, err := shares.ResolveKey(rel)
	if err != nil {
		return false
	}
	info, exists, statErr := store.Stat(ctx, key)
	if statErr != nil || !exists {
		return false
	}
	if url, supported, err := store.PresignGet(ctx, key, presignTTL); err == nil && supported && url != "" {
		http.Redirect(re.Response, re.Request, url, http.StatusTemporaryRedirect)
		return true
	}
	rc, _, err := store.Get(ctx, key)
	if err != nil {
		return false
	}
	defer rc.Close()
	if ct := guessContentType(key); ct != "" {
		re.Response.Header().Set("Content-Type", ct)
	}
	if info.Size > 0 {
		re.Response.Header().Set("Content-Length", strconv.FormatInt(info.Size, 10))
	}
	_, _ = io.Copy(re.Response, rc)
	return true
}

// guessContentType returns a best-effort Content-Type for the given key
// based on its extension. Falls back to "" when unknown.
func guessContentType(key string) string {
	ext := path.Ext(key)
	if ext == "" {
		return ""
	}
	if ct := mime.TypeByExtension(ext); ct != "" {
		return ct
	}
	// Cover the handful of types we serve regularly so the response
	// looks right even on minimal containers without /etc/mime.types.
	switch strings.ToLower(ext) {
	case ".pdf":
		return "application/pdf"
	case ".md", ".markdown":
		return "text/markdown; charset=utf-8"
	case ".json":
		return "application/json"
	}
	return ""
}

func renderDirectoryListing(re *core.RequestEvent, rec *shares.Record, rel, prefix string, children []objstore.ObjectInfo) error {
	sort.SliceStable(children, func(i, j int) bool {
		return strings.ToLower(children[i].Key) < strings.ToLower(children[j].Key)
	})

	var lis []string
	seenNames := map[string]struct{}{}
	for _, c := range children {
		tail := strings.TrimPrefix(c.Key, prefix+"/")
		if tail == "" {
			continue
		}
		topLevel := tail
		isDir := false
		if i := strings.Index(tail, "/"); i >= 0 {
			topLevel = tail[:i]
			isDir = true
		}
		if _, dup := seenNames[topLevel]; dup {
			continue
		}
		seenNames[topLevel] = struct{}{}

		childRel := topLevel
		if rel != "" {
			childRel = rel + "/" + topLevel
		}
		if !shares.IsUnderShare(childRel, rec.Paths) {
			continue
		}
		if isDir {
			lis = append(lis, fmt.Sprintf(`<li><a href="%s/">%s/</a></li>`,
				html.EscapeString(topLevel), html.EscapeString(topLevel)))
		} else {
			lis = append(lis, fmt.Sprintf(`<li><a href="%s">%s</a> (%d bytes)</li>`,
				html.EscapeString(topLevel), html.EscapeString(topLevel), c.Size))
		}
	}

	title := rel
	if title == "" {
		title = "."
	}
	return writeHTML(re, title,
		"<h1>"+html.EscapeString(title)+"</h1><ul>"+strings.Join(lis, "")+"</ul>")
}

func writeHTML(re *core.RequestEvent, title, body string) error {
	re.Response.Header().Set("Content-Type", "text/html; charset=utf-8")
	page := `<!DOCTYPE html><html><head><meta charset="utf-8"/><title>` +
		html.EscapeString(title) + `</title></head><body>` + body + `</body></html>`
	_, err := re.Response.Write([]byte(page))
	return err
}
