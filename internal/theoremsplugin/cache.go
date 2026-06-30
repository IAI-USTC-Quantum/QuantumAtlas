// Package theoremsplugin is the data layer of the builtin theorems plugin
// (ADR 0002): an in-memory, read-through cache of the proved-Theorems catalog
// that the host pulls (server-side `git pull --ff-only`) from an upstream
// Lean-content repo (today agony/qatlas-lean, eventually a content-only
// QuantumAtlas-Theorems repo).
//
// It reads three things straight from the checkout, with NO rename and NO
// schema change (ADR 0002 "the theorems plugin just reads them"):
//
//   - artifacts/registry.json               — the proved-Theorems catalog
//   - artifacts/audit_records/certified.json — the audit ledger
//   - lean/**/*.lean source files            — for source-on-demand
//
// This package carries no HTTP/route code (that lives in internal/routes, next
// to the platform's scope guards); it is a pure data accessor so it stays easy
// to test and so the plugin owns its domain vocabulary end-to-end.
package theoremsplugin

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/gitpull"
)

// Upstream artifact layout (relative to the repo checkout root). Fixed by the
// upstream repo's structure, not configurable.
const (
	registryRelPath  = "artifacts/registry.json"
	certifiedRelPath = "artifacts/audit_records/certified.json"
)

// Theorem is one entry of registry.json's `theorems` list — a machine-checked
// Lean 4 declaration that faithfully formalizes a Claim.
type Theorem struct {
	LeanFQN             string   `json:"lean_fqn"`
	File                string   `json:"file"`
	Kind                string   `json:"kind"` // theorem | lemma | definition | bound
	FamilyID            string   `json:"family_id"`
	StatementParaphrase string   `json:"statement_paraphrase"`
	UnitID              string   `json:"unit_id"`
	SorryFree           bool     `json:"sorry_free"`
	DependsOn           []string `json:"depends_on"`
	AddedTS             string   `json:"added_ts"`
	AxiomsUsed          []string `json:"axioms_used"`
	AuditStatus         string   `json:"audit_status"`
}

// Family is one entry of registry.json's `families` list — a thematic grouping
// of Theorems (foundations, oracular, hamiltonian-simulation, …).
type Family struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

// CertifiedEntry is one entry of certified.json's `certified` map — the audit
// verdict for a Claim (keyed by unit_id).
type CertifiedEntry struct {
	UnitID            string         `json:"unit_id"`
	Result            string         `json:"result"`
	Certified         bool           `json:"certified"`
	FQNs              []string       `json:"fqns"`
	Kernel            string         `json:"kernel"`
	ClaimFaithfulness string         `json:"claim_faithfulness"`
	// Magi is map[string]any (not map[string]int): the audit ledger is an
	// external artifact whose magi block carries the int tribunal counts AND
	// occasionally a free-text "note", so a fixed value type would reject the
	// whole record.
	Magi        map[string]any `json:"magi"`
	AuditedTS   string         `json:"audited_ts"`
	VerdictPath string         `json:"verdict_path"`
}

type registryFile struct {
	Version        int       `json:"version"`
	GeneratedTS    string    `json:"generated_ts"`
	NarrativeIntro string    `json:"narrative_intro"`
	Families       []Family  `json:"families"`
	Theorems       []Theorem `json:"theorems"`
}

type certifiedFile struct {
	Certified map[string]json.RawMessage `json:"certified"`
}

// Stats are the aggregate counts surfaced by /api/theorems/stats.
type Stats struct {
	Total       int            `json:"total"`
	SorryFree   int            `json:"sorry_free"`
	ByKind      map[string]int `json:"by_kind"`
	ByFamily    map[string]int `json:"by_family"`
	ByAudit     map[string]int `json:"by_audit_status"`
	Families    int            `json:"families"`
	Certified   int            `json:"certified"`
	GeneratedTS string         `json:"generated_ts,omitempty"`
}

// Snapshot is one immutable view of the parsed theorems catalog.
type Snapshot struct {
	Theorems    []Theorem
	Families    []Family
	Certified   map[string]CertifiedEntry
	GeneratedTS string
	GitCommit   string
	LoadedAt    time.Time
	byFQN       map[string]int // lean_fqn -> index into Theorems
}

