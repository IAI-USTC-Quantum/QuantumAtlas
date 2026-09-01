// Admin console API.
//
// The admin concept is a provider-login allowlist: the GitHub lists
// (Config.AdminGitHubLogins, auth.admin_logins) matched against the
// github_login stamped on the users record, or — since Gitea login was
// added — the Gitea lists (Config.AdminGiteaLogins, auth.gitea_admin_logins)
// matched against gitea_login. A caller is an admin iff they hold an
// authenticated PocketBase SESSION (admins are humans — PATs, both user
// and system, are rejected via sessionGuard semantics, same as /api/pat)
// AND one of those matches (case-insensitive, see Config.IsGitHubAdmin /
// Config.IsGiteaAdmin).
//
// Two endpoints:
//
//	GET /api/admin/whoami     — sessionGuard; any signed-in user gets
//	                            {login, is_admin}. The SPA uses this to
//	                            decide whether to render the admin nav;
//	                            non-admins get is_admin:false, not a 403.
//	GET /api/admin/db/schema  — adminGuard; read-only introspection of
//	                            the Postgres paper-registry database
//	                            (QATLAS_POSTGRES_DSN) via information_schema
//	                            + pg_catalog. 503 when the registry is
//	                            unavailable (same convention as /api/papers).
//	GET /api/admin/db/tables/{name}/rows — adminGuard; paginated raw-row
//	                                     browser over a registry table
//	                                     (admin_dbrows.go).
//	GET /api/admin/plugins              — adminGuard; plugin summary list
//	                                      (mirrors /api/v1/plugins).
//	GET /api/admin/plugins/{id}/manifest, PUT /api/admin/plugins/{id}/config
//	                                    — adminGuard; proxy to the
//	                                      qatlas-search microservice admin
//	                                      surface (admin_plugins.go).
//	POST /api/admin/mineru/run    — adminGuard; trigger a MinerU batch
//	                                conversion run immediately (see
//	                                admin_mineru.go).
//	GET  /api/admin/mineru/status — adminGuard; scheduler snapshot.
//	GET  /api/admin/acquisition/failures — adminGuard; persisted PDF
//	                                         acquisition failures/logs.
//
// The DB-flag user-management surface (list users, toggle availability
// / is_admin — backed by the users-record role flags, NOT the env
// allowlist) lives in admin_users.go.
//
// The agentic-search metering surface (usage / plans / quotas) lives in
// admin_usage.go.
package routes

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/auth"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/config"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/mineru"
	qplugin "github.com/IAI-USTC-Quantum/QuantumAtlas/internal/plugin"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/search"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/usage"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pocketbase/pocketbase/core"
)

// adminGuard layers the admin allowlist check on top of sessionGuard:
// the caller must be session-authenticated (PATs rejected, because admin
// surfaces are for humans — a leaked PAT must not reach them) AND their
// users-record github_login / gitea_login must appear in the matching
// config admin allowlist (either provider grants admin; the lists are
// checked against their own login field so a GitHub entry never matches
// a Gitea account of the same name, and vice versa).
// 403 {"detail":"admin only"} otherwise.
func adminGuard(cfg *config.Config, handler func(re *core.RequestEvent) error) func(re *core.RequestEvent) error {
	return sessionGuard(func(re *core.RequestEvent) error {
		if !isAdminCaller(re, cfg) {
			return re.JSON(http.StatusForbidden, map[string]string{
				"detail": "admin only",
			})
		}
		return handler(re)
	})
}

// isAdminCaller reports whether the session-authenticated caller is on
// either provider's admin allowlist. sessionGuard has already run, so
// re.Auth is a live users record (nil-tolerant for unit tests).
func isAdminCaller(re *core.RequestEvent, cfg *config.Config) bool {
	if re.Auth == nil {
		return false
	}
	return cfg.IsGitHubAdmin(re.Auth.GetString(auth.GitHubLoginField)) ||
		cfg.IsGiteaAdmin(re.Auth.GetString(auth.GiteaLoginField))
}

