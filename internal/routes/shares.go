package routes

import (
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/config"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/shares"

	"github.com/casbin/casbin/v2"
	"github.com/pocketbase/pocketbase/core"
)

// RegisterShares wires the /api/shares CRUD plus the public
// /share/{token}* serving routes. Both groups share the same Store so
// the public route can validate / resolve tokens against the same
// on-disk records the admin API mutates. enforcer is the process-wide
// casbin enforcer used to gate write endpoints by PAT scope;
// shares:read covers list, shares:write covers POST/DELETE (and
// implies read).
func RegisterShares(se *core.ServeEvent, cfg *config.Config, store *shares.Store, enforcer *casbin.Enforcer) {
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
		})
		if err != nil {
			return re.JSON(http.StatusBadRequest, map[string]string{"detail": err.Error()})
		}
		return re.JSON(http.StatusOK, map[string]any{
			"token":       rec.Token,
			"url_prefix":  shares.BuildURL(rec.Token, "", ""),
			"paths":       rec.Paths,
			"created_at":  rec.CreatedAt,
			"expires_at":  rec.ExpiresAt,
			"label":       rec.Label,
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
		return publicShareEntry(re, cfg, store)
	})

	// Catch-all handles both the trailing-slash index ("path" empty) and
	// any nested subpath. Go's net/http mux treats /share/{token}/ and
	// /share/{token}/{path...} as overlapping, so we collapse them.
	se.Router.GET("/share/{token}/{path...}", func(re *core.RequestEvent) error {
		if strings.Trim(re.Request.PathValue("path"), "/") == "" {
			return publicShareIndex(re, cfg, store)
		}
		return publicShareFile(re, cfg, store)
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

func publicShareEntry(re *core.RequestEvent, cfg *config.Config, store *shares.Store) error {
	token := re.Request.PathValue("token")
	rec, err := shareOr410(re, cfg, store, token)
	if err != nil {
		return nil // shareOr410 wrote the response
	}
	// Single-file share: stream the file directly.
	if len(rec.Paths) == 1 {
		fs, err := shares.ResolveTarget(cfg, rec.Paths[0])
		if err == nil {
			if info, err := os.Stat(fs); err == nil && info.Mode().IsRegular() {
				http.ServeFile(re.Response, re.Request, fs)
				return nil
			}
		}
	}
	// Otherwise redirect to the trailing-slash index so relative links
	// resolve under /share/{token}/.
	http.Redirect(re.Response, re.Request, shares.URLPrefix+"/"+token+"/", http.StatusTemporaryRedirect)
	return nil
}

func publicShareIndex(re *core.RequestEvent, cfg *config.Config, store *shares.Store) error {
	token := re.Request.PathValue("token")
	rec, err := shareOr410(re, cfg, store, token)
	if err != nil {
		return nil
	}
	var items []string
	for _, rel := range rec.Paths {
		fs, err := shares.ResolveTarget(cfg, rel)
		if err != nil {
			items = append(items, fmt.Sprintf("<li>%s (invalid)</li>", html.EscapeString(rel)))
			continue
		}
		info, statErr := os.Stat(fs)
		switch {
		case statErr != nil:
			items = append(items, fmt.Sprintf("<li>%s (missing)</li>", html.EscapeString(rel)))
		case info.IsDir():
			items = append(items, fmt.Sprintf(`<li><a href="%s/">%s/</a> (directory)</li>`,
				html.EscapeString(rel), html.EscapeString(rel)))
		default:
			items = append(items, fmt.Sprintf(`<li><a href="%s">%s</a> (%d bytes)</li>`,
				html.EscapeString(rel), html.EscapeString(rel), info.Size()))
		}
	}
	return writeHTML(re, "share", "<h1>Share</h1><ul>"+strings.Join(items, "")+"</ul>")
}

func publicShareFile(re *core.RequestEvent, cfg *config.Config, store *shares.Store) error {
	token := re.Request.PathValue("token")
	rel := strings.Trim(re.Request.PathValue("path"), "/")
	rec, err := shareOr410(re, cfg, store, token)
	if err != nil {
		return nil
	}
	if !shares.IsUnderShare(rel, rec.Paths) {
		return re.JSON(http.StatusForbidden, map[string]string{"detail": "not allowed for this share"})
	}
	fs, err := shares.ResolveTarget(cfg, rel)
	if err != nil {
		return re.JSON(http.StatusForbidden, map[string]string{"detail": err.Error()})
	}
	info, statErr := os.Stat(fs)
	if statErr != nil {
		return re.JSON(http.StatusNotFound, map[string]string{"detail": "not found"})
	}
	if info.Mode().IsRegular() {
		http.ServeFile(re.Response, re.Request, fs)
		return nil
	}
	if info.IsDir() {
		entries, err := os.ReadDir(fs)
		if err != nil {
			return re.JSON(http.StatusInternalServerError, map[string]string{"detail": err.Error()})
		}
		sort.SliceStable(entries, func(i, j int) bool {
			return strings.ToLower(entries[i].Name()) < strings.ToLower(entries[j].Name())
		})
		var lis []string
		for _, e := range entries {
			name := e.Name()
			childRel := name
			if rel != "" {
				childRel = rel + "/" + name
			}
			if !shares.IsUnderShare(childRel, rec.Paths) {
				continue
			}
			if e.IsDir() {
				lis = append(lis, fmt.Sprintf(`<li><a href="%s/">%s/</a></li>`,
					html.EscapeString(name), html.EscapeString(name)))
			} else {
				if info, err := os.Stat(filepath.Join(fs, name)); err == nil {
					lis = append(lis, fmt.Sprintf(`<li><a href="%s">%s</a> (%d bytes)</li>`,
						html.EscapeString(name), html.EscapeString(name), info.Size()))
				}
			}
		}
		title := rel
		if title == "" {
			title = "."
		}
		return writeHTML(re, title,
			"<h1>"+html.EscapeString(title)+"</h1><ul>"+strings.Join(lis, "")+"</ul>")
	}
	return re.JSON(http.StatusNotFound, map[string]string{"detail": "not found"})
}

func writeHTML(re *core.RequestEvent, title, body string) error {
	re.Response.Header().Set("Content-Type", "text/html; charset=utf-8")
	page := `<!DOCTYPE html><html><head><meta charset="utf-8"/><title>` +
		html.EscapeString(title) + `</title></head><body>` + body + `</body></html>`
	_, err := re.Response.Write([]byte(page))
	return err
}
