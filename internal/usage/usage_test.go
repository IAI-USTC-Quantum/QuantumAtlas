package usage

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/registry"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestResolveEffectiveLimit_Priority(t *testing.T) {
	override := 7
	planLimit := 42
	if got := ResolveEffectiveLimit(&override, &planLimit, 100); got != 7 {
		t.Errorf("override wins: got %d, want 7", got)
	}
	if got := ResolveEffectiveLimit(nil, &planLimit, 100); got != 42 {
		t.Errorf("plan wins over default: got %d, want 42", got)
	}
	if got := ResolveEffectiveLimit(nil, nil, 100); got != 100 {
		t.Errorf("default fallback: got %d, want 100", got)
	}
}

// TestStore_NilPool mirrors the registry.Store contract: with no pool
// every method reports registry.ErrCatalogUnavailable.
func TestStore_NilPool(t *testing.T) {
	s := NewStore(nil)
	ctx := context.Background()

	if _, _, err := s.CheckAndReserve(ctx, "u", MetricAgenticSearch, 10); !errors.Is(err, registry.ErrCatalogUnavailable) {
		t.Errorf("CheckAndReserve err = %v", err)
	}
	if err := s.Refund(ctx, "u", MetricAgenticSearch); !errors.Is(err, registry.ErrCatalogUnavailable) {
		t.Errorf("Refund err = %v", err)
	}
	if err := s.RecordTokens(ctx, "u", MetricAgenticSearch, 1); !errors.Is(err, registry.ErrCatalogUnavailable) {
		t.Errorf("RecordTokens err = %v", err)
	}
	if _, err := s.EffectiveLimit(ctx, "u", 10); !errors.Is(err, registry.ErrCatalogUnavailable) {
		t.Errorf("EffectiveLimit err = %v", err)
	}
	if _, err := s.DailyUsage(ctx, "", 10); !errors.Is(err, registry.ErrCatalogUnavailable) {
		t.Errorf("DailyUsage err = %v", err)
	}
	if _, err := s.ListPlans(ctx); !errors.Is(err, registry.ErrCatalogUnavailable) {
		t.Errorf("ListPlans err = %v", err)
	}
	if err := s.UpsertPlan(ctx, Plan{Name: "x", DailyLimit: 1}); !errors.Is(err, registry.ErrCatalogUnavailable) {
		t.Errorf("UpsertPlan err = %v", err)
	}
	if _, err := s.PlanExists(ctx, "free"); !errors.Is(err, registry.ErrCatalogUnavailable) {
		t.Errorf("PlanExists err = %v", err)
	}
	if err := s.UpsertUserQuota(ctx, "u", nil, nil); !errors.Is(err, registry.ErrCatalogUnavailable) {
		t.Errorf("UpsertUserQuota err = %v", err)
	}
}

// ---------------------------------------------------------------------------
// Live-PostgreSQL integration tests (QATLAS_TEST_PG_DSN-gated, like
// internal/registry). Default `go test` skips them.
// ---------------------------------------------------------------------------