// RegisterAdmin wires the /api/admin/* surface. pool is the Postgres
// paper-registry pool (registry.Store.Pool()) — may be nil when
// QATLAS_POSTGRES_DSN is unset, in which case the db/schema handler
// reports 503 while whoami keeps working. sched is the MinerU batch
// scheduler — may be nil when paper access is disabled, in which case
// the mineru endpoints report 503. app resolves users-collection logins
// for the usage view; usageStore backs the metering endpoints
// (admin_usage.go) and reports 503 when its pool is nil.
// pluginRegistry and remote back the plugin management surface
// (admin_plugins.go); nil values degrade to an empty plugin list and
// 503 proxy endpoints respectively.
func RegisterAdmin(se *core.ServeEvent, cfg *config.Config, app core.App, pool *pgxpool.Pool, sched *mineru.Scheduler, usageStore *usage.Store, pluginRegistry *qplugin.Registry, remote *search.RemoteProvider) {
	se.Router.GET("/api/admin/whoami", sessionGuard(adminWhoamiHandler(cfg)))
	se.Router.GET("/api/admin/db/schema", adminGuard(cfg, adminDBSchemaHandler(pool)))
	se.Router.GET("/api/admin/db/tables/{name}/rows", adminGuard(cfg, adminDBRowsHandler(pool)))
	se.Router.POST("/api/admin/mineru/run", adminGuard(cfg, adminMineruRunHandler(sched)))
	se.Router.GET("/api/admin/mineru/status", adminGuard(cfg, adminMineruStatusHandler(sched)))
	se.Router.GET("/api/admin/acquisition/failures", adminGuard(cfg, adminAcquisitionFailuresHandler(pool)))
	registerAdminUsers(se, cfg, app)
	registerAdminUsage(se, cfg, app, usageStore)
	registerAdminPlugins(se, cfg, pluginRegistry, remote)
}

// adminWhoamiHandler reports the caller's provider login (GitHub, else
// Gitea — whichever is stamped on the record) and admin status so the
// frontend can decide whether to show the admin nav. Session-only
// (PAT auth rejected with the sessionGuard 403, same as /api/pat).
//
// is_admin stays allowlist-only (the ops dashboard gate). The two
// role fields mirror the /api/admin/users guard: is_user_admin = an
// allowlist admin OR is_admin OR is_superadmin (drives the
// user-management nav), is_superadmin = an allowlist admin OR
// is_superadmin (drives the is_admin toggle column on that page).
func adminWhoamiHandler(cfg *config.Config) func(re *core.RequestEvent) error {
	return func(re *core.RequestEvent) error {
		login := ""
		if re.Auth != nil {
			// Display value only: show the GitHub login when stamped,
			// else the Gitea login, else empty. Both allowlists are
			// consulted below regardless of which one is set.
			login = re.Auth.GetString(auth.GitHubLoginField)
			if login == "" {
				login = re.Auth.GetString(auth.GiteaLoginField)
			}
		}
		envAdmin := isAdminCaller(re, cfg)
		isSuper := envAdmin
		isUserAdmin := envAdmin
		if re.Auth != nil {
			if re.Auth.GetBool(auth.IsSuperadminField) {
				isSuper = true
				isUserAdmin = true
			}
			if re.Auth.GetBool(auth.IsAdminField) {
				isUserAdmin = true
			}
		}
		return re.JSON(http.StatusOK, map[string]any{
			"login":          login,
			"is_admin":       envAdmin,
			"is_user_admin":  isUserAdmin,
			"is_superadmin":  isSuper,
		})
	}
}

// --- DB schema introspection -------------------------------------------------

// Response contract for GET /api/admin/db/schema. All tables in the
// public schema (goose_db_version included — admins want the full
// picture), ordered alphabetically.

type adminSchemaColumn struct {
	Name     string  `json:"name"`
	DataType string  `json:"data_type"`
	Nullable bool    `json:"nullable"`
	Default  *string `json:"default"`
	IsPK     bool    `json:"is_pk"`
}

