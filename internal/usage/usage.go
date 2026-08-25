// Package usage is the per-user metering store behind the metered
// agentic-search surface (POST /api/search/agentic). It tracks daily
// call counts and LLM token spend in the Postgres registry database
// (migration 00002_usage: plans / user_quotas / usage_daily) and
// exposes the atomic check-and-reserve primitive the endpoint uses to
// enforce per-user daily limits.
//
// "Day" buckets follow the DATABASE's CURRENT_DATE — i.e. the Postgres
// server's timezone (deployments run with TZ=UTC), so a "day" here is
// the database-UTC day, not the caller's local one.
//
// The pool is optional like the registry's: a nil pool makes every
// method report registry.ErrCatalogUnavailable so the routes layer can
// map it to its usual 503 contract.
package usage

import (
	"context"
	"errors"
	"time"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/registry"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// MetricAgenticSearch is the usage_daily metric bucket for agentic
// search calls.
const MetricAgenticSearch = "agentic_search"

// Store wraps the registry Postgres pool with usage-metering queries.
type Store struct {
	pool *pgxpool.Pool
}

// NewStore builds a Store. pool may be nil — all methods then report
// registry.ErrCatalogUnavailable (same nil-pool contract as
// registry.Store).
func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

// Plan is one row of the plans lookup table.
type Plan struct {
	Name        string `json:"name"`
	DailyLimit  int    `json:"daily_agentic_search_limit"`
	Description string `json:"description"`
}

// UserUsage is one (user, metric) bucket of one day, joined with the
// user's plan/quota state and its resolved effective limit. Plan is
// empty when the user has no user_quotas row.
type UserUsage struct {
	UserID         string `json:"user_id"`
	Day            string `json:"day"`
	Metric         string `json:"metric"`
	Count          int    `json:"count"`
	LLMTokens      int64  `json:"llm_tokens"`
	Plan           string `json:"plan"`
	Override       *int   `json:"daily_limit_override,omitempty"`
	EffectiveLimit int    `json:"effective_limit"`
}

// ResolveEffectiveLimit applies the limit-resolution priority:
// per-user override > plan limit > server-configured default. Pure
// function, unit-tested without a database.
func ResolveEffectiveLimit(override *int, planLimit *int, configDefault int) int {
	if override != nil {
		return *override
	}
	if planLimit != nil {
		return *planLimit
	}
	return configDefault
}

// CheckAndReserve atomically consumes one unit of (userID, today,
// metric) iff the bucket is below limit. The conditional increment runs
// as a single INSERT .. ON CONFLICT .. WHERE, so concurrent callers can
// never overshoot the limit. On success today is the post-increment
// count. When the limit is reached, ok=false and today is the current
// count (a best-effort follow-up read — never affects the decision).
func (s *Store) CheckAndReserve(ctx context.Context, userID, metric string, limit int) (today int, ok bool, err error) {
	if s.pool == nil {
		return 0, false, registry.ErrCatalogUnavailable
	}
	err = s.pool.QueryRow(ctx, `
		INSERT INTO usage_daily (user_id, day, metric, count) VALUES ($1, CURRENT_DATE, $2, 1)
		ON CONFLICT (user_id, day, metric) DO UPDATE SET count = usage_daily.count + 1
		WHERE usage_daily.count < $3
		RETURNING count`, userID, metric, limit).Scan(&today)
	if err == nil {
		return today, true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return 0, false, err
	}
	// Limit reached: report the current count for the 429 body.
	_ = s.pool.QueryRow(ctx, `
		SELECT count FROM usage_daily
		WHERE user_id = $1 AND day = CURRENT_DATE AND metric = $2`, userID, metric).Scan(&today)
	return today, false, nil
}

// Refund returns one unit to today's bucket (floor 0). Called when the
// upstream call failed after a successful CheckAndReserve so users are
// not charged for requests the server could not fulfil.
func (s *Store) Refund(ctx context.Context, userID, metric string) error {
	if s.pool == nil {
		return registry.ErrCatalogUnavailable
	}
	_, err := s.pool.Exec(ctx, `
		UPDATE usage_daily SET count = GREATEST(count - 1, 0)
		WHERE user_id = $1 AND day = CURRENT_DATE AND metric = $2`, userID, metric)
	return err
}

// RecordTokens adds tokens to today's llm_tokens accumulator for the
// bucket (inserting a 0-count row first if the bucket does not exist
// yet, which cannot happen on the normal reserve-then-record path but
// keeps the method safe standalone).
func (s *Store) RecordTokens(ctx context.Context, userID, metric string, tokens int64) error {
	if s.pool == nil {
		return registry.ErrCatalogUnavailable
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO usage_daily (user_id, day, metric, count, llm_tokens) VALUES ($1, CURRENT_DATE, $2, 0, $3)
		ON CONFLICT (user_id, day, metric) DO UPDATE SET llm_tokens = usage_daily.llm_tokens + $3`,
		userID, metric, tokens)
	return err
}

// EffectiveLimit resolves the daily limit for userID:
// user_quotas.daily_limit_override > the user's plan limit > configDefault.
func (s *Store) EffectiveLimit(ctx context.Context, userID string, configDefault int) (int, error) {
	if s.pool == nil {
		return 0, registry.ErrCatalogUnavailable
	}
	var override, planLimit *int
	err := s.pool.QueryRow(ctx, `
		SELECT q.daily_limit_override, p.daily_agentic_search_limit
		FROM user_quotas q
		LEFT JOIN plans p ON p.name = q.plan
		WHERE q.user_id = $1`, userID).Scan(&override, &planLimit)
	if errors.Is(err, pgx.ErrNoRows) {
		return configDefault, nil
	}
	if err != nil {
		return 0, err
	}
	return ResolveEffectiveLimit(override, planLimit, configDefault), nil
}

// DailyUsage lists every (user, metric) bucket for one day joined with
// plan/quota state and the resolved effective limit. An empty day means
// CURRENT_DATE. configDefault feeds ResolveEffectiveLimit for users
// without a quota row.
func (s *Store) DailyUsage(ctx context.Context, day string, configDefault int) ([]UserUsage, error) {
	if s.pool == nil {
		return nil, registry.ErrCatalogUnavailable
	}
	var dayArg *string
	if day != "" {
		dayArg = &day
	}
	rows, err := s.pool.Query(ctx, `
		SELECT u.user_id, u.day, u.metric, u.count, u.llm_tokens,
		       q.plan, q.daily_limit_override, p.daily_agentic_search_limit
		FROM usage_daily u
		LEFT JOIN user_quotas q ON q.user_id = u.user_id
		LEFT JOIN plans p ON p.name = q.plan
		WHERE u.day = COALESCE($1::date, CURRENT_DATE)
		ORDER BY u.user_id, u.metric`, dayArg)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []UserUsage{}
	for rows.Next() {
		var u UserUsage
		var day time.Time
		var plan *string
		var override, planLimit *int
		if err := rows.Scan(&u.UserID, &day, &u.Metric, &u.Count, &u.LLMTokens,
			&plan, &override, &planLimit); err != nil {
			return nil, err
		}
		u.Day = day.Format("2006-01-02")
		if plan != nil {
			u.Plan = *plan
		}
		u.Override = override
		u.EffectiveLimit = ResolveEffectiveLimit(override, planLimit, configDefault)
		out = append(out, u)
	}
	return out, rows.Err()
}

// ListPlans returns all plans ordered by name.
func (s *Store) ListPlans(ctx context.Context) ([]Plan, error) {
	if s.pool == nil {
		return nil, registry.ErrCatalogUnavailable
	}
	rows, err := s.pool.Query(ctx, `
		SELECT name, daily_agentic_search_limit, description FROM plans ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Plan{}
	for rows.Next() {
		var p Plan
		if err := rows.Scan(&p.Name, &p.DailyLimit, &p.Description); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// UpsertPlan inserts or replaces one plan row.
func (s *Store) UpsertPlan(ctx context.Context, p Plan) error {
	if s.pool == nil {
		return registry.ErrCatalogUnavailable
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO plans (name, daily_agentic_search_limit, description) VALUES ($1, $2, $3)
		ON CONFLICT (name) DO UPDATE SET
		    daily_agentic_search_limit = EXCLUDED.daily_agentic_search_limit,
		    description = EXCLUDED.description`,
		p.Name, p.DailyLimit, p.Description)
	return err
}

// PlanExists reports whether name is a known plan (used to give the
// admin API a clean 400 instead of an FK-violation 500).
func (s *Store) PlanExists(ctx context.Context, name string) (bool, error) {
	if s.pool == nil {
		return false, registry.ErrCatalogUnavailable
	}
	var exists bool
	err := s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM plans WHERE name = $1)`, name).Scan(&exists)
	return exists, err
}

// UpsertUserQuota binds userID to a plan and/or a hard daily-limit
// override. A nil plan keeps the existing binding (or 'free' on
// insert); a nil override CLEARS the override (the admin API treats
// absent and explicit-null the same). The caller must validate the plan
// exists first (PlanExists) when a plan is given.
func (s *Store) UpsertUserQuota(ctx context.Context, userID string, plan *string, override *int) error {
	if s.pool == nil {
		return registry.ErrCatalogUnavailable
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO user_quotas (user_id, plan, daily_limit_override) VALUES ($1, COALESCE($2, 'free'), $3)
		ON CONFLICT (user_id) DO UPDATE SET
		    plan = COALESCE($2, user_quotas.plan),
		    daily_limit_override = $3,
		    updated_at = now()`,
		userID, plan, override)
	return err
}