func testPool(t *testing.T) (*pgxpool.Pool, context.Context) {
	t.Helper()
	dsn := os.Getenv("QATLAS_TEST_PG_DSN")
	if dsn == "" {
		t.Skip("QATLAS_TEST_PG_DSN unset; skipping live-PostgreSQL integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)
	if err := registry.Migrate(ctx, pool); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return pool, ctx
}

// cleanupUsage removes the rows this suite created.
func cleanupUsage(t *testing.T, pool *pgxpool.Pool, userIDs ...string) {
	t.Helper()
	ctx := context.Background()
	for _, id := range userIDs {
		_, _ = pool.Exec(ctx, `DELETE FROM usage_daily WHERE user_id = $1`, id)
		_, _ = pool.Exec(ctx, `DELETE FROM user_quotas WHERE user_id = $1`, id)
	}
	_, _ = pool.Exec(ctx, `DELETE FROM plans WHERE name = 'test-plan'`)
}

func TestIntegrationCheckAndReserveRefund(t *testing.T) {
	pool, ctx := testPool(t)
	s := NewStore(pool)
	const user = "usage-it-reserve"
	cleanupUsage(t, pool, user)
	t.Cleanup(func() { cleanupUsage(t, pool, user) })

	// Limit 2: two reservations succeed, the third is refused.
	for i, want := range []int{1, 2} {
		today, ok, err := s.CheckAndReserve(ctx, user, MetricAgenticSearch, 2)
		if err != nil || !ok {
			t.Fatalf("reserve %d: today=%d ok=%v err=%v", i, today, ok, err)
		}
		if today != want {
			t.Errorf("reserve %d: today=%d, want %d", i, today, want)
		}
	}
	today, ok, err := s.CheckAndReserve(ctx, user, MetricAgenticSearch, 2)
	if err != nil || ok {
		t.Fatalf("over-limit reserve: today=%d ok=%v err=%v, want ok=false", today, ok, err)
	}
	if today != 2 {
		t.Errorf("over-limit today=%d, want 2 (current count)", today)
	}

	// Refund releases one slot; the next reserve succeeds again.
	if err := s.Refund(ctx, user, MetricAgenticSearch); err != nil {
		t.Fatalf("Refund: %v", err)
	}
	if today, ok, err := s.CheckAndReserve(ctx, user, MetricAgenticSearch, 2); err != nil || !ok || today != 2 {
		t.Errorf("reserve after refund: today=%d ok=%v err=%v, want (2, true, nil)", today, ok, err)
	}

	// RecordTokens accumulates.
	if err := s.RecordTokens(ctx, user, MetricAgenticSearch, 100); err != nil {
		t.Fatalf("RecordTokens: %v", err)
	}
	if err := s.RecordTokens(ctx, user, MetricAgenticSearch, 50); err != nil {
		t.Fatalf("RecordTokens: %v", err)
	}
	rows, err := s.DailyUsage(ctx, "", 999)
	if err != nil {
		t.Fatalf("DailyUsage: %v", err)
	}
	var found *UserUsage
	for i := range rows {
		if rows[i].UserID == user {
			found = &rows[i]
		}
	}
	if found == nil {
		t.Fatalf("user %s missing from DailyUsage: %+v", user, rows)
	}
	if found.Count != 2 || found.LLMTokens != 150 {
		t.Errorf("bucket = count %d tokens %d, want 2 / 150", found.Count, found.LLMTokens)
	}
	if found.EffectiveLimit != 999 {
		t.Errorf("EffectiveLimit = %d, want 999 (config default, no quota row)", found.EffectiveLimit)
	}
}

func TestIntegrationEffectiveLimitAndQuotas(t *testing.T) {
	pool, ctx := testPool(t)
	s := NewStore(pool)
	const user = "usage-it-quota"
	cleanupUsage(t, pool, user)
	t.Cleanup(func() { cleanupUsage(t, pool, user) })

	// No quota row → config default.
	if got, err := s.EffectiveLimit(ctx, user, 123); err != nil || got != 123 {
		t.Errorf("no quota row: got %d, err %v, want 123", got, err)
	}

	// Plan binding (seeded 'pro' = 100000) wins over the default.
	plan := "pro"
	if err := s.UpsertUserQuota(ctx, user, &plan, nil); err != nil {
		t.Fatalf("UpsertUserQuota plan: %v", err)
	}
	if got, err := s.EffectiveLimit(ctx, user, 123); err != nil || got != 100000 {
		t.Errorf("plan limit: got %d, err %v, want 100000", got, err)
	}

	// A hard override beats the plan.
	override := 5
	if err := s.UpsertUserQuota(ctx, user, nil, &override); err != nil {
		t.Fatalf("UpsertUserQuota override: %v", err)
	}
	if got, err := s.EffectiveLimit(ctx, user, 123); err != nil || got != 5 {
		t.Errorf("override: got %d, err %v, want 5", got, err)
	}

	// Plans: seeded defaults exist; upserting a custom plan round-trips.
	plans, err := s.ListPlans(ctx)
	if err != nil {
		t.Fatalf("ListPlans: %v", err)
	}
	names := map[string]int{}
	for _, p := range plans {
		names[p.Name] = p.DailyLimit
	}
	if names["free"] != 10000 || names["pro"] != 100000 || names["max"] != 1000000 {
		t.Errorf("seeded plans wrong: %v", names)
	}
	if err := s.UpsertPlan(ctx, Plan{Name: "test-plan", DailyLimit: 3, Description: "it"}); err != nil {
		t.Fatalf("UpsertPlan: %v", err)
	}
	exists, err := s.PlanExists(ctx, "test-plan")
	if err != nil || !exists {
		t.Errorf("PlanExists(test-plan) = %v, %v", exists, err)
	}
}
