// Package healthz aggregates liveness + dependency probes for the
// /api/health endpoint.
//
// # Why a separate package
//
// The /api/health route used to be PocketBase's built-in one-liner.
// Adding dependency probes (RustFS bucket existence, Neo4j Bolt
// connectivity, wiki git HEAD commit time) blows the handler past
// "comfortable inline size" and forces us to import minio + neo4j
// drivers from main, which couples the binary entrypoint to backend
// SDKs that should stay confined to the route layer. Centralising the
// probes here keeps main.go thin and makes the checks individually
// unit-testable.
//
// # Response shape
//
// We deliberately return HTTP 200 with a structured payload regardless
// of dependency state. This matches the rest of the QuantumAtlas API
// (graph endpoints return 200 + {"error":...} when Neo4j is down) and
// avoids the trap where a monitor flips alarms because a single
// downstream is briefly unreachable while the server itself is fully
// up. Operators that want fail-on-degraded should match on the
// `data.status` field in the body.
//
// status values:
//
//   - "healthy"   — every configured dependency responded.
//   - "degraded"  — server up, but one or more configured dependencies
//                   failed their probe. Caller-visible APIs may still
//                   work in fallback mode (e.g. local raw store).
//   - "not_configured" appears per-check when a dependency is optional
//                   and the operator hasn't enabled it (e.g. Neo4j
//                   without NEO4J_URI). Not_configured checks do NOT
//                   downgrade the aggregate status.
package healthz

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/config"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/neo4j"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/objstore"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/safego"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/wiki"
)

// Check is a single dependency probe result. LatencyMS is the wall
// time of the underlying call (in milliseconds) when applicable;
// omitted when the check was skipped (not_configured).
type Check struct {
	Status    string `json:"status"`              // "ok" | "error" | "not_configured"
	Error     string `json:"error,omitempty"`     // populated when Status="error"
	LatencyMS int64  `json:"latency_ms,omitempty"`

	// Optional per-check fields. Different checks fill different
	// subsets — we keep them on one struct so the JSON layout stays
	// uniform and the SPA doesn't need a discriminated union.
	Backend    string `json:"backend,omitempty"`     // raw store: "s3" | "s3-router" | "local"
	Endpoint   string `json:"endpoint,omitempty"`    // s3 endpoint URL
	Bucket     string `json:"bucket,omitempty"`      // s3 bucket name
	Buckets    []string `json:"buckets,omitempty"`   // s3-router: probed bucket names
	URI        string `json:"uri,omitempty"`         // neo4j bolt URI
	Database   string `json:"database,omitempty"`    // neo4j database
	Dir        string `json:"dir,omitempty"`         // wiki working tree path
	Commit     string `json:"commit,omitempty"`      // wiki HEAD short SHA
	CommitTime string `json:"commit_time,omitempty"` // wiki HEAD commit ISO 8601
	Branch     string `json:"branch,omitempty"`      // wiki branch
	Dirty      *bool  `json:"dirty,omitempty"`       // wiki worktree dirty flag
}

// Result is the full /api/health response payload.
//
// Aggregate Status is "healthy" when every check in Checks is either
// "ok" or "not_configured"; "degraded" otherwise. The server itself
// is implicitly "ok" — if we couldn't serve the request the client
// wouldn't be reading this struct.
type Result struct {
	Status        string           `json:"status"`
	Version       string           `json:"version"`
	UptimeSeconds int64            `json:"uptime_seconds"`
	Time          string           `json:"time"`
	Checks        map[string]Check `json:"checks"`
}

// PBResult wraps Result into PocketBase's standard /api/health envelope
// shape `{code, message, data}`. We override PocketBase's built-in
// `/api/health` handler with this so existing PocketBase JS / Dart
// SDK clients (which call `pb.health.check()` and expect that
// envelope) keep working, while operators and monitoring see our
// dependency-aware fields nested inside `data`.
//
// `message` flips from the PocketBase default "API is healthy." to
// "Dependency degraded." when at least one configured probe is in
// error — useful for log scrapers that only sample the top-level
// string. `code` is always 200 to stay consistent with the project's
// "HTTP status is for transport-layer health, body is the truth"
// convention; flipping to 503 would force Caddy reverse_proxy +
// any upstream LB to treat us as down even when the server itself
// is fully serving traffic.
type PBResult struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    Result `json:"data"`
}

