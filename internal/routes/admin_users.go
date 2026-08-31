// DB-flag user management: GET/PATCH /api/admin/users.
//
// This is the surface backed by the users-record role flags (see
// internal/auth/migrations.go 1788100000_add_role_flags_to_users.go):
//
//	is_admin      — may list all users and toggle their availability
//	                (disabled); the flag itself is READ-ONLY to them.
//	is_superadmin — additionally may toggle other users' is_admin.
//
// The env-derived allowlist admin (Config.IsGitHubAdmin, adminGuard)
// is a strict superset here: it passes userAdminGuard and is treated
// as superadmin-equivalent, so the operator always retains role
// management even when every DB flag is off.
//
//	GET   /api/admin/users        — userAdminGuard; every users record
//	                                 (id, name, email, github_login,
//	                                 is_admin, is_superadmin, disabled,
//	                                 created, updated).
//	PATCH /api/admin/users/{id}   — userAdminGuard; body is a JSON
//	                                 object with any of {"disabled":
//	                                 bool, "is_admin": bool}. At least
//	                                 one key required. is_admin needs
//	                                 superadmin (else 403).
//
// Self-protection rules (prevent lockout of the managing surface):
//
//   - nobody may set disabled on their own record (you'd lock
//     yourself out of the very surface you're using);
//   - nobody may change their own is_admin;
//   - a non-superadmin may not disable a superadmin.
//
// Note is_superadmin is deliberately NOT patchable over this API:
// it is seeded from auth.superadmin_logins at bootstrap (see
// auth.promoteRoleFlags) or set via the PocketBase admin UI — an
// in-band promotion right would make privilege escalation a single
// leaked-admin-session away.
package routes

import (
	"encoding/json"
	"net/http"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/auth"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/config"

	"github.com/pocketbase/pocketbase/core"
)

// userAdminGuard layers the DB-flag role check on top of sessionGuard
// (PATs rejected — user management is for humans, same posture as
// adminGuard). A caller passes when they hold a session AND at least
// one of: the env admin allowlist, is_admin, is_superadmin.
func userAdminGuard(cfg *config.Config, handler func(re *core.RequestEvent) error) func(re *core.RequestEvent) error {
	return sessionGuard(func(re *core.RequestEvent) error {
		if !isUserManager(re, cfg) {
			return re.JSON(http.StatusForbidden, map[string]string{
				"detail": "user management requires admin",
			})
		}
		return handler(re)
	})
}

// isUserManager reports whether the session-authenticated caller may
// use the /api/admin/users surface. sessionGuard has already run, so
// re.Auth is a live users record.
func isUserManager(re *core.RequestEvent, cfg *config.Config) bool {
	if re.Auth == nil {
		return false
	}
	if cfg.IsGitHubAdmin(re.Auth.GetString(auth.GitHubLoginField)) {
		return true
	}
	return re.Auth.GetBool(auth.IsAdminField) || re.Auth.GetBool(auth.IsSuperadminField)
}

// isSuperadminCaller reports whether the caller may modify is_admin
// flags: env allowlist admins (operators) or is_superadmin holders.
func isSuperadminCaller(re *core.RequestEvent, cfg *config.Config) bool {
	if re.Auth == nil {
		return false
	}
	if cfg.IsGitHubAdmin(re.Auth.GetString(auth.GitHubLoginField)) {
		return true
	}
	return re.Auth.GetBool(auth.IsSuperadminField)
}

// registerAdminUsers wires the user-management routes. Called from
// RegisterAdmin so all /api/admin/* wiring lives in one place.
func registerAdminUsers(se *core.ServeEvent, cfg *config.Config, app core.App) {
	se.Router.GET("/api/admin/users", userAdminGuard(cfg, adminListUsersHandler(app)))
	se.Router.PATCH("/api/admin/users/{id}", userAdminGuard(cfg, adminUpdateUserHandler(cfg, app)))
}

