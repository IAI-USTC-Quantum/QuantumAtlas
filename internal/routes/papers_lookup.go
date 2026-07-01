package routes

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/openalex"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/openalexcorpus"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/papers"

	"github.com/pocketbase/pocketbase/core"
)

// paperLookupHandler answers GET /api/papers/lookup?ids=arxiv:…,openalex:…,doi:…
// (ADR 0007): a batch, EXACT, by-id resolver over the local OpenAlex corpus
// (ADR 0006). Each ref is a namespaced, unversioned `kind:id` string. The
// response is per-ref `{ref, title, authors, year, hosted, resolved}`:
//
//   - hosted   — is this ref a Paper QuantumAtlas hosts (join vs papers)?
//   - resolved — did the OpenAlex corpus have it? false (not an error) for ids
//     the corpus doesn't hold; the whole batch never errors on one miss.
//
// corpus_available signals whether the OpenAlex corpus backend answered. When
// it is false the qatlas client may fall back to the public OpenAlex API for
// this session (graceful degradation, mirroring ADR 0006).
//
// Fuzzy free-text search is a deliberately separate, deferred capability — this
// endpoint is exact-by-id only.
const maxLookupRefs = 200

type lookupResult struct {
	Ref      string   `json:"ref"`
	Title    string   `json:"title,omitempty"`
	Authors  []string `json:"authors,omitempty"`
	Year     int      `json:"year,omitempty"`
	Hosted   bool     `json:"hosted"`
	Resolved bool     `json:"resolved"`
}

func paperLookupHandler(re *core.RequestEvent, catalog *papers.Store, corpus *openalexcorpus.Store, resolver *openalex.Resolver) error {
	refs := parseLookupIDs(re.Request.URL.Query().Get("ids"))
	if len(refs) == 0 {
		return re.JSON(http.StatusBadRequest, map[string]string{
			"detail": "ids query param required: comma-separated namespaced kind:id (arxiv:… / openalex:… / doi:…)",
		})
	}

	ctx := re.Request.Context()
	corpusAvailable := corpus != nil && corpus.Available(ctx)
	// Lazy fetch-on-miss (ADR 0006): the corpus is a write-through cache, so
	// a by-id miss can be filled from the public OpenAlex API and written
	// back. Only when the corpus is reachable (somewhere to write) and a
	// resolver is configured (mailto set).
	lazyFetch := corpusAvailable && resolver != nil && resolver.Enabled()

	results := make([]lookupResult, 0, len(refs))
	for _, ref := range refs {
		r := lookupResult{Ref: ref}
		kind, id, ok := splitRef(ref)
		if !ok {
			// Unknown / malformed namespace — echo it back unresolved rather
			// than erroring the whole batch.
			results = append(results, r)
			continue
		}

		r.Hosted = catalogHosted(ctx, catalog, kind, id)

		if corpusAvailable {
			work, found := corpusResolve(ctx, corpus, kind, id)
			if !found && lazyFetch {
				// Cache miss: fetch live + write back so the next read is local.
				work, found = fetchOnMiss(ctx, corpus, resolver, kind, id)
			}
			if found {
				r.Title = strings.TrimSpace(work.Title)
				r.Authors = openalex.AuthorNames(work)
				r.Year = yearFromPubDate(work.PublicationDate)
				r.Resolved = true
			}
		}
		results = append(results, r)
	}

	return re.JSON(http.StatusOK, map[string]any{
		"results":          results,
		"corpus_available": corpusAvailable,
	})
}

// fetchOnMiss fills a corpus miss from the public OpenAlex API and writes the
// record back (ADR 0006 write-through cache). Only "openalex" and "doi" refs
// map to a /works/{id} GET; "arxiv" is not such a key, so it stays a
// corpus-only lookup (found=false here). Any fetch / write error degrades to
// found=false — a miss is never an error for the batch. On a successful fetch
// the work is returned even if the write-back fails (the read still resolves).
func fetchOnMiss(ctx context.Context, corpus *openalexcorpus.Store, resolver *openalex.Resolver, kind, id string) (openalex.Work, bool) {
	if kind != "openalex" && kind != "doi" {
		return openalex.Work{}, false
	}
	raw, work, err := resolver.FetchWorkRecord(ctx, kind, id)
	if err != nil {
		return openalex.Work{}, false
	}
	if err := corpus.UpsertFetchedWork(ctx, raw, work); err != nil {
		slog.Warn("lookup: lazy corpus write-back failed", "kind", kind, "id", id, "error", err)
		// Still return the fetched work — the read resolves even if caching failed.
	}
	return work, true
}