// Probes bundles the live backends the checker needs. We pass them in
// rather than reach into globals so unit tests can substitute fakes
// (especially RawStore — wiring a fake S3 server in-process is more
// pain than it's worth).
//
// RawStore is the objstore.Store the server is using. Its concrete
// type determines what we report: *objstore.S3Store → real BucketExists
// probe; anything else → "local" backend treated as always-ok (the
// local fs is the process's own filesystem; failure would have killed
// PocketBase startup already).
type Probes struct {
	Cfg      *config.Config
	RawStore objstore.Store
	Version  string
	Started  time.Time
}

// probeTimeout caps each individual dependency probe so a slow
// downstream can't make /api/health hang. 5 s is long enough for a
// Bolt handshake over WAN and short enough that monitoring stays
// responsive.
const probeTimeout = 5 * time.Second

// RunPB is Run wrapped into the PocketBase /api/health envelope. See
// PBResult for the rationale (PocketBase SDK compatibility + ops
// readability). Use this from the handler that overrides PocketBase's
// built-in /api/health; use Run directly only if you need the bare
// Result for a different surface.
func RunPB(ctx context.Context, p Probes) PBResult {
	r := Run(ctx, p)
	msg := "API is healthy."
	if r.Status != "healthy" {
		msg = "Dependency degraded."
	}
	return PBResult{
		Code:    200,
		Message: msg,
		Data:    r,
	}
}

// Run executes every probe in parallel and returns the aggregated
// Result. The provided ctx caps total wall time; individual probes
// use a derived context with probeTimeout, so a single slow probe
// doesn't bleed into the others.
//
// Safe to call concurrently — each invocation builds its own neo4j
// driver (matching the per-request lifecycle used elsewhere) and the
// S3 / wiki probes are read-only.
func Run(ctx context.Context, p Probes) Result {
	now := time.Now().UTC()
	res := Result{
		Version:       p.Version,
		UptimeSeconds: int64(now.Sub(p.Started).Seconds()),
		Time:          now.Format(time.RFC3339),
		Checks:        make(map[string]Check, 3),
	}

	var (
		mu sync.Mutex
		wg sync.WaitGroup
	)
	set := func(name string, c Check) {
		mu.Lock()
		res.Checks[name] = c
		mu.Unlock()
	}

	wg.Add(3)

	// Each probe goroutine wraps its body in a recover() so a panic
	// inside a probe (e.g. an SDK bug, a nil-deref) is logged and the
	// caller's WaitGroup still completes — otherwise /api/health would
	// hang the request goroutine forever, which would also leak the
	// connection from the HTTP pool.
	probe := func(name string, fn func() Check) {
		defer wg.Done()
		defer func() {
			if r := recover(); r != nil {
				safego.LogPanic("healthz."+name, r)
				set(name, Check{Status: "error", Error: "probe panic (see server log)"})
			}
		}()
		set(name, fn())
	}

	go probe("rawstore", func() Check { return probeRawStore(ctx, p.RawStore) })
	go probe("neo4j", func() Check { return probeNeo4j(ctx, p.Cfg) })
	go probe("wiki", func() Check { return probeWiki(p.Cfg) })

	wg.Wait()

	res.Status = aggregate(res.Checks)
	return res
}

// aggregate downgrades to "degraded" the moment any check reports
// "error". "not_configured" is treated as a deliberate skip — it
// doesn't pull the overall status down.
func aggregate(checks map[string]Check) string {
	for _, c := range checks {
		if c.Status == "error" {
			return "degraded"
		}
	}
	return "healthy"
}