type adminSchemaIndex struct {
	Name       string `json:"name"`
	Definition string `json:"definition"`
}

type adminSchemaConstraint struct {
	Name       string `json:"name"`
	Kind       string `json:"kind"` // PRIMARY KEY | FOREIGN KEY | UNIQUE | CHECK | EXCLUDE
	Definition string `json:"definition"`
}

type adminSchemaTable struct {
	Name        string                  `json:"name"`
	RowEstimate int64                   `json:"row_estimate"`
	TotalSize   string                  `json:"total_size"` // pg_size_pretty(pg_total_relation_size)
	Columns     []adminSchemaColumn     `json:"columns"`
	Indexes     []adminSchemaIndex      `json:"indexes"`
	Constraints []adminSchemaConstraint `json:"constraints"`
}

type adminDBSchemaResponse struct {
	Database string             `json:"database"`
	Tables   []adminSchemaTable `json:"tables"`
}

// Read-only catalog queries. All SELECTs against information_schema /
// pg_catalog, scoped to schema 'public'. No user input is interpolated.

const adminSchemaDatabaseSQL = `SELECT current_database()`

const adminSchemaTablesSQL = `
SELECT c.relname,
       GREATEST(c.reltuples, 0)::bigint,
       pg_size_pretty(pg_total_relation_size(c.oid))
FROM pg_class c
JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname = 'public' AND c.relkind IN ('r', 'p')
ORDER BY c.relname`

const adminSchemaColumnsSQL = `
SELECT table_name,
       column_name,
       CASE WHEN data_type = 'USER-DEFINED' THEN udt_name ELSE data_type END,
       is_nullable,
       column_default
FROM information_schema.columns
WHERE table_schema = 'public'
ORDER BY table_name, ordinal_position`

const adminSchemaPKColumnsSQL = `
SELECT t.relname, a.attname
FROM pg_index i
JOIN pg_class t ON t.oid = i.indrelid
JOIN pg_namespace n ON n.oid = t.relnamespace
JOIN pg_attribute a ON a.attrelid = t.oid AND a.attnum = ANY (i.indkey)
WHERE i.indisprimary AND n.nspname = 'public'`

const adminSchemaIndexesSQL = `
SELECT tablename, indexname, indexdef
FROM pg_indexes
WHERE schemaname = 'public'
ORDER BY tablename, indexname`

const adminSchemaConstraintsSQL = `
SELECT t.relname,
       c.conname,
       CASE c.contype
           WHEN 'p' THEN 'PRIMARY KEY'
           WHEN 'f' THEN 'FOREIGN KEY'
           WHEN 'u' THEN 'UNIQUE'
           WHEN 'c' THEN 'CHECK'
           WHEN 'x' THEN 'EXCLUDE'
           ELSE c.contype::text
       END,
       pg_get_constraintdef(c.oid)
FROM pg_constraint c
JOIN pg_class t ON t.oid = c.conrelid
JOIN pg_namespace n ON n.oid = t.relnamespace
WHERE n.nspname = 'public'
ORDER BY t.relname, c.conname`

// adminDBSchemaHandler introspects the Postgres registry database. 503
// when the pool is missing (DSN unset) or any catalog query fails —
// same convention as the papers routes ({"detail": "..."} body).
func adminDBSchemaHandler(pool *pgxpool.Pool) func(re *core.RequestEvent) error {
	return func(re *core.RequestEvent) error {
		if pool == nil {
			return re.JSON(http.StatusServiceUnavailable, map[string]string{
				"detail": "postgres registry unavailable (QATLAS_POSTGRES_DSN unset)",
			})
		}
		ctx, cancel := context.WithTimeout(re.Request.Context(), 15*time.Second)
		defer cancel()
		schema, err := fetchDBSchema(ctx, pool)
		if err != nil {
			slog.Error("admin: db schema introspection failed", "error", err)
			return re.JSON(http.StatusServiceUnavailable, map[string]string{
				"detail": "postgres registry unavailable (schema introspection failed); retry shortly",
			})
		}
		return re.JSON(http.StatusOK, schema)
	}
}