// parseLookupIDs splits the comma-separated ids param, trims, drops empties,
// de-dupes (preserving order), and caps the batch size.
func parseLookupIDs(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	seen := map[string]bool{}
	out := make([]string, 0, 8)
	for _, part := range strings.Split(raw, ",") {
		p := strings.TrimSpace(part)
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
		if len(out) >= maxLookupRefs {
			break
		}
	}
	return out
}

// splitRef parses a `kind:id` reference into (kind, id). Returns ok=false for
// an unknown namespace or a missing id. The split is on the FIRST colon so DOI
// ids (which contain no colon) and old-style arxiv ids (which contain a slash,
// not a colon) survive intact.
func splitRef(ref string) (kind, id string, ok bool) {
	i := strings.IndexByte(ref, ':')
	if i <= 0 || i == len(ref)-1 {
		return "", "", false
	}
	kind = strings.ToLower(strings.TrimSpace(ref[:i]))
	id = strings.TrimSpace(ref[i+1:])
	switch kind {
	case "arxiv", "doi", "openalex":
		return kind, id, true
	default:
		return "", "", false
	}
}

// corpusResolve fetches a work from the OpenAlex corpus by ref kind and decodes
// the subset (title, authors, publication_date) used by lookup. found=false on
// a corpus miss OR any corpus error (the batch degrades gracefully per ref).
func corpusResolve(ctx context.Context, corpus *openalexcorpus.Store, kind, id string) (openalex.Work, bool) {
	var (
		raw   json.RawMessage
		found bool
		err   error
	)
	switch kind {
	case "openalex":
		raw, found, err = corpus.GetWork(ctx, id)
	case "doi":
		raw, found, err = corpus.GetWorkByDOI(ctx, id)
	case "arxiv":
		// The corpus has no by-arxiv primary key, but exposes an arxiv_id
		// filter on the public QueryWorks API.
		res, qerr := corpus.QueryWorks(ctx, openalexcorpus.QueryOptions{
			Filters: []openalexcorpus.Filter{{Key: "arxiv_id", Value: stripArxivVersion(id)}},
			PerPage: 1,
		})
		err = qerr
		if qerr == nil && len(res.Results) > 0 {
			raw, found = res.Results[0], true
		}
	}
	if err != nil || !found {
		return openalex.Work{}, false
	}
	var work openalex.Work
	if jsonErr := json.Unmarshal(raw, &work); jsonErr != nil {
		return openalex.Work{}, false
	}
	return work, true
}

// catalogHosted reports whether a ref corresponds to a papers row (a Paper
// QuantumAtlas hosts). Best-effort: a catalog miss or unavailability is "not
// hosted", never an error for the batch.
func catalogHosted(ctx context.Context, catalog *papers.Store, kind, id string) bool {
	if catalog == nil {
		return false
	}
	scheme, value := kind, id
	if kind == "arxiv" {
		value = stripArxivVersion(id)
	}
	hosted, err := catalog.IsHosted(ctx, scheme, value)
	return err == nil && hosted
}

var arxivVersionRE = regexp.MustCompile(`v\d+$`)

// stripArxivVersion removes a trailing version suffix (References cite a work,
// not a snapshot — ADR 0007 — so refs are unversioned, but we strip defensively).
func stripArxivVersion(id string) string {
	return arxivVersionRE.ReplaceAllString(id, "")
}

// yearFromPubDate parses the leading YYYY of an OpenAlex publication_date
// ("2022-06-15"). Returns 0 when absent / unparseable.
func yearFromPubDate(date string) int {
	date = strings.TrimSpace(date)
	if len(date) < 4 {
		return 0
	}
	y, err := strconv.Atoi(date[:4])
	if err != nil {
		return 0
	}
	return y
}