func probeRawStore(ctx context.Context, store objstore.Store) Check {
	if store == nil {
		// Shouldn't happen — initRawStore is fatal on failure — but
		// be defensive so a future refactor can't crash /api/health.
		return Check{Status: "error", Error: "raw store not initialised"}
	}

	s3, ok := store.(*objstore.S3Store)
	if !ok {
		// Multi-bucket Router (v0.7.0 default for S3 deployments):
		// probe each distinct S3 backend, aggregate to the worst
		// status so a single broken bucket surfaces as degraded.
		if r, isRouter := store.(*objstore.Router); isRouter {
			return probeRouter(ctx, r)
		}
		// LocalStore (filesystem-backed). The process owns the
		// directory, so a probe would be testing our own filesystem
		// — if that's broken the server isn't actually serving.
		return Check{
			Status:  "ok",
			Backend: "local",
		}
	}

	c := Check{
		Backend:  "s3",
		Endpoint: s3.EndpointURL(),
		Bucket:   s3.Bucket(),
	}
	pctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	start := time.Now()
	exists, err := s3.Ping(pctx)
	c.LatencyMS = time.Since(start).Milliseconds()
	switch {
	case err != nil:
		c.Status = "error"
		c.Error = err.Error()
	case !exists:
		// Credentials work but the bucket isn't there — treat as
		// error because uploads will fail.
		c.Status = "error"
		c.Error = "bucket does not exist"
	default:
		c.Status = "ok"
	}
	return c
}

// probeRouter HEAD-checks every distinct S3 bucket behind a multi-bucket
// Router. Any bucket failing (connectivity or missing) downgrades the
// aggregate to "error"; latency is the max over probed backends.
func probeRouter(ctx context.Context, r *objstore.Router) Check {
	backends := r.S3Backends()
	c := Check{Backend: "s3-router"}
	if len(backends) == 0 {
		return Check{Status: "ok", Backend: "local"}
	}
	c.Endpoint = backends[0].EndpointURL()
	c.Status = "ok"
	for _, s3 := range backends {
		c.Buckets = append(c.Buckets, s3.Bucket())
		pctx, cancel := context.WithTimeout(ctx, probeTimeout)
		start := time.Now()
		exists, err := s3.Ping(pctx)
		cancel()
		if ms := time.Since(start).Milliseconds(); ms > c.LatencyMS {
			c.LatencyMS = ms
		}
		switch {
		case err != nil:
			c.Status = "error"
			c.Error = fmt.Sprintf("bucket %s: %v", s3.Bucket(), err)
		case !exists:
			c.Status = "error"
			c.Error = fmt.Sprintf("bucket %s does not exist", s3.Bucket())
		}
		if c.Status == "error" {
			break
		}
	}
	return c
}

func probeNeo4j(ctx context.Context, cfg *config.Config) Check {
	if cfg == nil || cfg.Neo4jURI == "" {
		return Check{Status: "not_configured"}
	}
	c := Check{
		URI:      cfg.Neo4jURI,
		Database: cfg.Neo4jDatabase,
	}
	pctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	start := time.Now()
	client, err := neo4j.NewClient(cfg.Neo4jURI, cfg.Neo4jUser, cfg.Neo4jPassword, cfg.Neo4jDatabase)
	if err != nil {
		c.LatencyMS = time.Since(start).Milliseconds()
		c.Status = "error"
		c.Error = err.Error()
		return c
	}
	defer client.Close(pctx)
	if err := client.Connect(pctx); err != nil {
		c.LatencyMS = time.Since(start).Milliseconds()
		c.Status = "error"
		c.Error = err.Error()
		return c
	}
	c.LatencyMS = time.Since(start).Milliseconds()
	c.Status = "ok"
	return c
}

func probeWiki(cfg *config.Config) Check {
	if cfg == nil || cfg.WikiDir == "" {
		return Check{Status: "not_configured"}
	}
	info := wiki.ReadGitInfo(cfg.WikiDir)
	c := Check{Dir: cfg.WikiDir}
	if !info.Enabled {
		c.Status = "error"
		c.Error = "wiki directory is not a git repository"
		return c
	}
	c.Status = "ok"
	c.Commit = info.Commit
	c.CommitTime = info.CommitTime
	c.Branch = info.Branch
	c.Dirty = info.Dirty
	return c
}