// fetchDBSchema assembles the full schema report with five sequential
// catalog queries. Sequential (not concurrent) keeps the code obvious;
// catalog reads on a handful of tables are sub-millisecond.
func fetchDBSchema(ctx context.Context, pool *pgxpool.Pool) (*adminDBSchemaResponse, error) {
	var resp adminDBSchemaResponse
	if err := pool.QueryRow(ctx, adminSchemaDatabaseSQL).Scan(&resp.Database); err != nil {
		return nil, err
	}

	tableIndex := map[string]int{}
	rows, err := pool.Query(ctx, adminSchemaTablesSQL)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var t adminSchemaTable
		if err := rows.Scan(&t.Name, &t.RowEstimate, &t.TotalSize); err != nil {
			return nil, err
		}
		// Non-nil slices so the JSON renders [] rather than null.
		t.Columns = []adminSchemaColumn{}
		t.Indexes = []adminSchemaIndex{}
		t.Constraints = []adminSchemaConstraint{}
		tableIndex[t.Name] = len(resp.Tables)
		resp.Tables = append(resp.Tables, t)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()

	if err := collectColumns(ctx, pool, &resp, tableIndex); err != nil {
		return nil, err
	}
	if err := collectPKColumns(ctx, pool, &resp, tableIndex); err != nil {
		return nil, err
	}
	if err := collectIndexes(ctx, pool, &resp, tableIndex); err != nil {
		return nil, err
	}
	if err := collectConstraints(ctx, pool, &resp, tableIndex); err != nil {
		return nil, err
	}
	return &resp, nil
}

func collectColumns(ctx context.Context, pool *pgxpool.Pool, resp *adminDBSchemaResponse, tableIndex map[string]int) error {
	rows, err := pool.Query(ctx, adminSchemaColumnsSQL)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var table string
		var col adminSchemaColumn
		var nullable string
		if err := rows.Scan(&table, &col.Name, &col.DataType, &nullable, &col.Default); err != nil {
			return err
		}
		col.Nullable = nullable == "YES"
		if i, ok := tableIndex[table]; ok {
			resp.Tables[i].Columns = append(resp.Tables[i].Columns, col)
		}
	}
	return rows.Err()
}

func collectPKColumns(ctx context.Context, pool *pgxpool.Pool, resp *adminDBSchemaResponse, tableIndex map[string]int) error {
	rows, err := pool.Query(ctx, adminSchemaPKColumnsSQL)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var table, column string
		if err := rows.Scan(&table, &column); err != nil {
			return err
		}
		if i, ok := tableIndex[table]; ok {
			for j := range resp.Tables[i].Columns {
				if resp.Tables[i].Columns[j].Name == column {
					resp.Tables[i].Columns[j].IsPK = true
				}
			}
		}
	}
	return rows.Err()
}

func collectIndexes(ctx context.Context, pool *pgxpool.Pool, resp *adminDBSchemaResponse, tableIndex map[string]int) error {
	rows, err := pool.Query(ctx, adminSchemaIndexesSQL)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var table string
		var idx adminSchemaIndex
		if err := rows.Scan(&table, &idx.Name, &idx.Definition); err != nil {
			return err
		}
		if i, ok := tableIndex[table]; ok {
			resp.Tables[i].Indexes = append(resp.Tables[i].Indexes, idx)
		}
	}
	return rows.Err()
}

func collectConstraints(ctx context.Context, pool *pgxpool.Pool, resp *adminDBSchemaResponse, tableIndex map[string]int) error {
	rows, err := pool.Query(ctx, adminSchemaConstraintsSQL)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var table string
		var con adminSchemaConstraint
		if err := rows.Scan(&table, &con.Name, &con.Kind, &con.Definition); err != nil {
			return err
		}
		if i, ok := tableIndex[table]; ok {
			resp.Tables[i].Constraints = append(resp.Tables[i].Constraints, con)
		}
	}
	return rows.Err()
}