// Cache is the live theorems catalog used by the /api/theorems/* routes.
// Construct with NewCache; call Stop() at shutdown. Mirrors wiki.Cache: a
// single immutable snapshot pointer swapped atomically on refresh, a
// background staleness ticker, and a synchronous Reload() the platform calls
// after a successful `git pull`.
type Cache struct {
	dir          string
	refreshEvery time.Duration

	snap atomic.Pointer[Snapshot]

	muRefresh sync.Mutex

	cancel func()
	done   chan struct{}
}

// NewCache builds the initial snapshot synchronously (so the first request
// after startup hits warm cache) and starts the background refresh ticker. The
// initial load is allowed to fail — the cache stays empty and accessors
// degrade to empty responses until a later Refresh succeeds. Pass
// refreshEvery <= 0 to use the default of 60s.
func NewCache(dir string, refreshEvery time.Duration) *Cache {
	if refreshEvery <= 0 {
		refreshEvery = 60 * time.Second
	}
	c := &Cache{
		dir:          dir,
		refreshEvery: refreshEvery,
		done:         make(chan struct{}),
	}
	if _, err := c.Refresh(true); err != nil {
		slog.Warn("theorems: initial cache load failed", "dir", dir, "error", err)
	}
	stop := make(chan struct{})
	c.cancel = func() { close(stop) }
	go c.loop(stop)
	return c
}

// Stop tears down the background refresh goroutine. Safe to call repeatedly.
func (c *Cache) Stop() {
	if c.cancel == nil {
		return
	}
	c.cancel()
	<-c.done
	c.cancel = nil
}

func (c *Cache) loop(stop <-chan struct{}) {
	defer close(c.done)
	t := time.NewTicker(c.refreshEvery)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			if _, err := c.Refresh(false); err != nil {
				slog.Warn("theorems: background refresh failed", "error", err)
			}
		}
	}
}

// Reload forces a synchronous rebuild. Called by the platform's GitPullPlugin
// hook after a successful `git pull` so the next read sees fresh data.
func (c *Cache) Reload() error {
	_, err := c.Refresh(true)
	return err
}

// Refresh rebuilds the snapshot if git HEAD has moved (or always when
// force=true / the cache is empty). Returns (changed, err).
func (c *Cache) Refresh(force bool) (bool, error) {
	c.muRefresh.Lock()
	defer c.muRefresh.Unlock()

	currentCommit := gitpull.Output(c.dir, "rev-parse", "HEAD")
	cur := c.snap.Load()
	if !force && cur != nil && cur.GitCommit != "" && cur.GitCommit == currentCommit {
		return false, nil
	}

	reg, err := readRegistry(filepath.Join(c.dir, registryRelPath))
	if err != nil {
		return false, err
	}
	certified := readCertified(filepath.Join(c.dir, certifiedRelPath))

	sort.Slice(reg.Theorems, func(i, j int) bool {
		return reg.Theorems[i].LeanFQN < reg.Theorems[j].LeanFQN
	})
	byFQN := make(map[string]int, len(reg.Theorems))
	for i, t := range reg.Theorems {
		byFQN[t.LeanFQN] = i
	}

	snap := &Snapshot{
		Theorems:    reg.Theorems,
		Families:    reg.Families,
		Certified:   certified,
		GeneratedTS: reg.GeneratedTS,
		GitCommit:   currentCommit,
		LoadedAt:    time.Now(),
		byFQN:       byFQN,
	}
	c.snap.Store(snap)
	slog.Info("theorems: cache refreshed",
		"theorems", len(reg.Theorems), "families", len(reg.Families),
		"certified", len(certified), "git_commit", shortCommit(currentCommit))
	return true, nil
}

func readRegistry(path string) (registryFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return registryFile{}, err
	}
	var reg registryFile
	if err := json.Unmarshal(data, &reg); err != nil {
		return registryFile{}, err
	}
	return reg, nil
}

// readCertified is best-effort: a missing/malformed audit ledger degrades to
// an empty map rather than failing the whole refresh (registry.json is the
// source of truth; certified.json is enrichment). Each entry is decoded
// individually so ONE malformed entry can't drop the rest of the ledger.
func readCertified(path string) map[string]CertifiedEntry {
	out := map[string]CertifiedEntry{}
	data, err := os.ReadFile(path)
	if err != nil {
		return out
	}
	var cf certifiedFile
	if err := json.Unmarshal(data, &cf); err != nil {
		slog.Warn("theorems: certified.json parse failed", "path", path, "error", err)
		return out
	}
	for unitID, raw := range cf.Certified {
		var entry CertifiedEntry
		if err := json.Unmarshal(raw, &entry); err != nil {
			slog.Warn("theorems: skipping malformed certified entry", "unit_id", unitID, "error", err)
			continue
		}
		out[unitID] = entry
	}
	return out
}