// adminUserJSON is the wire shape of a user both in the list response
// and after a PATCH — one shape everywhere so the SPA/CLI can render
// either with the same code.
func adminUserJSON(rec *core.Record) map[string]any {
	return map[string]any{
		"id":            rec.Id,
		"name":          rec.GetString("name"),
		"email":         rec.GetString("email"),
		"github_login":  rec.GetString(auth.GitHubLoginField),
		"is_admin":      rec.GetBool(auth.IsAdminField),
		"is_superadmin": rec.GetBool(auth.IsSuperadminField),
		"disabled":      rec.GetBool(auth.DisabledField),
		"created":       rec.GetDateTime("created").String(),
		"updated":       rec.GetDateTime("updated").String(),
	}
}

// adminListUsersHandler answers GET /api/admin/users with every users
// record, oldest first. The knowledge base is members-only so the user
// count stays small; pagination would be speculative.
func adminListUsersHandler(app core.App) func(re *core.RequestEvent) error {
	return func(re *core.RequestEvent) error {
		records, err := app.FindAllRecords(auth.UsersCollection)
		if err != nil {
			return re.JSON(http.StatusServiceUnavailable, map[string]string{
				"detail": "user store unavailable",
			})
		}
		users := make([]map[string]any, 0, len(records))
		for _, rec := range records {
			users = append(users, adminUserJSON(rec))
		}
		return re.JSON(http.StatusOK, map[string]any{
			"users": users,
			"total": len(users),
		})
	}
}

// adminUpdateUserBody is the PATCH /api/admin/users/{id} request.
// Pointer bools distinguish "absent" (don't touch) from "false".
type adminUpdateUserBody struct {
	Disabled *bool `json:"disabled"`
	IsAdmin  *bool `json:"is_admin"`
}

// adminUpdateUserHandler answers PATCH /api/admin/users/{id}.
func adminUpdateUserHandler(cfg *config.Config, app core.App) func(re *core.RequestEvent) error {
	return func(re *core.RequestEvent) error {
		var body adminUpdateUserBody
		if err := json.NewDecoder(re.Request.Body).Decode(&body); err != nil {
			return re.JSON(http.StatusBadRequest, map[string]string{
				"detail": "invalid JSON body",
			})
		}
		if body.Disabled == nil && body.IsAdmin == nil {
			return re.JSON(http.StatusBadRequest, map[string]string{
				"detail": `nothing to update: provide "disabled" and/or "is_admin"`,
			})
		}

		target, err := app.FindRecordById(auth.UsersCollection, re.Request.PathValue("id"))
		if err != nil {
			return re.JSON(http.StatusNotFound, map[string]string{
				"detail": "user not found",
			})
		}

		caller := re.Auth // sessionGuard guarantees non-nil
		self := target.Id == caller.Id

		if body.IsAdmin != nil {
			if !isSuperadminCaller(re, cfg) {
				return re.JSON(http.StatusForbidden, map[string]string{
					"detail": "changing is_admin requires superadmin",
				})
			}
			if self {
				return re.JSON(http.StatusForbidden, map[string]string{
					"detail": "cannot change your own admin flag",
				})
			}
			target.Set(auth.IsAdminField, *body.IsAdmin)
		}

		if body.Disabled != nil {
			if self {
				return re.JSON(http.StatusForbidden, map[string]string{
					"detail": "cannot disable your own account",
				})
			}
			if *body.Disabled &&
				!isSuperadminCaller(re, cfg) &&
				target.GetBool(auth.IsSuperadminField) {
				return re.JSON(http.StatusForbidden, map[string]string{
					"detail": "disabling a superadmin requires superadmin",
				})
			}
			target.Set(auth.DisabledField, *body.Disabled)
		}

		if err := app.Save(target); err != nil {
			return re.JSON(http.StatusInternalServerError, map[string]string{
				"detail": "save failed",
			})
		}
		return re.JSON(http.StatusOK, adminUserJSON(target))
	}
}
