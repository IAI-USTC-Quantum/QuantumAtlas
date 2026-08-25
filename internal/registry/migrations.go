// Package registry is the PostgreSQL-backed paper registry with a
// versioned (goose) schema. It owns the papers + paper_assets +
// paper_identities tables and exposes the multi-identity dedup entry
// point ResolveOrMint: every way a work can be named (DOI, bare arXiv
// id, versioned arXiv id, title hash) resolves through paper_identities
// to a single surrogate paper_id ("qa_" + lowercase ULID), minted
// transactionally on first sight.
//
// The connection pool is optional so local dev can run without
// PostgreSQL: with a nil pool every operation reports
// ErrCatalogUnavailable. Schema management lives in Migrate /
// SchemaVersion (goose, embedded migrations).
package registry

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

//go:embed migrations
var migrationsFS embed.FS

// ErrSchemaTooNew marks the "database is newer than the binary" refusal:
// Migrate wraps it when the applied goose version exceeds the latest
// bundled migration, so callers (main.go) can treat it as a hard,
// non-retryable fatal instead of a transient connection failure.
var ErrSchemaTooNew = errors.New("registry: database schema is newer than the binary")

// Migrate applies all bundled migrations (goose up). Guards against
// running an old binary against a newer database: when the applied
// schema version exceeds the latest bundled migration it refuses with an
// error instead of leaving the schema half-migrated. Returns
// ErrCatalogUnavailable when no pool is configured.
func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
	if pool == nil {
		return ErrCatalogUnavailable
	}
	p, err := newProvider(pool)
	if err != nil {
		return err
	}
	dbVersion, err := p.GetDBVersion(ctx)
	if errors.Is(err, goose.ErrVersionNotFound) {
		dbVersion = 0
	} else if err != nil {
		return fmt.Errorf("registry: read schema version: %w", err)
	}
	if latest := latestSourceVersion(p); dbVersion > latest {
		return fmt.Errorf("%w: database schema version %d is newer than latest bundled migration %d; upgrade the binary", ErrSchemaTooNew, dbVersion, latest)
	}
	if _, err := p.Up(ctx); err != nil {
		return fmt.Errorf("registry: migrate: %w", err)
	}
	return nil
}

// SchemaVersion returns the latest applied migration version (0 when the
// goose version table does not exist yet). Returns
// ErrCatalogUnavailable when no pool is configured.
func SchemaVersion(ctx context.Context, pool *pgxpool.Pool) (int64, error) {
	if pool == nil {
		return 0, ErrCatalogUnavailable
	}
	p, err := newProvider(pool)
	if err != nil {
		return 0, err
	}
	v, err := p.GetDBVersion(ctx)
	if errors.Is(err, goose.ErrVersionNotFound) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("registry: read schema version: %w", err)
	}
	return v, nil
}

// newProvider builds a goose provider over the embedded migrations,
// sharing the caller's pgx pool via the stdlib bridge.
func newProvider(pool *pgxpool.Pool) (*goose.Provider, error) {
	sub, err := fs.Sub(migrationsFS, "migrations")
	if err != nil {
		return nil, fmt.Errorf("registry: open embedded migrations: %w", err)
	}
	p, err := goose.NewProvider(
		goose.DialectPostgres,
		stdlib.OpenDBFromPool(pool),
		sub,
		goose.WithLogger(slogGooseLogger{}),
	)
	if err != nil {
		return nil, fmt.Errorf("registry: goose provider: %w", err)
	}
	return p, nil
}

// latestSourceVersion returns the highest bundled migration version.
func latestSourceVersion(p *goose.Provider) int64 {
	var latest int64
	for _, src := range p.ListSources() {
		if src.Version > latest {
			latest = src.Version
		}
	}
	return latest
}

// slogGooseLogger routes goose's log output into log/slog.
type slogGooseLogger struct{}

func (slogGooseLogger) Fatalf(format string, v ...any) { slog.Error(fmt.Sprintf(format, v...)) }
func (slogGooseLogger) Printf(format string, v ...any) { slog.Info(fmt.Sprintf(format, v...)) }