// ListFilter narrows the theorem listing. Empty fields match everything.
type ListFilter struct {
	FamilyID    string
	AuditStatus string
	Kind        string
}

// List returns cached theorems matching f (sorted by lean_fqn), or an empty
// slice if no snapshot has loaded.
func (c *Cache) List(f ListFilter) []Theorem {
	snap := c.snap.Load()
	if snap == nil {
		return []Theorem{}
	}
	out := make([]Theorem, 0, len(snap.Theorems))
	for _, t := range snap.Theorems {
		if f.FamilyID != "" && t.FamilyID != f.FamilyID {
			continue
		}
		if f.AuditStatus != "" && t.AuditStatus != f.AuditStatus {
			continue
		}
		if f.Kind != "" && t.Kind != f.Kind {
			continue
		}
		out = append(out, t)
	}
	return out
}

// Find returns the theorem with the given lean_fqn, or false.
func (c *Cache) Find(fqn string) (Theorem, bool) {
	snap := c.snap.Load()
	if snap == nil {
		return Theorem{}, false
	}
	i, ok := snap.byFQN[fqn]
	if !ok {
		return Theorem{}, false
	}
	return snap.Theorems[i], true
}

// CertifiedFor returns the audit-ledger entry for a unit_id, or false.
func (c *Cache) CertifiedFor(unitID string) (CertifiedEntry, bool) {
	snap := c.snap.Load()
	if snap == nil || unitID == "" {
		return CertifiedEntry{}, false
	}
	e, ok := snap.Certified[unitID]
	return e, ok
}

// Families returns the cached family definitions.
func (c *Cache) Families() []Family {
	snap := c.snap.Load()
	if snap == nil {
		return []Family{}
	}
	return snap.Families
}

// Stats returns the aggregate counts.
func (c *Cache) Stats() Stats {
	snap := c.snap.Load()
	st := Stats{
		ByKind:   map[string]int{},
		ByFamily: map[string]int{},
		ByAudit:  map[string]int{},
	}
	if snap == nil {
		return st
	}
	st.Total = len(snap.Theorems)
	st.Families = len(snap.Families)
	st.Certified = len(snap.Certified)
	st.GeneratedTS = snap.GeneratedTS
	for _, t := range snap.Theorems {
		if t.SorryFree {
			st.SorryFree++
		}
		if t.Kind != "" {
			st.ByKind[t.Kind]++
		}
		if t.FamilyID != "" {
			st.ByFamily[t.FamilyID]++
		}
		if t.AuditStatus != "" {
			st.ByAudit[t.AuditStatus]++
		}
	}
	return st
}

// ErrSourceUnavailable family — returned by Source.
type SourceError struct{ Detail string }

func (e *SourceError) Error() string { return e.Detail }

// Source returns the Lean source file backing a theorem (by lean_fqn). The
// file path comes from the registry entry and is validated to stay within the
// checkout root (defence-in-depth against a poisoned registry.json with a
// `..`-escaping path). Returns the relative path and full file text.
func (c *Cache) Source(fqn string) (relPath string, src string, err error) {
	t, ok := c.Find(fqn)
	if !ok {
		return "", "", &SourceError{Detail: "theorem not found: " + fqn}
	}
	if strings.TrimSpace(t.File) == "" {
		return "", "", &SourceError{Detail: "theorem has no source file: " + fqn}
	}
	clean := filepath.Clean(t.File)
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) || filepath.IsAbs(clean) {
		return "", "", &SourceError{Detail: "invalid source path"}
	}
	full := filepath.Join(c.dir, clean)
	// Confirm the resolved path is inside the checkout root.
	rel, relErr := filepath.Rel(c.dir, full)
	if relErr != nil || strings.HasPrefix(rel, "..") {
		return "", "", &SourceError{Detail: "invalid source path"}
	}
	data, readErr := os.ReadFile(full)
	if readErr != nil {
		return "", "", &SourceError{Detail: "source file unavailable: " + clean}
	}
	return clean, string(data), nil
}

// Ready reports whether at least one snapshot has loaded.
func (c *Cache) Ready() bool {
	return c.snap.Load() != nil
}

// Snapshot returns the current snapshot pointer (or nil).
func (c *Cache) Snapshot() *Snapshot {
	return c.snap.Load()
}

func shortCommit(s string) string {
	if len(s) < 7 {
		return s
	}
	return s[:7]
}
