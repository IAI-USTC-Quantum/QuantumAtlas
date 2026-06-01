package main

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"

	_ "modernc.org/sqlite"
)

// qatlasDBConnect is the *sql.DB connector PocketBase calls for both
// data.db and aux.db. It mirrors core.DefaultDBConnect's pragma list
// with three QuantumAtlas-specific tweaks:
//
//  1. busy_timeout bumped 10 s → 30 s. SQLite serialises all writes;
//     under bursty upload traffic the default 10 s window has been
//     observed to surface "database is locked" errors during long
//     PocketBase migrations on slow disks (the migrations run
//     transactionally and can hold the writer lock for a few seconds
//     each). 30 s costs nothing in the happy path because contention
//     is exceptional, and dramatically reduces the rate of operator
//     pages caused by transient lock waits.
//
//  2. mmap_size 0 → 64 MiB. SQLite normally reads pages via pread();
//     mmap-backed reads are page-cache resident and skip the syscall,
//     measurably faster for the large read-side queries qatlas does
//     to render the /api/papers/{id}/resources endpoint. 64 MiB is
//     conservative — PB has two DBs (data + aux), so the cap doubles
//     in resident memory, but only pages actually touched count.
//
//  3. PocketBase's other defaults (journal_mode=WAL,
//     journal_size_limit=200MB, synchronous=NORMAL, foreign_keys=ON,
//     temp_store=MEMORY, cache_size=-32000) are preserved verbatim.
//     We deliberately do NOT mass-override them — PB picks defaults
//     that work on every supported platform, and divergence makes
//     upstream changes hard to track.
//
// The pragma order matters: busy_timeout MUST come before
// journal_mode(WAL), per upstream comment in core.DefaultDBConnect:
// otherwise the WAL setup might race with another connection and
// fail without retrying.
func qatlasDBConnect(dbPath string) (*dbx.DB, error) {
	pragmas := "?_pragma=busy_timeout(30000)" +
		"&_pragma=journal_mode(WAL)" +
		"&_pragma=journal_size_limit(200000000)" +
		"&_pragma=synchronous(NORMAL)" +
		"&_pragma=foreign_keys(ON)" +
		"&_pragma=temp_store(MEMORY)" +
		"&_pragma=cache_size(-32000)" +
		"&_pragma=mmap_size(67108864)"
	return dbx.Open("sqlite", dbPath+pragmas)
}

// logSQLitePragmas reads back the active pragma values from a live
// connection so operators can verify the tuning is actually in effect
// at startup (otherwise a typo in qatlasDBConnect would silently fall
// back to SQLite defaults, which are worse than PB defaults). One log
// line per database, structured for easy ingest.
func logSQLitePragmas(ctx context.Context, app core.App) {
	if app == nil {
		return
	}
	// Run a quick checkpoint to truncate any leftover WAL from an
	// unclean prior shutdown. Best-effort: failure is harmless (WAL
	// will just stay long until SQLite's own autocheckpoint fires)
	// and we don't want to fail bootstrap over it.
	walCheckpoint(ctx, app)
	readPragmas(ctx, app, "data.db", app.DB())
	if aux := app.AuxDB(); aux != nil {
		readPragmas(ctx, app, "aux.db", aux)
	}
}

func readPragmas(ctx context.Context, _ core.App, label string, db dbx.Builder) {
	// PRAGMAs we care about, in display order. Each yields one row
	// with a single column whose name matches the pragma. We use
	// SELECT * FROM pragma_xxx so the result column name is stable.
	pragmas := []string{
		"journal_mode",
		"synchronous",
		"busy_timeout",
		"cache_size",
		"mmap_size",
		"journal_size_limit",
		"foreign_keys",
		"temp_store",
		"wal_autocheckpoint",
	}
	attrs := []any{"db", label}
	for _, p := range pragmas {
		var v any
		err := db.NewQuery(fmt.Sprintf("PRAGMA %s", p)).Row(&v)
		if err != nil {
			attrs = append(attrs, p, fmt.Sprintf("error:%s", err.Error()))
			continue
		}
		attrs = append(attrs, p, v)
	}
	slog.Info("sqlite pragmas active", attrs...)
}

// walCheckpoint runs `PRAGMA wal_checkpoint(TRUNCATE)` on the main
// database. PB does its own autocheckpoint based on WAL page count
// (default 1000 pages), but after an unclean shutdown the WAL can
// linger until the next write triggers it. Running TRUNCATE at boot
// gives us a small predictable startup work-package vs. lazy lurking
// disk usage.
func walCheckpoint(ctx context.Context, app core.App) {
	if app == nil || app.DB() == nil {
		return
	}
	start := time.Now()
	// PRAGMA wal_checkpoint returns 3 columns: busy, log, checkpointed.
	// We don't care about the values for logging — the relevant fact
	// is whether the call succeeded, plus how long it took.
	type row struct {
		Busy         int `db:"busy"`
		Log          int `db:"log"`
		Checkpointed int `db:"checkpointed"`
	}
	var r row
	err := app.DB().NewQuery("PRAGMA wal_checkpoint(TRUNCATE)").One(&r)
	dur := time.Since(start)
	if err != nil {
		slog.Warn("sqlite: startup wal_checkpoint failed", "error", err, "duration_ms", dur.Milliseconds())
		return
	}
	slog.Info("sqlite: startup wal_checkpoint complete",
		"checkpointed_frames", r.Checkpointed,
		"duration_ms", dur.Milliseconds(),
	)
}
