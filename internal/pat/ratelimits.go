// Default rate-limit rules for the PAT surface.
//
// PocketBase ships a built-in rate limiter (see apis.middlewares_rate_limit.go)
// that consults Settings().RateLimits.Rules on every request and rejects
// callers that exceed the matching rule's MaxRequests-per-Duration budget.
// The default Settings has rate limiting **disabled** and zero rules
// configured, which is the right development default — but in production
// we want at least two guard-rails on /api/pat so a stolen session JWT
// can't be used to flood-mint PATs and an anonymous bcrypt-DoS can't
// fill the activity log.
//
// EnsureDefaults installs those two rules **idempotently** at server
// startup. Idempotency matters because:
//
//   - Each restart re-applies, so the rules survive a forgotten admin-
//     UI save (operator removed them in dev, forgot to put them back).
//   - If an admin manually tightens a rule in the admin UI, our code
//     respects it — we only Add when no rule matches the label.
//   - Settings are persisted to the SQLite settings table; saving on
//     every boot with identical content is a no-op for SQLite (no row
//     change, no WAL traffic).
//
// What we apply (production policy):
//
//   - POST /api/pat   audience=@auth   10 req / 60s   (anti-flood-mint
//     when a session JWT is compromised)
//   - POST /api/pat   audience=@guest  30 req / 60s   (anti-log-flood
//     and anti-brute-bcrypt; all guest /api/pat calls 401 immediately
//     so this is generous, but caps the worst case)
//
// Rules are NOT applied to the rest of the write surface — at PAT
// entropy (143 bits) the brute-force angle is already unreachable, and
// we don't want to surprise legitimate callers (CI pipelines, batch
// uploaders) with 429s. Per-endpoint rate limits can be added later by
// extending this slice.

package pat

import (
	"github.com/pocketbase/pocketbase/core"
)

// DefaultRateLimitRules is the canonical rule set ratchet that
// EnsureDefaults installs. Exported (rather than embedded inside
// EnsureDefaults) so tests can verify the policy without going
// through a real PocketBase Settings round-trip.
var DefaultRateLimitRules = []core.RateLimitRule{
	{
		Label:       "POST /api/pat",
		Audience:    core.RateLimitRuleAudienceAuth,
		Duration:    60,
		MaxRequests: 10,
	},
	{
		Label:       "POST /api/pat",
		Audience:    core.RateLimitRuleAudienceGuest,
		Duration:    60,
		MaxRequests: 30,
	},
}

// EnsureDefaults applies the PAT-surface rate-limit rules to the
// given app's Settings, idempotently. Returns (changed, error) where
// changed is true iff Settings had to be saved (no rule existed for
// at least one of our labels, OR the rate limiter itself was
// disabled). Caller decides whether to log the change.
//
// Call at OnBootstrap (after PocketBase has loaded the Settings from
// the SQLite settings table). At that point app.Settings() is the
// live, in-memory copy that the rate limit middleware consults.
//
// Concurrency note: Settings is *.SettingsModel, designed to be
// mutated under the app's settings lock — PocketBase's own admin UI
// PUT goes through app.Save(settings) which holds the same lock.
// Calling EnsureDefaults from OnBootstrap is safe (single-threaded
// startup, no concurrent admin UI writes).
func EnsureDefaults(app core.App) (changed bool, err error) {
	settings := app.Settings()

	added := false
	for _, want := range DefaultRateLimitRules {
		if hasMatchingRule(settings.RateLimits.Rules, want) {
			continue
		}
		settings.RateLimits.Rules = append(settings.RateLimits.Rules, want)
		added = true
	}

	enabledFlip := false
	if added && !settings.RateLimits.Enabled {
		settings.RateLimits.Enabled = true
		enabledFlip = true
	}

	if !added && !enabledFlip {
		return false, nil
	}

	if err := app.Save(settings); err != nil {
		return false, err
	}
	return true, nil
}

// hasMatchingRule returns true iff the slice already contains a rule
// covering (label, audience). We match on those two fields only — if
// the existing rule has a tighter MaxRequests / Duration than our
// default, that's an operator's deliberate tightening and we leave
// it alone. If it's looser, that's also their call (e.g. local dev
// box raising the limit for load testing) — we don't paternalistically
// override.
func hasMatchingRule(existing []core.RateLimitRule, want core.RateLimitRule) bool {
	for _, r := range existing {
		if r.Label == want.Label && r.Audience == want.Audience {
			return true
		}
	}
	return false
}
