package routes

// OpenAPI / Swagger annotations.
//
// PocketBase registers routes as closures on se.Router (see RegisterSearch,
// RegisterPAT, RegisterPapers and the
// closures in cmd/qatlasd/main.go). swaggo/swag can only attach an
// operation to a Go function's doc comment, and most of our handlers are
// anonymous closures with no addressable declaration. Rather than refactor
// every route into a named function purely to host a comment, we keep all
// operation declarations here as documented no-op stubs — the swaggo-
// sanctioned "declarative comments live anywhere" pattern.
//
// These stubs carry NO logic; they exist solely so `swag init` can emit the
// path entries. The Go CI generate-and-diff check (see docs/contributing.md)
// regenerates internal/apidocs and fails if these
// annotations and the committed spec disagree, so the spec can never silently
// fall behind the annotations. Keeping the annotations correct relative to the
// actual handler behavior remains a review-time discipline (true of swaggo on
// any router — it never introspects handler bodies).
//
// Each stub is grouped by @Tags matching its source file. When you add or
// change a route, update the matching stub here and run `go tool swag init`
// with the arguments in docs/contributing.md.

// --- System -----------------------------------------------------------------

// healthCheck reports server liveness plus dependency probes.
//
// @Summary     Health check
// @Description Liveness plus parallel dependency probes (rawstore, postgres,
// @Description registry). HTTP status is always 200; read data.status for the
// @Description real verdict ("healthy" | "degraded").
// @Tags        System
// @Produce     json
// @Success     200 {object} map[string]interface{}
// @Router      /api/health [get]
func docHealthCheck() {}

// serverInfo returns the server mode, version, engine and capabilities.
//
// @Summary     Server info
// @Description Capability discovery: mode / version / engine plus a
// @Description capabilities block — paper_access, markdown_delivery,
// @Description pdf_delivery (always false — the /pdf endpoint answers
// @Description 410 by design), agentic_search, and a nested mineru
// @Description object (enabled / on_demand). Privacy mirrors /api/health:
// @Description anonymous callers see the booleans only; authenticated
// @Description callers (system PAT or session) additionally get
// @Description mineru.daily_cap and mineru.converted_today from the
// @Description batch-scheduler snapshot. Note the quota semantics:
// @Description daily_cap self-limits the nightly BATCH scheduler
// @Description (default 4000/day, reserving headroom for interactive
// @Description traffic); on-demand conversions triggered by GET
// @Description /markdown are NOT counted against it — they only share
// @Description the upstream per-token daily quota.
// @Tags        System
// @Produce     json
// @Success     200 {object} map[string]interface{}
// @Router      /api/server/info [get]
func docServerInfo() {}

// listPlugins returns discovered plugin manifests and runtime status.
//
// @Summary     List plugins
// @Tags        Plugins
// @Produce     json
// @Security    BearerAuth
// @Success     200 {object} map[string]interface{}
// @Failure     401 {object} map[string]string
// @Failure     403 {object} map[string]string
// @Router      /api/v1/plugins [get]
func docListPlugins() {}

// enablePlugin enables a configured plugin at runtime.
//
// @Summary     Enable plugin
// @Tags        Plugins
// @Produce     json
// @Security    BearerAuth
// @Param       id path string true "plugin id"
// @Success     200 {object} map[string]interface{}
// @Failure     401 {object} map[string]string
// @Failure     403 {object} map[string]string
// @Failure     404 {object} map[string]string
// @Router      /api/v1/plugins/{id}/enable [post]
func docEnablePlugin() {}

// disablePlugin disables a configured plugin at runtime.
//
// @Summary     Disable plugin
// @Tags        Plugins
// @Produce     json
// @Security    BearerAuth
// @Param       id path string true "plugin id"
// @Success     200 {object} map[string]interface{}
// @Failure     401 {object} map[string]string
// @Failure     403 {object} map[string]string
// @Failure     404 {object} map[string]string
// @Router      /api/v1/plugins/{id}/disable [post]
func docDisablePlugin() {}

// docInstallScript serves the POSIX-sh installer for the qatlasd binary.
//
// @Summary     Installer script
// @Description Returns a POSIX sh script (text/x-shellscript) that downloads
// @Description the latest qatlasd release binary.
// @Tags        System
// @Produce     plain
// @Success     200 {string} string "shell script"
// @Router      /install-qatlasd.sh [get]
func docInstallScript() {}

// --- Search ------------------------------------------------------------------

// searchPapers runs a multi-provider paper search.
//
// @Summary     Search papers
// @Description Fans one search entry out to the configured providers
// @Description (catalog / arxiv / openalex / remote), merges hits by paper
// @Description identity (DOI > arXiv > title hash) and resolve-or-mints each
// @Description identity-anchored hit against the paper registry. Newly minted
// @Description papers carry created=true as metadata only: search does not
// @Description download papers or submit conversion tasks. Select identifiers
// @Description explicitly via POST /api/downloader/fetch. Title-only hits return as un-minted
// @Description candidates. The entry may carry an identity (arxiv_id / doi)
// @Description instead of free text — identity fields are forwarded to the
// @Description remote provider for identity-aware lookups; an entry with
// @Description none of text / title / arxiv_id / doi is a 400. Each minted
// @Description result also carries its hosting summary: has_md / has_pdf /
// @Description status from the registry default asset (omitted when the
// @Description registry is unavailable). Requires the papers:read scope.
// @Tags        Search
// @Accept      json
// @Produce     json
// @Security    BearerAuth
// @Param       body body object true "search entry {text?, title?, doi?, arxiv_id?, max_results?, required_phrases?}"
// @Success     200 {object} map[string]interface{} "{results:[{paper_id,hit,created,has_md?,has_pdf?,status?}], candidates:[...]}"
// @Failure     400 {object} map[string]string "invalid JSON or empty search entry"
// @Failure     401 {object} map[string]string
// @Failure     403 {object} map[string]string
// @Failure     503 {object} map[string]string "registry unavailable"
// @Router      /api/search [post]
func docSearchPapers() {}

// searchAgentic runs the metered agentic (LLM-conclusion) search.
//
// @Summary     Agentic search (metered)
// @Description One multi-source search through the agentic backend
// @Description (remote qatlas-search microservice or the local runner),
// @Description optionally with an LLM conclusion ({"agent": false} skips
// @Description it). The body is a search entry plus "agent" and "sources"
// @Description extensions; identity fields (arxiv_id / doi / title) are
// @Description forwarded so identity-only entries run identity lookups
// @Description instead of an empty query — an entry with none of text /
// @Description title / arxiv_id / doi is a 400 (before metering). Every
// @Description call is metered per user per day (usage block); a failed
// @Description upstream is refunded. Results mirror POST /api/search,
// @Description including the has_md / has_pdf / status hosting summary
// @Description on minted results. Anchoring is metadata-only; downloads require
// @Description explicit POST /api/downloader/fetch. Requires the papers:read scope plus a
// @Description user-bound credential (system PATs get 403).
// @Tags        Search
// @Accept      json
// @Produce     json
// @Security    BearerAuth
// @Param       body body object true "{text?, title?, doi?, arxiv_id?, max_results?, agent?, sources?}"
// @Success     200 {object} map[string]interface{} "{results:[{paper_id,hit,created,has_md?,has_pdf?,status?}], candidates:[...], conclusion, usage, errors}"
// @Failure     400 {object} map[string]string "invalid JSON or empty search entry"
// @Failure     401 {object} map[string]string
// @Failure     403 {object} map[string]string "system PAT (no user to meter)"
// @Failure     429 {object} map[string]interface{} "daily agentic limit reached"
// @Failure     502 {object} map[string]string "upstream failed (quota refunded)"
// @Failure     503 {object} map[string]string "no backend configured / registry unavailable"
// @Router      /api/search/agentic [post]
func docSearchAgentic() {}

// @Summary     Scoring language capabilities
// @Description Remote-only DSL features, limits and generation_available. Browser session required.
// @Tags        Search
// @Produce     json
// @Security    BearerAuth
// @Success     200 {object} map[string]interface{}
// @Failure     401 {object} map[string]interface{}
// @Failure     503 {object} map[string]interface{}
// @Router      /api/search/scoring/capabilities [get]
func docScoringCapabilities() {}

// @Summary     Generate a restricted scorer from natural language (metered)
// @Description Requires papers:read and a user-bound credential. Generates and compiles a qatlas-expr-v1 scorer without searching or ingesting. One successful generation consumes one shared Agentic daily unit; failures refund the unit while recording known token usage. At most one internal repair.
// @Tags        Search
// @Accept      json
// @Produce     json
// @Security    BearerAuth
// @Param       body body object true "{query:string, requirements:string}; each 1-4000 Unicode characters"
// @Success     200 {object} map[string]interface{} "{scorer,summary,warnings,scorer_hash,feature_version,usage:{today,limit,llm_tokens}}"
// @Failure     400 {object} map[string]interface{}
// @Failure     401 {object} map[string]interface{}
// @Failure     403 {object} map[string]interface{}
// @Failure     413 {object} map[string]interface{}
// @Failure     422 {object} map[string]interface{}
// @Failure     429 {object} map[string]interface{}
// @Failure     502 {object} map[string]interface{}
// @Failure     503 {object} map[string]interface{}
// @Failure     504 {object} map[string]interface{}
// @Router      /api/search/scoring/generate [post]
func docScoringGenerate() {}

// @Summary     Search using a confirmed custom scorer
// @Description Search anchors metadata only; downloading selected identifiers requires POST /api/downloader/fetch.
// @Description Remote-only fused search with ranking=scorer and agent=false. Requires papers:read. Recompiles the supplied program on every request. No LLM calls. Preserves one globally ordered hit list including title-only candidates and custom score explanations; only returned identity-anchored hits are resolve-or-minted. Scores are neither normalized nor probabilities.
// @Tags        Search
// @Accept      json
// @Produce     json
// @Security    BearerAuth
// @Param       body body object true "{text:string,sources:[string],max_results?:1..100,scorer:{language:qatlas-expr-v1,filter?:string,score:string},explain?:boolean}"
// @Success     200 {object} map[string]interface{} "{hits:[...],ranking,usage,errors,remote:true}"
// @Failure     400 {object} map[string]interface{}
// @Failure     401 {object} map[string]interface{}
// @Failure     403 {object} map[string]interface{}
// @Failure     413 {object} map[string]interface{}
// @Failure     422 {object} map[string]interface{}
// @Failure     429 {object} map[string]interface{}
// @Failure     502 {object} map[string]interface{}
// @Failure     503 {object} map[string]interface{}
// @Router      /api/search/ranked [post]
func docSearchRanked() {}

// multiSearch runs the per-backend ("multi") search.
//
// @Summary     Multi-backend search (per-platform raw results)
// @Description Proxies one mode="multi" call to the qatlas-search
// @Description microservice: every requested backend returns its own raw
// @Description hit list (the source's own order), with NO cross-backend
// @Description merge or ranking. The caller's stored third-party API keys
// @Description (configured in the dashboard) are decrypted and forwarded so
// @Description key-requiring backends run under the user's credentials.
// @Description Provide text or doi (at least one nonblank). A DOI-only request
// @Description forwards doi with an empty query for identity-aware lookup;
// @Description nonempty text retains text-search semantics when both are supplied.
// @Description Identity-anchored hits (DOI / arXiv id) are resolve-or-
// @Description minted as metadata only before the response, without downloads
// @Description or conversion tasks, and carry the server-side
// @Description enrichment paper_id / created / has_md / status (the
// @Description hosting summary is omitted when the registry is
// @Description unavailable). Title-only hits — no DOI, no arXiv id — are
// @Description never minted and carry none of these fields.
// @Description Requires the papers:read scope; 503 when search.remote is
// @Description disabled; 502 when the microservice call fails.
// @Tags        Search
// @Accept      json
// @Produce     json
// @Security    BearerAuth
// @Param       body body object true "{text?, doi?, max_results?, sources: [backend names]}; text or doi is required"
// @Success     200 {object} map[string]interface{} "{results: {backend: [{title, authors?, year?, doi?, arxiv_id?, url?, venue?, citations?, source, score, paper_id?, created?, has_md?, status?}]}, usage, errors: {backend: msg}, remote: true}"
// @Failure     400 {object} map[string]string
// @Failure     401 {object} map[string]string
// @Failure     403 {object} map[string]string
// @Failure     502 {object} map[string]string "upstream failed"
// @Failure     503 {object} map[string]string "search.remote disabled"
// @Router      /api/search/multi [post]
func docMultiSearch() {}

// searchBackends returns the selectable search backend catalog.
//
// @Summary     List search backends
// @Description The backend catalog for the search page's backend picker:
// @Description the static table merged with the qatlas-search
// @Description microservice's live availability (server_ready) and the
// @Description caller's stored API keys (key_configured). selectable =
// @Description server_ready || (user_key && key_configured) — key-requiring
// @Description backends without a stored key render disabled. Session-only
// @Description (key_configured is user-private).
// @Tags        Search
// @Produce     json
// @Security    BearerAuth
// @Success     200 {object} map[string]interface{} "{remote, keys_enabled, backends: [{name, label, category, requires_key, user_key, server_ready, key_configured, selectable}]}"
// @Failure     401 {object} map[string]string
// @Failure     403 {object} map[string]string
// @Router      /api/search/backends [get]
func docSearchBackends() {}

// downloaderFetch submits identifiers to the robust downloader.
//
// @Summary     Robust download (enqueue)
// @Description Parses each input line (DOI, arXiv id, or paper URL),
// @Description resolve-or-mints it into the registry and enqueues the
// @Description multi-paradigm acquisition ladder (arXiv direct → OA
// @Description APIs → publisher URL patterns → landing page → LLM
// @Description agent fallback). Every fetched body is validated (%PDF-
// @Description magic, trailer, size bounds, HTML-disguise classification)
// @Description before it is stored and the MinerU pipeline is triggered.
// @Description Track progress via GET /api/downloader/jobs or the paper
// @Description detail acquisition block. Requires papers:write.
// @Tags        Downloader
// @Accept      json
// @Produce     json
// @Security    BearerAuth
// @Param       body body object true "{items: [\"10.1038/...\", \"arXiv:2401.12345\", \"https://doi.org/...\"]} (max 50)"
// @Success     200 {object} map[string]interface{} "{items: [{input, kind, paper_id?, created, error?}], enqueued}"
// @Failure     400 {object} map[string]string
// @Failure     401 {object} map[string]string
// @Failure     403 {object} map[string]string
// @Failure     503 {object} map[string]string "downloader or registry not configured"
// @Router      /api/downloader/fetch [post]
func docDownloaderFetch() {}

// downloaderJobs lists downloader job progress.
//
// @Summary     Downloader jobs
// @Description In-process snapshot of downloader jobs (active + recent
// @Description terminal): per-paper state/phase, the winning strategy,
// @Description the full attempt trace and healthz-style counters. The
// @Description SPA's Robust Downloader page polls this every 2s while
// @Description jobs are active. Requires papers:read.
// @Tags        Downloader
// @Produce     json
// @Security    BearerAuth
// @Success     200 {object} map[string]interface{} "{jobs: [...], counters: {queued, in_flight, succeeded, failed, skipped}}"
// @Failure     401 {object} map[string]string
// @Failure     403 {object} map[string]string
// @Router      /api/downloader/jobs [get]
func docDownloaderJobs() {}

// downloaderRemoteJobs lists the persisted remote-fleet job snapshot.
//
// @Summary     Downloader remote jobs
// @Description PostgreSQL-persisted snapshot of outbound fleet tasks (most
// @Description recently updated first, capped at 500): id, worker_id, state
// @Description (queued/running/staged/done/failed), identifier and error.
// @Description Unlike /api/downloader/jobs this survives server restarts.
// @Description Answers 200 with {enabled: false, jobs: []} when the remote
// @Description fleet is disabled. Requires papers:read.
// @Tags        Downloader
// @Produce     json
// @Security    BearerAuth
// @Success     200 {object} map[string]interface{} "{enabled: bool, jobs: [{id, worker_id, state, identifier, error?, updated_at}]}"
// @Failure     401 {object} map[string]string
// @Failure     403 {object} map[string]string
// @Router      /api/downloader/remote-jobs [get]
func docDownloaderRemoteJobs() {}

// papersList returns the paginated registry paper list.
//
// @Summary     List papers
// @Description Paginated list of registry papers (merged tombstones
// @Description excluded). has_md filters on converted markdown present on
// @Description the paper's default asset (the "converted papers" page);
// @Description status filters by lifecycle; q is a case-insensitive title
// @Description substring; arxiv_id / doi / paper_id are exact-identity
// @Description filters (arxiv_id may carry a version suffix and doi a
// @Description URL prefix — both are canonicalized before matching).
// @Description Sorted by created_at (default) or updated_at,
// @Description descending. Requires the papers:read scope.
// @Tags        Papers
// @Produce     json
// @Security    BearerAuth
// @Param       has_md   query bool   false "true = only papers with converted markdown on the default asset"
// @Param       status   query string false "pending | ready | failed"
// @Param       q        query string false "title substring (case-insensitive)"
// @Param       arxiv_id query string false "exact arXiv id filter (version suffix optional)"
// @Param       doi      query string false "exact DOI filter (URL prefix tolerated)"
// @Param       paper_id query string false "exact surrogate paper id (qa_...)"
// @Param       page     query int    false "1-based page (default 1)"
// @Param       per_page query int    false "page size (default 20, max 100)"
// @Param       sort     query string false "created_at (default) | updated_at, descending"
// @Success     200 {object} map[string]interface{} "{items:[{paper_id,arxiv_id,doi,title,status,has_pdf,has_md,image_count,created_at,updated_at}], total, page, per_page}"
// @Failure     400 {object} map[string]string "invalid query parameter"
// @Failure     401 {object} map[string]string
// @Failure     403 {object} map[string]string
// @Failure     503 {object} map[string]string "registry unavailable"
// @Router      /api/papers [get]
func docPapersList() {}

// paperDetail returns one registry paper with its assets.
//
// @Summary     Get paper by id
// @Description Returns the registry paper (status, identities) plus its
// @Description asset rows. The id is the surrogate paper_id ("qa_" +
// @Description ULID), an arXiv id (new or old style, with or without a
// @Description vN suffix), or a DOI — identifier forms resolve against
// @Description the registry; a valid identifier the server does not
// @Description host answers 404 pointing at GET /api/papers/lookup for
// @Description metadata resolution.
// @Tags        Papers
// @Produce     json
// @Security    BearerAuth
// @Param       paper_id path string true "paper id: qa_... | arXiv id | DOI"
// @Success     200 {object} map[string]interface{}
// @Failure     401 {object} map[string]string
// @Failure     403 {object} map[string]string
// @Failure     404 {object} map[string]string
// @Failure     503 {object} map[string]string "registry unavailable"
// @Router      /api/papers/{paper_id} [get]
func docPaperDetail() {}

// paperImages lists one paper's image files from the object store, on demand.
//
// @Summary     List paper images
// @Description Lists the image objects of every asset of the paper,
// @Description fetched on demand from the images bucket (no separate sync
// @Description listing exists). Each asset reports kind "zip" (single
// @Description images zip) or "dir" (per-paper directory) depending on the
// @Description layout found, its files, and whether the 200-object cap
// @Description truncated the listing. Empty files when the asset has no
// @Description images. Requires the papers:read scope.
// @Tags        Papers
// @Produce     json
// @Security    BearerAuth
// @Param       paper_id path string true "surrogate paper id (qa_...)"
// @Success     200 {object} map[string]interface{} "{paper_id, assets:[{asset_id, source, arxiv_version, kind, files:[{key,size}], truncated}]}"
// @Failure     401 {object} map[string]string
// @Failure     403 {object} map[string]string
// @Failure     404 {object} map[string]string
// @Failure     503 {object} map[string]string "registry unavailable"
// @Router      /api/papers/{paper_id}/images [get]
func docPaperImages() {}

// --- Papers ------------------------------------------------------------------

// paperStats returns downloaded / converted paper counts from the index.
//
// @Summary     Paper statistics
// @Description Counts of PDFs, markdown, JSON and mineru-pending papers from
// @Description the S3 paper index. Returns {available:false} when no S3
// @Description backend is configured.
// @Tags        Papers
// @Produce     json
// @Success     200 {object} map[string]interface{}
// @Security    BearerAuth
// @Failure     401 {object} map[string]string
// @Failure     403 {object} map[string]string
// @Router      /api/papers/stats [get]
func docPaperStats() {}

// needsMineru lists papers that have a PDF but no converted markdown.
//
// @Summary     Papers needing MinerU
// @Tags        Papers
// @Produce     json
// @Param       limit query int false "max results"
// @Success     200 {object} map[string]interface{}
// @Security    BearerAuth
// @Failure     401 {object} map[string]string
// @Failure     403 {object} map[string]string
// @Router      /api/papers/needs-mineru [get]
func docNeedsMineru() {}

// paperLookup batch-resolves namespaced kind:id references against the local
// OpenAlex corpus (ADR 0007).
//
// @Summary     Resolve references (batch, exact)
// @Description Resolves comma-separated namespaced `kind:id` refs
// @Description (arxiv:… / openalex:… / doi:…) against the local OpenAlex
// @Description corpus. Returns per-ref {ref,title,authors,year,hosted,
// @Description has_md,resolved} plus corpus_available — hosted reports
// @Description whether QuantumAtlas hosts the ref, and has_md (meaningful
// @Description only when hosted) whether its default asset carries
// @Description converted markdown, so batch consumers learn both facts in
// @Description one call (≤200 refs per batch). Exact-by-id only; fuzzy
// @Description search is a separate deferred capability.
// @Tags        Papers
// @Produce     json
// @Param       ids query string true "comma-separated kind:id refs"
// @Success     200 {object} map[string]interface{}
// @Security    BearerAuth
// @Failure     400 {object} map[string]string
// @Failure     401 {object} map[string]string
// @Failure     403 {object} map[string]string
// @Router      /api/papers/lookup [get]
func docPaperLookup() {}

// paperMatch is the paper identity matching surface (proxied to the
// qatlas-match microservice when match.remote is enabled).
//
// @Summary     Match paper identities against the registry
// @Description Decides whether papers (qatlas-id / DOI / arXiv id /
// @Description OpenAlex id / paper URL / title) are already inside the
// @Description qatlas registry and returns the unified qa_… id.
// @Description Precision-first: identifiers match exactly after strict
// @Description normalization (trim / lowercase / known URL prefix /
// @Description arXiv vN); titles match only when EVERY word matches
// @Description (case-insensitive, order-insensitive). Merged papers
// @Description resolve to the surviving paper. Free-form `inputs` are
// @Description auto-detected per entry; the typed fields force a kind;
// @Description author/year narrow ambiguous title matches.
// @Description Upstream failures are never reported as empty matches: HTTP 504
// @Description denotes timeout; HTTP 502 includes a stable code distinguishing
// @Description transport, upstream HTTP and invalid response failures. Only the
// @Description numeric upstream_status is exposed, never private upstream bodies.
// @Tags        Papers
// @Accept      json
// @Produce     json
// @Param       request body object true "{inputs: [string], doi?, arxiv_id?, openalex_id?, qatlas_id?, url?, title?, author?, year?}"
// @Success     200 {object} map[string]interface{} "{results: [{input, kind, normalized, matched, qatlas_id, method, ambiguous, paper, candidates, truncated, reason}]}"
// @Security    BearerAuth
// @Failure     400 {object} map[string]string
// @Failure     401 {object} map[string]string
// @Failure     403 {object} map[string]string
// @Failure     502 {object} map[string]interface{} "code, detail, optional numeric upstream_status"
// @Failure     503 {object} map[string]string "match backend disabled"
// @Failure     504 {object} map[string]interface{} "match_upstream_timeout; no match result available"
// @Router      /api/papers/match [post]
func docPaperMatch() {}

// paperResources stanzas were removed in v0.9.0 — the server no longer
// serves PDF or image bytes outbound by default. paperMarkdown /
// paperMarkdownStatus / paperPDF / paperPDFStatus / paperImagesZip are
// conditional on QATLAS_PAPER_ACCESS_ENABLED=true (default off). The
// /pdf endpoint itself now always answers 410 Gone (PDF delivery
// disabled, plan §B); only its side-effect-free status probe remains.
// See the RegisterPapers doc comment for the compliance rationale.

// paperMarkdown serves the cached markdown bytes for a paper.
//
// @Summary     Get paper markdown
// @Description Returns the cached MinerU markdown for the given arxiv id
// @Description (or DOI — the id_or_doi path component is auto-detected
// @Description against the IANA prefix `10.<registrant>/...`). A qa_
// @Description paper_id is also accepted: the server resolves the
// @Description surrogate to the paper's canonical identity (pinned to
// @Description the highest ingested arXiv version; DOI-only papers are
// @Description served from the DOI namespace). Only
// @Description registered when QATLAS_PAPER_ACCESS_ENABLED=true on the
// @Description server (default off).
// @Description
// @Description Canonical resolution: a `:PaperWork` node with
// @Description `identifier_scheme='doi'` ALWAYS wins over its arxiv
// @Description twin when both exist — DOI is the canonical identity of
// @Description the published version. The dispatcher serves DOI bytes
// @Description for either id form when a DOI contribution is on file;
// @Description pass `?force_arxiv=1` to opt out per request (DOI input
// @Description with force_arxiv + no arxiv twin returns 409). See
// @Description docs/server/upload-api.md §Canonical resolution.
// @Description
// @Description Long-running operation semantics: on cache miss the
// @Description server may transparently fetch the PDF from arxiv.org
// @Description (silent_fetch) — or, for a DOI without an arXiv twin,
// @Description from the open-access PDF URL OpenAlex surfaces
// @Description (best_oa_location.pdf_url) — and trigger a MinerU
// @Description conversion. The first call returns 202 with
// @Description `Operation-Location:
// @Description /api/papers/{id}/markdown/status` and `Retry-After: 5`;
// @Description clients poll the status endpoint until state=cached then
// @Description re-GET this resource for the bytes. A DOI with no arXiv
// @Description twin and no OA PDF stays 404 (contribute the PDF via
// @Description POST /api/papers/{doi}/upload-pdf).
// @Description
// @Description Transport (ADR 0011): markdown DEFAULTS to a byte stream
// @Description (text/markdown). Pass `?format=link` to instead receive a
// @Description JSON body with a short-lived RustFS direct link
// @Description (`{markdown_url, format:"link", expires_in}`); on a backend
// @Description that cannot presign (dev LocalStore) a link request
// @Description transparently falls back to bytes.
// @Tags        Papers
// @Produce     plain
// @Security    BearerAuth
// @Param       id_or_doi path string true "arXiv canonical id with vN suffix, or a DOI (e.g. 10.1103/PhysRevLett.103.150502)"
// @Param       force_arxiv query string false "1/true: bypass the DOI-canonical default and serve the arxiv twin (or 409 if no twin exists)"
// @Param       format query string false "link|bytes — override the default transport (markdown defaults to bytes)"
// @Success     200 {string} string "markdown bytes (text/markdown), or a JSON {markdown_url} when ?format=link"
// @Success     202 {object} map[string]interface{} "long-running operation started; poll status_url"
// @Failure     400 {object} map[string]string "invalid arxiv_id or DOI"
// @Failure     401 {object} map[string]string
// @Failure     403 {object} map[string]string
// @Failure     404 {object} map[string]interface{} "DOI unknown to OpenAlex / no arXiv twin and no OA PDF (contrib upload possible); or paper unknown and silent fetch unavailable"
// @Failure     409 {object} map[string]interface{} "force_arxiv requested but DOI has no arxiv twin"
// @Failure     502 {object} map[string]interface{} "prior conversion failed inside the cooldown window, or OpenAlex upstream error"
// @Failure     503 {object} map[string]interface{} "asset storage unavailable (code=asset_store_unavailable, retryable=true, Retry-After); cache-only mode or DOI resolution unavailable"
// @Router      /api/papers/{id_or_doi}/markdown [get]
func docPaperMarkdown() {}

// paperMarkdownStatus reports current markdown / conversion state.
//
// @Summary     Get markdown conversion status
// @Description Side-effect-free poll surface. Never starts a job and
// @Description never triggers a fetch. Only registered when
// @Description QATLAS_PAPER_ACCESS_ENABLED=true on the server. Like the
// @Description markdown endpoint, the id may be an arXiv id, a DOI, or
// @Description a qa_ paper_id.
// @Description
// @Description Canonical resolution: same DOI-wins rule as
// @Description /api/papers/{id_or_doi}/markdown. Pass `?force_arxiv=1`
// @Description to query the arxiv-side status instead.
// @Description
// @Description Response shape always carries the agent-decision triple
// @Description `state / pdf_ready / md_ready` plus an optional `phase`
// @Description (fetching_pdf | converting_md | ready | error_fetching |
// @Description error_converting) and `fetch` / `convert` sub-objects
// @Description with bytes_received / mineru_task_id / polled_count so a
// @Description polling client can show precise progress.
// @Tags        Papers
// @Produce     json
// @Security    BearerAuth
// @Param       id_or_doi path string true "arXiv canonical id or DOI"
// @Param       force_arxiv query string false "1/true: bypass DOI-canonical default"
// @Success     200 {object} map[string]interface{} "status payload (state ∈ cached|queued|running|none|failed|cooldown|unavailable)"
// @Failure     400 {object} map[string]string
// @Failure     401 {object} map[string]string
// @Failure     403 {object} map[string]string
// @Failure     404 {object} map[string]string "paper unknown and silent fetch unavailable"
// @Failure     503 {object} map[string]interface{} "asset storage unavailable; retryable=true with Retry-After"
// @Router      /api/papers/{id_or_doi}/markdown/status [get]
func docPaperMarkdownStatus() {}

// paperPDF is disabled: PDF delivery was turned off in favour of the
// markdown endpoint, so the route now always answers 410 Gone.
//
// @Summary     Get paper PDF (disabled — 410 Gone)
// @Description PDF delivery is disabled. This endpoint no longer
// @Description serves PDF bytes (or direct links) in any state: it
// @Description validates the id and always returns 410 Gone with
// @Description `{"detail": "PDF delivery is disabled; use the markdown
// @Description endpoint instead"}`. Use
// @Description /api/papers/{id_or_doi}/markdown instead. The id may be
// @Description an arXiv id, a DOI, or a qa_ paper_id (resolved to the
// @Description canonical identity first). Only registered when
// @Description QATLAS_PAPER_ACCESS_ENABLED=true on the server.
// @Tags        Papers
// @Produce     json
// @Security    BearerAuth
// @Param       id_or_doi path string true "arXiv canonical id with vN suffix, or a DOI"
// @Param       force_arxiv query string false "1/true: bypass DOI-canonical default; return 409 if DOI has no arxiv twin"
// @Success     410 {object} map[string]string "PDF delivery is disabled; use the markdown endpoint instead"
// @Failure     400 {object} map[string]string "invalid arxiv_id or DOI"
// @Failure     401 {object} map[string]string
// @Failure     403 {object} map[string]string
// @Failure     409 {object} map[string]interface{} "force_arxiv requested but DOI has no arxiv twin"
// @Failure     502 {object} map[string]interface{} "OpenAlex upstream error (DOI dispatch)"
// @Failure     503 {object} map[string]interface{} "DOI resolution unavailable"
// @Router      /api/papers/{id_or_doi}/pdf [get]
func docPaperPDF() {}

// paperPDFStatus reports current PDF / fetch state.
//
// @Summary     Get PDF fetch status
// @Description Side-effect-free probe reporting the pdf_ready /
// @Description md_ready booleans for a paper. Retained for debugging
// @Description after PDF delivery was disabled (GET .../pdf answers
// @Description 410 Gone); the body no longer carries a pdf_url. The id
// @Description may be an arXiv id, a DOI, or a qa_ paper_id. Only
// @Description registered when QATLAS_PAPER_ACCESS_ENABLED=true.
// @Description
// @Description Canonical resolution: same DOI-wins rule as
// @Description /api/papers/{id_or_doi}/markdown. Pass `?force_arxiv=1`
// @Description to query the arxiv-side status instead.
// @Tags        Papers
// @Produce     json
// @Security    BearerAuth
// @Param       id_or_doi path string true "arXiv canonical id or DOI"
// @Param       force_arxiv query string false "1/true: bypass DOI-canonical default"
// @Success     200 {object} map[string]interface{} "status payload (state ∈ cached|queued|running|none|failed|unavailable)"
// @Failure     400 {object} map[string]string
// @Failure     401 {object} map[string]string
// @Failure     403 {object} map[string]string
// @Failure     503 {object} map[string]interface{} "asset storage unavailable; retryable=true with Retry-After"
// @Router      /api/papers/{id_or_doi}/pdf/status [get]
func docPaperPDFStatus() {}

// paperImagesZip downloads the paper's images bundle (one zip produced
// by the MinerU conversion).
//
// @Summary     Get paper images zip
// @Description Returns the images zip (application/zip) for the given
// @Description arxiv id or DOI — the bundle the MinerU conversion
// @Description produced alongside the markdown. The id may also be a
// @Description qa_ paper_id (resolved to the canonical identity
// @Description first). Only registered when
// @Description QATLAS_PAPER_ACCESS_ENABLED=true on the server.
// @Description
// @Description Canonical resolution: same DOI-wins rule as
// @Description /api/papers/{id_or_doi}/markdown; pass `?force_arxiv=1`
// @Description to opt out per request.
// @Description
// @Description This endpoint has no long-running-operation semantics:
// @Description when no images zip is stored it answers 404 (fetch
// @Description /markdown first to trigger the conversion that produces
// @Description the images).
// @Description
// @Description Transport (ADR 0011): defaults to a byte stream
// @Description (application/zip). Pass `?format=link` to instead
// @Description receive a JSON body with a short-lived RustFS direct
// @Description link (`{images_url, format:"link", expires_in}`); on a
// @Description backend that cannot presign (dev LocalStore) a link
// @Description request transparently falls back to bytes. Any other
// @Description ?format= value is a 400.
// @Tags        Papers
// @Produce     application/zip
// @Security    BearerAuth
// @Param       id_or_doi path string true "arXiv canonical id with vN suffix, or a DOI"
// @Param       force_arxiv query string false "1/true: bypass DOI-canonical default; return 409 if DOI has no arxiv twin"
// @Param       format query string false "link|bytes — override the default transport (images zip defaults to bytes)"
// @Success     200 {string} string "images zip bytes (application/zip), or a JSON {images_url} when ?format=link"
// @Failure     400 {object} map[string]string "invalid arxiv_id or DOI, or invalid ?format= value"
// @Failure     401 {object} map[string]string
// @Failure     403 {object} map[string]string
// @Failure     404 {object} map[string]string "no images available (fetch /markdown first to trigger conversion)"
// @Failure     409 {object} map[string]interface{} "force_arxiv requested but DOI has no arxiv twin"
// @Failure     503 {object} map[string]interface{} "DOI resolution unavailable"
// @Router      /api/papers/{id_or_doi}/images/zip [get]
func docPaperImagesZip() {}

// paperFigures returns the figure/caption index extracted from the
// paper's MinerU markdown.
//
// @Summary     Get paper figures index
// @Description Groups the markdown's image references into figures:
// @Description consecutive `![](images/<sha256>.<ext>)` lines form one
// @Description multi-panel figure sharing a caption (searched up to 12
// @Description lines below the group, then 4 lines above). Each figure
// @Description reports its number, caption, the preceding prose line as
// @Description context (≤200 runes) and its image files with sizes plus
// @Description per-image download URLs; images no figure references come
// @Description back as unmatched_images, and image_count is the total
// @Description listed. Sizes come from the images zip's central
// @Description directory (no member decompression) or the legacy
// @Description per-paper directory listing. A paper with no markdown
// @Description answers 200 with markdown_ready:false and empty figures
// @Description (its conversion has not produced markdown yet). The id
// @Description may be a qa_ surrogate, an arXiv id, or a DOI; arXiv/DOI
// @Description forms are only dispatched when
// @Description QATLAS_PAPER_ACCESS_ENABLED=true.
// @Tags        Papers
// @Produce     json
// @Security    BearerAuth
// @Param       id path string true "paper id: qa_... | arXiv id | DOI"
// @Success     200 {object} map[string]interface{} "{paper_id, resolved_id, markdown_ready, figures:[{fig_no, caption, context, images:[{name,size,url}]}], unmatched_images:[{name,size,url}], image_count}"
// @Failure     400 {object} map[string]string "unrecognized paper id"
// @Failure     401 {object} map[string]string
// @Failure     403 {object} map[string]string
// @Failure     404 {object} map[string]string "no such paper"
// @Failure     503 {object} map[string]string "registry unavailable"
// @Router      /api/papers/{id}/figures [get]
func docPaperFigures() {}

// paperImageGet streams one extracted image of a paper.
//
// @Summary     Get paper image
// @Description Serves a single MinerU-extracted image (content-addressed
// @Description `<sha256>.<jpg|jpeg|png|gif|webp>`) without downloading
// @Description the whole images zip — the URLs in the figures index and
// @Description the images listing point here. Bytes come from the
// @Description paper's images zip (member extracted in memory) or its
// @Description legacy per-paper directory. Responses are immutable
// @Description content-addressed files and carry
// @Description `Cache-Control: public, max-age=86400`. The id may be a
// @Description qa_ surrogate, an arXiv id, or a DOI; arXiv/DOI forms are
// @Description only dispatched when QATLAS_PAPER_ACCESS_ENABLED=true.
// @Tags        Papers
// @Produce     image/jpeg
// @Security    BearerAuth
// @Param       id path string true "paper id: qa_... | arXiv id | DOI"
// @Param       name path string true "image file name: <sha256-hex>.<jpg|jpeg|png|gif|webp>"
// @Success     200 {file} binary "image bytes with the per-extension Content-Type"
// @Failure     400 {object} map[string]string "name is not a whitelisted image file name"
// @Failure     401 {object} map[string]string
// @Failure     403 {object} map[string]string
// @Failure     404 {object} map[string]string "no such paper, or the paper's images hold no such member"
// @Failure     503 {object} map[string]string "registry unavailable"
// @Router      /api/papers/{id}/images/{name} [get]
func docPaperImageGet() {}

// --- Block comments (Q1 originals) --------------------------------------------

// paperSourcesList lists the source-PDF identities of a paper.
//
// @Summary     List paper sources (block comments)
// @Description Source PDF assets of a paper for the block-comments
// @Description surface: each row pins one immutable byte-identity
// @Description (origin label like arxiv:v2 / upload, sha256, size).
// @Description The current_source_id / is_current pointer is derived
// @Description from the paper's current parse revision (paper_sources
// @Description rows are append-only). The paper_id path segment may be
// @Description a qa_ surrogate, an arXiv id, or a DOI — all resolve to
// @Description the canonical qa_ (merged papers follow merged_into;
// @Description the request alias is echoed via X-QAtlas-Requested-Id /
// @Description X-QAtlas-Resolved-Id). Requires login or the
// @Description papers:read scope.
// @Tags        BlockComments
// @Produce     json
// @Security    BearerAuth
// @Param       paper_id path string true "paper id: qa_... | arXiv id | DOI"
// @Success     200 {object} map[string]interface{} "{paper_id, sources:[{source_id,origin,sha256,size_bytes,is_current,created_at,pdf_endpoint}], current_source_id}"
// @Failure     400 {object} map[string]string "unrecognized id form"
// @Failure     401 {object} map[string]string
// @Failure     403 {object} map[string]string
// @Failure     404 {object} map[string]string "no such paper"
// @Failure     503 {object} map[string]string "registry unavailable"
// @Router      /api/papers/{paper_id}/sources [get]
func docPaperSourcesList() {}

// paperSourcePDF streams the original source PDF bytes.
//
// @Summary     Get source PDF bytes
// @Description The ORIGINAL PDF bytes of one source, hash-verifiable
// @Description via ETag / X-QAtlas-Sha256 (= the row's sha256). Range
// @Description requests are honoured with the SAME authentication as
// @Description full GETs. ?version=vN is a pin, not a hint: on mismatch
// @Description with the source's own origin the answer is 404 — the
// @Description server never substitutes a newer version or the journal
// @Description edition. This is the block-comments reading surface; the
// @Description legacy /api/papers/{id}/pdf route stays 410.
// @Tags        BlockComments
// @Produce     application/pdf
// @Security    BearerAuth
// @Param       paper_id path string true "paper id: qa_... | arXiv id | DOI"
// @Param       source_id path string true "source id from the sources list"
// @Param       version query string false "pin: arXiv version, e.g. v2 — mismatch 404s, never substitutes"
// @Success     200 {file} binary "PDF bytes (Range → 206)"
// @Success     206 {file} binary "partial content"
// @Failure     400 {object} map[string]string
// @Failure     401 {object} map[string]string
// @Failure     403 {object} map[string]string
// @Failure     404 {object} map[string]string "no such source / version pin mismatch / bytes missing"
// @Failure     503 {object} map[string]string "registry or store unavailable"
// @Router      /api/papers/{paper_id}/sources/{source_id}/pdf [get]
func docPaperSourcePDF() {}

// paperParsesList lists the immutable parse revisions of a paper.
//
// @Summary     List parse revisions (block comments)
// @Description Parse revisions of a paper: each row is one immutable
// @Description parse artifact (schema + schema_version + artifact
// @Description sha256) bound to the source it was parsed from. A
// @Description re-parse inserts a new revision and flips the
// @Description is_current pointer (current_revision_id); old artifacts
// @Description keep serving unchanged bytes. Requires login or
// @Description papers:read.
// @Tags        BlockComments
// @Produce     json
// @Security    BearerAuth
// @Param       paper_id path string true "paper id: qa_... | arXiv id | DOI"
// @Success     200 {object} map[string]interface{} "{paper_id, parses:[{revision_id,source_id,schema,schema_version,artifact_sha256,is_current,created_at,json_endpoint,blocks_endpoint}], current_revision_id}"
// @Failure     401 {object} map[string]string
// @Failure     403 {object} map[string]string
// @Failure     404 {object} map[string]string "no such paper"
// @Failure     503 {object} map[string]string "registry unavailable"
// @Router      /api/papers/{paper_id}/parses [get]
func docPaperParsesList() {}

// paperParseJSON serves the original parse artifact bytes.
//
// @Summary     Get parse JSON bytes
// @Description The ORIGINAL Middle JSON artifact bytes of one parse
// @Description revision — not a re-serialization. ETag /
// @Description X-QAtlas-Sha256 carry the artifact sha256 the revision
// @Description row pins; the server verifies the stored bytes against
// @Description it before serving (a mismatch is a 500, never silent).
// @Description Revisions are immutable: the bytes for a revision_id
// @Description never change. Requires login or papers:read.
// @Tags        BlockComments
// @Produce     json
// @Security    BearerAuth
// @Param       paper_id path string true "paper id: qa_... | arXiv id | DOI"
// @Param       revision path string true "parse revision id"
// @Success     200 {file} binary "Middle JSON bytes (application/json)"
// @Failure     401 {object} map[string]string
// @Failure     403 {object} map[string]string
// @Failure     404 {object} map[string]string "no such revision / bytes missing"
// @Failure     500 {object} map[string]string "stored bytes fail the pinned sha256"
// @Failure     503 {object} map[string]string "registry or store unavailable"
// @Router      /api/papers/{paper_id}/parses/{revision}/json [get]
func docPaperParseJSON() {}

// paperBlocksList keyset-paginates the top-level blocks of one parse.
//
// @Summary     List parse blocks (keyset)
// @Description Top-level blocks of one immutable parse revision, in
// @Description stable (page_idx, block index) order. Block indexes are
// @Description the parse's own public 1-based numbers and may be
// @Description non-contiguous (1,2,5,...); gaps are real — never
// @Description renumbered. page_idx is the original 0-based page index
// @Description (page_no in each item is the public 1-based number).
// @Description cursor is the opaque next_cursor token (keyset on the
// @Description last emitted block); per_page defaults to 20, max 100.
// @Description Requires login or papers:read.
// @Tags        BlockComments
// @Produce     json
// @Security    BearerAuth
// @Param       paper_id path string true "paper id: qa_... | arXiv id | DOI"
// @Param       revision path string true "parse revision id"
// @Param       page_idx query int false "filter: exact 0-based page index"
// @Param       cursor query string false "keyset cursor from next_cursor"
// @Param       per_page query int false "page size (default 20, max 100)"
// @Success     200 {object} map[string]interface{} "{paper_id,revision_id,schema,schema_version,blocks:[{page_idx,block_index,page_no,type,content,bbox,has_bbox,raw}],next_cursor}"
// @Failure     400 {object} map[string]string "bad query parameter"
// @Failure     401 {object} map[string]string
// @Failure     403 {object} map[string]string
// @Failure     404 {object} map[string]string "no such revision / bytes missing"
// @Failure     422 {object} map[string]string "artifact fails the docvortex.middle v2.0 profile"
// @Failure     503 {object} map[string]string "registry or store unavailable"
// @Router      /api/papers/{paper_id}/parses/{revision}/blocks [get]
func docPaperBlocksList() {}

// paperBlockGet is the combined single-block read.
//
// @Summary     Read one block (combined)
// @Description Combined read of ONE block: source (paper + source PDF
// @Description identity + parse revision identity) + anchor (the
// @Description permanent comment anchor: canonical qa_, source_id,
// @Description parse_revision, schema, page_idx, block_index) +
// @Description content (the block's parsed value, original JSON under
// @Description raw) + discussions. Block lookup is EXACT on
// @Description (page_idx, block index): a non-contiguous index gap or a
// @Description page the parse lacks answers 404 — never a
// @Description nearest-block fallback. The same visual text under two
// @Description parses of one PDF is TWO distinct anchors; comments
// @Description never migrate between revisions. discussions is an
// @Description empty placeholder until the Q2 comment API lands
// @Description (discussions_ready=false). Requires login or
// @Description papers:read.
// @Tags        BlockComments
// @Produce     json
// @Security    BearerAuth
// @Param       paper_id path string true "paper id: qa_... | arXiv id | DOI"
// @Param       revision path string true "parse revision id"
// @Param       page_idx path int true "0-based page index"
// @Param       block_index path int true "1-based public block index (exact)"
// @Success     200 {object} map[string]interface{} "{source, anchor, content, discussions, discussions_ready, next_cursor}"
// @Failure     400 {object} map[string]string "malformed block path"
// @Failure     401 {object} map[string]string
// @Failure     403 {object} map[string]string
// @Failure     404 {object} map[string]string "no such revision or block (exact-index semantics)"
// @Failure     422 {object} map[string]string "artifact fails the docvortex.middle v2.0 profile"
// @Failure     503 {object} map[string]string "registry or store unavailable"
// @Router      /api/papers/{paper_id}/parses/{revision}/blocks/{page_idx}/{block_index} [get]
func docPaperBlockGet() {}

// paperBlockImage crops the original PDF page render to the block bbox.
//
// @Summary     Get block image (original-page crop)
// @Description A PNG crop of the ORIGINAL source PDF page rendered and
// @Description clipped to the block's [0,1]-normalized bbox — a real
// @Description page region, never a redraw of the parsed text. Honest
// @Description failures, never a fabricated image: the block carries
// @Description no bbox → 404 reason=no_bbox; the source PDF bytes are
// @Description missing → 404 reason=source_missing; the page is beyond
// @Description the source PDF → 404 reason=page_out_of_range; no PDF
// @Description rasterizer installed on the server → 503
// @Description reason=renderer_unavailable (operators install
// @Description poppler-utils or set paper_access.block_image_command;
// @Description resolution via paper_access.block_image_dpi, default
// @Description 150). ETag is deterministic over (source sha, page,
// @Description bbox, dpi). Requires login or papers:read.
// @Tags        BlockComments
// @Produce     image/png
// @Security    BearerAuth
// @Param       paper_id path string true "paper id: qa_... | arXiv id | DOI"
// @Param       revision path string true "parse revision id"
// @Param       page_idx path int true "0-based page index"
// @Param       block_index path int true "1-based public block index (exact)"
// @Success     200 {file} binary "PNG crop of the rendered original page"
// @Failure     400 {object} map[string]string "malformed block path"
// @Failure     401 {object} map[string]string
// @Failure     403 {object} map[string]string
// @Failure     404 {object} map[string]string "no_bbox | source_missing | page_out_of_range | no such revision/block"
// @Failure     422 {object} map[string]string "artifact fails the docvortex.middle v2.0 profile"
// @Failure     503 {object} map[string]string "renderer_unavailable | registry or store unavailable"
// @Router      /api/papers/{paper_id}/parses/{revision}/blocks/{page_idx}/{block_index}/image [get]
func docPaperBlockImage() {}

// paperStatusBatch probes asset readiness for up to 200 papers at once.
//
// @Summary     Batch paper asset status
// @Description Folds the .../markdown/status agent-decision surface into
// @Description one request: for each comma-separated id (qa_ surrogate,
// @Description arXiv id, or DOI — de-duplicated, max 200) the entry
// @Description carries requested_id, the resolved serving id (bare
// @Description arXiv inputs pin to the highest ingested asset version),
// @Description md_ready / pdf_ready store probes, the converter
// @Description state machine's state/phase labels, the default asset's
// @Description image_count, and an error field ("not found" for ids the
// @Description registry does not host; "catalog unavailable" when
// @Description PostgreSQL is down — per-entry, never failing the batch).
// @Tags        Papers
// @Produce     json
// @Security    BearerAuth
// @Param       ids query string true "comma-separated paper ids (qa_... | arXiv id | DOI; max 200)"
// @Success     200 {object} map[string]interface{} "{results:[{requested_id, resolved_id, md_ready, pdf_ready, image_count, phase, state, error}]}"
// @Failure     400 {object} map[string]string "missing or over-limit ids list"
// @Failure     401 {object} map[string]string
// @Failure     403 {object} map[string]string
// @Router      /api/papers/status/batch [get]
func docPaperStatusBatch() {}

// uploadPDF stores a paper PDF.
//
// @Summary     Upload paper PDF (arXiv id or DOI)
// @Description Content-addressed upload with sha256 idempotency. 200 when
// @Description bytes are unchanged, 201 when written, 409 on a content
// @Description conflict without overwrite=true.
// @Description
// @Description The {arxiv_id} slot also accepts a DOI (`10.<registrant>/<suffix>`)
// @Description for contributing a *published* version that may have no arXiv
// @Description preprint. DOI uploads are stored under a disjoint `pdf/doi/...`
// @Description namespace and the server resolves the DOI's title / authors /
// @Description linked arxiv id from OpenAlex — the contributor cannot supply
// @Description that metadata. The result is reported in `X-QAtlas-Verification`
// @Description and the JSON `verification` block; `verify=strict` rejects a
// @Description doi-not-found with 409 (or metadata-unavailable / unconfigured
// @Description with 503).
// @Tags        Papers
// @Accept      mpfd
// @Produce     json
// @Security    BearerAuth
// @Param       arxiv_id        path     string true  "arXiv identifier (with vN) OR DOI (10.x/...)"
// @Param       overwrite       query    bool   false "overwrite on content conflict"
// @Param       expected_sha256 query    string false "client-computed PDF sha256 (in-transit guard)"
// @Param       verify          query    string false "DOI only: 'strict' rejects when OpenAlex cannot resolve the DOI (default warn)"
// @Param       pdf             formData file   true  "PDF file"
// @Success     201 {object} map[string]interface{} "created"
// @Success     200 {object} map[string]interface{} "unchanged"
// @Failure     400 {object} map[string]interface{}
// @Failure     409 {object} map[string]interface{}
// @Router      /api/papers/{arxiv_id}/upload-pdf [post]
func docUploadPDF() {}

// uploadMineRU stores a MinerU result zip (markdown + images bundle) for a paper.
//
// @Summary     Upload paper MinerU bundle (arXiv id or DOI)
// @Description Accepts the entire MinerU result zip exactly as returned by `full_zip_url`. Server extracts `full.md` plus every `images/*` entry and stores them to the markdown and images object buckets respectively. Images are written before the markdown so any reader that observes the markdown also observes all referenced images. Replaces the v0.7.x `upload-markdown` endpoint (which only accepted a single .md file and silently dropped images).
// @Description
// @Description The {arxiv_id} slot also accepts a DOI (`10.<registrant>/<suffix>`) to contribute the converted *published* version. DOI bundles are stored under the `markdown/doi/...` + `images/doi/...` namespace; the server resolves canonical metadata (title, authors, linked arxiv id) from OpenAlex on the contributor's behalf — there is no `title` / `authors` form field. `verify=strict` rejects when OpenAlex cannot resolve the DOI (see upload-pdf). The result is reported in `X-QAtlas-Verification`.
// @Tags        Papers
// @Accept      mpfd
// @Produce     json
// @Security    BearerAuth
// @Param       arxiv_id        path     string true  "arXiv identifier (with vN) OR DOI (10.x/...)"
// @Param       overwrite       query    bool   false "overwrite on content conflict"
// @Param       expected_sha256 query    string false "client-computed zip sha256 (in-transit integrity check)"
// @Param       pdf_sha256      query    string false "sha256 of the source PDF that was converted (cross-checked against stored PDF)"
// @Param       verify          query    string false "DOI only: 'strict' rejects when OpenAlex cannot resolve the DOI (default warn)"
// @Param       source          query    string false "short label of the contributor's MinerU run (truncated to 64 chars)"
// @Param       mineru_zip      formData file   true  "MinerU result zip (must contain full.md; optional images/*)"
// @Success     201 {object} map[string]interface{}
// @Success     200 {object} map[string]interface{}
// @Failure     400 {object} map[string]interface{}
// @Failure     409 {object} map[string]interface{}
// @Router      /api/papers/{arxiv_id}/upload-mineru [post]
func docUploadMineRU() {}

// mineruClaim acquires a MinerU processing claim for a paper.
//
// @Summary     Claim MinerU processing
// @Tags        Papers
// @Produce     json
// @Security    BearerAuth
// @Param       arxiv_id path string true "arXiv identifier"
// @Success     201 {object} map[string]interface{} "claim granted (body is the claim record)"
// @Failure     400 {object} map[string]string      "invalid arxiv_id"
// @Failure     404 {object} map[string]string      "not claimable (no PDF in catalog, or markdown already exists)"
// @Failure     409 {object} map[string]interface{} "already claimed by someone else (body includes existing claim details)"
// @Failure     500 {object} map[string]string      "internal error"
// @Failure     503 {object} map[string]string      "catalog unavailable (PostgreSQL unreachable)"
// @Router      /api/papers/{arxiv_id}/mineru-claim [post]
func docMineruClaim() {}

// mineruLease acquires a MinerU processing lease for a paper.
//
// @Summary     Acquire MinerU processing lease
// @Description Acquires the same MinerU processing lease returned by the claim path. The response body uses claim_id as the lease identifier.
// @Tags        Papers
// @Produce     json
// @Security    BearerAuth
// @Param       arxiv_id path string true "arXiv identifier"
// @Success     201 {object} map[string]interface{} "lease granted (body is the lease record; claim_id identifies the lease)"
// @Failure     400 {object} map[string]string      "invalid arxiv_id"
// @Failure     404 {object} map[string]string      "not claimable (no PDF in catalog, or markdown already exists)"
// @Failure     409 {object} map[string]interface{} "already leased by someone else (body includes existing lease details)"
// @Failure     500 {object} map[string]string      "internal error"
// @Failure     503 {object} map[string]string      "catalog unavailable (PostgreSQL unreachable)"
// @Router      /api/v1/papers/{arxiv_id}/mineru-lease [post]
func docMineruLease() {}

// mineruClaimRelease releases a previously acquired MinerU claim.
//
// @Summary     Release MinerU claim
// @Tags        Papers
// @Security    BearerAuth
// @Param       arxiv_id path string true "arXiv identifier"
// @Param       claim_id path string true "claim id"
// @Success     204 "claim released (empty body)"
// @Failure     400 {object} map[string]string "invalid arxiv_id"
// @Failure     409 {object} map[string]string "claim_id does not match the active claim"
// @Failure     500 {object} map[string]string "internal error"
// @Failure     503 {object} map[string]string "catalog unavailable (PostgreSQL unreachable)"
// @Router      /api/papers/{arxiv_id}/mineru-claim/{claim_id} [delete]
func docMineruClaimRelease() {}

// mineruLeaseRelease releases a previously acquired MinerU lease.
//
// @Summary     Release MinerU lease
// @Description Releases a MinerU processing lease by claim_id.
// @Tags        Papers
// @Security    BearerAuth
// @Param       arxiv_id path string true "arXiv identifier"
// @Param       claim_id path string true "claim id"
// @Success     204 "lease released (empty body)"
// @Failure     400 {object} map[string]string "invalid arxiv_id"
// @Failure     409 {object} map[string]string "claim_id does not match the active lease"
// @Failure     500 {object} map[string]string "internal error"
// @Failure     503 {object} map[string]string "catalog unavailable (PostgreSQL unreachable)"
// @Router      /api/v1/papers/{arxiv_id}/mineru-lease/{claim_id} [delete]
func docMineruLeaseRelease() {}

// --- PAT ---------------------------------------------------------------------

// createPAT mints a personal access token. Session-token auth only.
//
// @Summary     Create PAT
// @Description Mints a personal access token. Plaintext is returned exactly
// @Description once. Requires a PocketBase session token (PAT auth is
// @Description refused here, to stop a leaked PAT from self-replicating).
// @Tags        PAT
// @Accept      json
// @Produce     json
// @Security    BearerAuth
// @Param       body body patCreateRequest true "token spec"
// @Success     200 {object} patCreateResponse
// @Failure     400 {object} map[string]string
// @Router      /api/pat [post]
func docCreatePAT() {}

// listPAT lists the caller's PATs (no plaintext, no hash).
//
// @Summary     List PATs
// @Tags        PAT
// @Produce     json
// @Security    BearerAuth
// @Success     200 {array} patSummary
// @Router      /api/pat [get]
func docListPAT() {}

// deletePAT revokes a PAT by id.
//
// @Summary     Delete PAT
// @Tags        PAT
// @Produce     json
// @Security    BearerAuth
// @Param       id path string true "PAT record id"
// @Success     200 {object} map[string]bool
// @Failure     404 {object} map[string]string
// @Router      /api/pat/{id} [delete]
func docDeletePAT() {}

// patScopes returns the canonical scope vocabulary.
//
// @Summary     PAT scopes
// @Description Lists the available scopes and their descriptions so clients
// @Description can render the create form without hardcoding the vocabulary.
// @Tags        PAT
// @Produce     json
// @Success     200 {object} map[string]interface{}
// @Router      /api/pat/scopes [get]
func docPATScopes() {}

// --- Me (user dashboard) -----------------------------------------------------
//
// Read-only self-service surface behind the SPA's user dashboard.
// Session-token auth only (PAT auth refused, same as /api/pat). See
// internal/routes/me.go.

// meProfile returns the caller's own profile.
//
// @Summary     My profile
// @Description Returns the signed-in user's profile:
// @Description {id, email, name, avatar, github_login, gitea_login,
// @Description github_bound, gitea_bound, is_admin, is_superadmin,
// @Description created}.
// @Description avatar is the PocketBase file name — build the URL via the
// @Description PocketBase files API. *_bound reports which OAuth2
// @Description identities are linked to the record (the dashboard's
// @Description account-binding panels).
// @Tags        Me
// @Produce     json
// @Security    BearerAuth
// @Success     200 {object} map[string]interface{} "{id, email, name, avatar, github_login, gitea_login, github_bound, gitea_bound, is_admin, is_superadmin, created}"
// @Failure     401 {object} map[string]string
// @Failure     403 {object} map[string]string "PAT auth not accepted"
// @Router      /api/me [get]
func docMeProfile() {}

// meUsage returns the caller's agentic-search metering state for today.
//
// @Summary     My usage
// @Description Returns {metric, today, limit, llm_tokens} for the
// @Description agentic-search surface: today's call count, the caller's
// @Description effective daily limit (per-user override > plan >
// @Description search.agentic.daily_limit) and the LLM tokens consumed
// @Description today. Same metering shape as the agentic response's usage
// @Description block.
// @Tags        Me
// @Produce     json
// @Security    BearerAuth
// @Success     200 {object} meUsageResponse
// @Failure     401 {object} map[string]string
// @Failure     403 {object} map[string]string "PAT auth not accepted"
// @Failure     503 {object} map[string]string "postgres registry unavailable"
// @Router      /api/me/usage [get]
func docMeUsage() {}

// meSearchKeysList returns the caller's stored search API keys.
//
// @Summary     List my search API keys
// @Description The caller's per-user third-party search API key inventory
// @Description (backend name, masked hint, updated_at — never the key
// @Description material). enabled=false means the server has no encryption
// @Description secret configured and the whole feature is off. Session-only
// @Description (same reasoning as /api/pat: a leaked PAT must not read the
// @Description owner's third-party keys).
// @Tags        Me
// @Produce     json
// @Security    BearerAuth
// @Success     200 {object} map[string]interface{} "{enabled: bool, keys: [{backend, hint, updated_at}]}"
// @Failure     401 {object} map[string]string
// @Failure     403 {object} map[string]string "PAT auth not accepted"
// @Router      /api/me/search-keys [get]
func docMeSearchKeysList() {}

// meSearchKeysPut stores one search API key.
//
// @Summary     Store my search API key
// @Description Upserts the caller's API key for one backend (AES-256-GCM
// @Description encrypted at rest; injected into multi/agentic search calls
// @Description on the caller's behalf). The backend must be a catalog
// @Description backend with a user-key slot. 503 when the server has no
// @Description encryption secret configured. Session-only.
// @Tags        Me
// @Accept      json
// @Produce     json
// @Security    BearerAuth
// @Param       backend path string true "backend name (e.g. ieee, tavily)"
// @Param       body body object true "{key: string}"
// @Success     200 {object} map[string]bool "{ok: true}"
// @Failure     400 {object} map[string]string "unknown backend / no user-key slot / empty key"
// @Failure     401 {object} map[string]string
// @Failure     403 {object} map[string]string "PAT auth not accepted"
// @Failure     503 {object} map[string]string "feature disabled"
// @Router      /api/me/search-keys/{backend} [put]
func docMeSearchKeysPut() {}

// meSearchKeysDelete removes one stored search API key.
//
// @Summary     Delete my search API key
// @Description Removes the caller's stored key for one backend (opaque 404
// @Description when absent). Session-only.
// @Tags        Me
// @Produce     json
// @Security    BearerAuth
// @Param       backend path string true "backend name"
// @Success     200 {object} map[string]bool "{ok: true}"
// @Failure     401 {object} map[string]string
// @Failure     403 {object} map[string]string "PAT auth not accepted"
// @Failure     404 {object} map[string]string "key not found"
// @Failure     503 {object} map[string]string "feature disabled"
// @Router      /api/me/search-keys/{backend} [delete]
func docMeSearchKeysDelete() {}

// --- Admin -------------------------------------------------------------------
//
// Admin console API. Session-token auth only (PATs rejected); every
// admin endpoint requires the users record to carry is_admin or
// is_superadmin (the auth.admin_logins / auth.gitea_admin_logins YAML
// lists only seed those flags at boot). See internal/routes/admin.go.

// adminWhoami reports the caller's provider login and admin status.
//
// @Summary     Admin whoami
// @Description Returns {login, is_admin, is_user_admin, is_superadmin}
// @Description for the signed-in session user. login is the github_login,
// @Description falling back to gitea_login when only that is stamped.
// @Description All three flags read the users-record role flags (the
// @Description auth.admin_logins / superadmin_logins YAML lists only
// @Description seed them at boot): is_admin and is_user_admin are true
// @Description when the record holds is_admin or is_superadmin; the
// @Description SPA uses them to decide which admin surfaces to render.
// @Description Non-admins get false flags rather than a 403.
// @Description Session-token auth only (PAT auth
// @Description refused, same as /api/pat).
// @Tags        Admin
// @Produce     json
// @Security    BearerAuth
// @Success     200 {object} map[string]interface{} "{login, is_admin, is_user_admin, is_superadmin}"
// @Failure     401 {object} map[string]string
// @Failure     403 {object} map[string]string "PAT auth not accepted"
// @Router      /api/admin/whoami [get]
func docAdminWhoami() {}

// adminDBSchema introspects the Postgres paper-registry database structure.
//
// @Summary     Postgres schema browser
// @Description Read-only introspection of the QATLAS_POSTGRES_DSN database:
// @Description every table in schema public (goose_db_version included,
// @Description alphabetical) with row estimate, total size, columns
// @Description (type/nullable/default/is_pk), indexes and constraints.
// @Description Admin-only: session token + is_admin or is_superadmin
// @Description on the users record.
// @Tags        Admin
// @Produce     json
// @Security    BearerAuth
// @Success     200 {object} map[string]interface{} "{database, tables:[{name, row_estimate, total_size, columns, indexes, constraints}]}"
// @Failure     401 {object} map[string]string
// @Failure     403 {object} map[string]string "admin only"
// @Failure     503 {object} map[string]string "postgres registry unavailable"
// @Router      /api/admin/db/schema [get]
func docAdminDBSchema() {}

// adminDevdocTicket mints a signed one-shot URL into the gated dev-docs site.
//
// @Summary     Dev-docs ticket
// @Description Returns {url} — a short-lived signed link into the admin-only
// @Description dev-docs site (/devdoc). Opening it sets an HttpOnly cookie and
// @Description redirects to the ticket-free URL; the cookie authorizes the
// @Description docs for 12h. Admin-only: session token + is_admin or
// @Description is_superadmin on the users record.
// @Tags        Admin
// @Produce     json
// @Security    BearerAuth
// @Success     200 {object} map[string]string "{url}"
// @Failure     401 {object} map[string]string
// @Failure     403 {object} map[string]string "admin only"
// @Failure     404 {object} map[string]string "dev docs not bundled"
// @Router      /api/admin/devdoc/ticket [post]
func docAdminDevdocTicket() {}

// adminListUsers returns every users record with its role/availability flags.
//
// @Summary     List users
// @Description Every users record: {users:[{id, name, email, github_login,
// @Description gitea_login, is_admin, is_superadmin, disabled, created,
// @Description updated}], total}.
// @Description Requires a session AND one of: is_admin, is_superadmin
// @Description (userAdminGuard — PATs rejected; the YAML admin lists are
// @Description only a boot-time seed for those flags).
// @Tags        Admin
// @Produce     json
// @Security    BearerAuth
// @Success     200 {object} map[string]interface{} "{users, total}"
// @Failure     401 {object} map[string]string
// @Failure     403 {object} map[string]string "user management requires admin / PAT auth not accepted"
// @Router      /api/admin/users [get]
func docAdminListUsers() {}

// adminUpdateUser toggles a user's availability and/or is_admin flag.
//
// @Summary     Update user flags
// @Description Body is a JSON object with any of {"disabled": bool,
// @Description "is_admin": bool} — at least one key required. disabled needs
// @Description admin-or-above; is_admin needs the is_superadmin flag.
// @Description Self-protection: nobody may disable
// @Description themselves or change their own is_admin; admins cannot
// @Description disable superadmins. is_superadmin is not patchable here.
// @Tags        Admin
// @Accept      json
// @Produce     json
// @Security    BearerAuth
// @Param       id path string true "users record id"
// @Success     200 {object} map[string]interface{} "the updated user record"
// @Failure     400 {object} map[string]string "empty or invalid body"
// @Failure     401 {object} map[string]string
// @Failure     403 {object} map[string]string "role too low / self-protection"
// @Failure     404 {object} map[string]string "user not found"
// @Router      /api/admin/users/{id} [patch]
func docAdminUpdateUser() {}

// --- OAuth Device Flow -------------------------------------------------------
//
// RFC 8628 device authorization grant used by `qatlas auth login --device`.
// Two of the five endpoints are anonymous (the CLI has no session); the
// other three require a browser session — PATs are explicitly rejected for
// the same reason RegisterPAT rejects them, so a leaked PAT cannot mint
// or approve further PATs.

// oauthDeviceCode starts a device-flow authorization request.
//
// @Summary     Start device authorization
// @Description RFC 8628 §3.1. Anonymous (no auth) — the CLI uses this to
// @Description obtain a device_code (kept secret, polled on /token) and a
// @Description short user_code that the human enters on the SPA's
// @Description /<lang>/device page after authenticating.
// @Tags        OAuth Device
// @Accept      json
// @Produce     json
// @Param       body body oauthDeviceCodeRequest true "PAT spec to mint after approval"
// @Success     200 {object} oauthDeviceCodeResponse
// @Failure     400 {object} map[string]string
// @Router      /api/oauth/device/code [post]
func docOAuthDeviceCode() {}

// oauthDeviceToken polls for the minted PAT after approval.
//
// @Summary     Poll for PAT after device approval
// @Description RFC 8628 §3.4 + §3.5. Anonymous. The CLI polls this with
// @Description device_code at the published interval until the server
// @Description returns the minted PAT plaintext (success) or an error
// @Description string ("authorization_pending", "slow_down", "expired_token",
// @Description "access_denied", "invalid_grant"). HTTP status is always
// @Description 400 on errors per the RFC so the CLI can switch on `error`.
// @Tags        OAuth Device
// @Accept      json
// @Produce     json
// @Param       body body oauthDeviceTokenRequest true "device_code from /code"
// @Success     200 {object} oauthDeviceTokenResponse "minted PAT plaintext (returned exactly once)"
// @Failure     400 {object} oauthDeviceTokenError "RFC 8628 error string"
// @Router      /api/oauth/device/token [post]
func docOAuthDeviceToken() {}

// oauthDeviceLookup resolves a user_code so the SPA can render the
// approval page.
//
// @Summary     Look up pending device request
// @Description Session-only (PAT auth rejected). Returns the PAT spec
// @Description (name, description, scopes, expires_in_days) tied to a
// @Description pending user_code so the /<lang>/device page can show the
// @Description user what they're about to approve.
// @Tags        OAuth Device
// @Produce     json
// @Security    BearerAuth
// @Param       user_code query string true "short user code (with or without dashes)"
// @Success     200 {object} oauthDeviceLookupResponse
// @Failure     400 {object} map[string]string "invalid user_code"
// @Failure     401 {object} map[string]string
// @Failure     404 {object} map[string]string "not found"
// @Router      /api/oauth/device/code [get]
func docOAuthDeviceLookup() {}

// oauthDeviceApprove approves a pending device request.
//
// @Summary     Approve a device request
// @Description Session-only (PAT auth rejected). Marks the pending request
// @Description as approved so the next /token poll mints the PAT bound to
// @Description the approving user.
// @Tags        OAuth Device
// @Accept      json
// @Produce     json
// @Security    BearerAuth
// @Param       body body oauthDeviceUserCodeBody true "user_code from /<lang>/device"
// @Success     200 {object} map[string]string "{\"status\":\"approved\"}"
// @Failure     400 {object} map[string]string
// @Failure     401 {object} map[string]string
// @Failure     404 {object} map[string]string
// @Router      /api/oauth/device/approve [post]
func docOAuthDeviceApprove() {}

// oauthDeviceDeny denies a pending device request.
//
// @Summary     Deny a device request
// @Description Session-only (PAT auth rejected). Marks the pending request
// @Description as denied so the next /token poll returns "access_denied".
// @Tags        OAuth Device
// @Accept      json
// @Produce     json
// @Security    BearerAuth
// @Param       body body oauthDeviceUserCodeBody true "user_code from /<lang>/device"
// @Success     200 {object} map[string]string "{\"status\":\"denied\"}"
// @Failure     400 {object} map[string]string
// @Failure     401 {object} map[string]string
// @Failure     404 {object} map[string]string
// @Router      /api/oauth/device/deny [post]
func docOAuthDeviceDeny() {}

// --- Admin: asset browser -----------------------------------------------------

// adminAssetList lists a paper's assets with object keys.
//
// @Summary     List paper assets
// @Description Admin-only asset listing: every PDF / markdown object the
// @Description paper owns, with S3 object keys, sizes, SHA256s.
// @Tags        Admin
// @Produce     json
// @Security    BearerAuth
// @Param       paper_id path string true "paper id (qa_...)"
// @Success     200 {object} map[string]interface{}
// @Failure     403 {object} map[string]string
// @Failure     404 {object} map[string]string
// @Router      /api/admin/assets/{paper_id} [get]
func docAdminAssetList() {}

// adminAssetDownload streams an asset as attachment.
//
// @Summary     Download paper asset
// @Description Admin-only proxy stream with Content-Disposition: attachment.
// @Tags        Admin
// @Produce     octet-stream
// @Security    BearerAuth
// @Param       paper_id path string true "paper id"
// @Param       kind path string true "pdf | markdown"
// @Success     200 {file} binary
// @Router      /api/admin/assets/{paper_id}/{kind}/download [get]
func docAdminAssetDownload() {}

// adminAssetURL returns a presigned S3 URL.
//
// @Summary     Presigned asset URL
// @Description Admin-only: time-limited presigned URL for direct browser-to-S3.
// @Tags        Admin
// @Produce     json
// @Security    BearerAuth
// @Param       paper_id path string true "paper id"
// @Param       kind path string true "pdf | markdown"
// @Param       ttl query string false "duration (default 1h, max 24h)"
// @Success     200 {object} map[string]interface{}
// @Router      /api/admin/assets/{paper_id}/{kind}/url [get]
func docAdminAssetURL() {}

// adminAssetSearch finds papers with assets.
//
// @Summary     Search papers with assets
// @Description Admin-only: find papers with at least one asset.
// @Tags        Admin
// @Produce     json
// @Security    BearerAuth
// @Param       q query string true "search query"
// @Success     200 {object} map[string]interface{}
// @Router      /api/admin/assets/search [get]
func docAdminAssetSearch() {}

// searchSurveyDoc is the bounded survey-search surface.
// searchSurvey godoc
// @Summary Plan and execute a bounded academic survey search
// @Description Results are anchored as metadata only, without downloads or conversion tasks; selected identifiers require POST /api/downloader/fetch.
// @Description Keywords, author/year/venue/citation rules and agentic planning are owned by qatlas-search. Rules filter a bounded retrieved set, not an exhaustive corpus. Per-user backend keys are injected by qatlasd and cannot be supplied by callers.
// @Tags search
// @Accept json
// @Produce json
// @Param request body object true "{goal, queries?, rules?: {authors?, venues?, year_from?, year_to?, min_citations?, sort?}, sources, max_results?, agentic?}; agentic=true uses existing daily quota"
// @Success 200 {object} map[string]interface{}
// @Failure 400 {object} map[string]string
// @Failure 401 {object} map[string]string
// @Failure 503 {object} map[string]string
// @Router /api/search/survey [post]
func searchSurveyDoc() {}

// --- RAG ----------------------------------------------------------------------

// ragRetrieve relays one semantic-retrieval query to the qatlas-rag
// microservice.
//
// @Summary     RAG retrieve (proxied)
// @Description Forwards the request body verbatim to qatlas-rag's
// @Description POST /v1/retrieve (the retrieve request/response schema
// @Description belongs to the qatlas-rag repository — qatlasd only
// @Description authenticates the caller and relays, so schema changes
// @Description never need a qatlasd release) and streams the reply back
// @Description as-is. Bodies are capped at 64 KiB. rag.remote disabled →
// @Description 503 "rag service not configured"; microservice
// @Description unreachable → 503 "rag unreachable: …"; a non-2xx
// @Description upstream reply is forwarded with its own status + body so
// @Description clients see the rag error schema. Requires the
// @Description papers:read scope.
// @Tags        RAG
// @Accept      json
// @Produce     json
// @Security    BearerAuth
// @Param       body body object true "retrieve query (qatlas-rag /v1/retrieve schema)"
// @Success     200 {object} map[string]interface{} "the microservice's reply, relayed verbatim"
// @Failure     400 {object} map[string]string "body read failed"
// @Failure     401 {object} map[string]string
// @Failure     403 {object} map[string]string
// @Failure     413 {object} map[string]string "body exceeds 64 KiB"
// @Failure     503 {object} map[string]string "rag.remote disabled / microservice unreachable"
// @Router      /api/rag/retrieve [post]
func docRagRetrieve() {}

// ragEvidence relays one evidence lookup to the qatlas-rag microservice.
//
// @Summary     RAG evidence (proxied)
// @Description Same relay contract as /api/rag/retrieve but targeting
// @Description qatlas-rag's POST /v1/evidence: body forwarded verbatim,
// @Description reply streamed back as-is, 64 KiB cap, upstream errors
// @Description forwarded with their own status. Requires the papers:read
// @Description scope.
// @Tags        RAG
// @Accept      json
// @Produce     json
// @Security    BearerAuth
// @Param       body body object true "evidence query (qatlas-rag /v1/evidence schema)"
// @Success     200 {object} map[string]interface{} "the microservice's reply, relayed verbatim"
// @Failure     400 {object} map[string]string "body read failed"
// @Failure     401 {object} map[string]string
// @Failure     403 {object} map[string]string
// @Failure     413 {object} map[string]string "body exceeds 64 KiB"
// @Failure     503 {object} map[string]string "rag.remote disabled / microservice unreachable"
// @Router      /api/rag/evidence [post]
func docRagEvidence() {}

// --- Comments (Q2 block-level comments, plan §12.2) ---------------------------

// commentsListDiscussions returns the Issue-style discussion list of one
// paper, newest first, keyset-paginated by discussion_id.
//
// @Summary     List block discussions
// @Description Issue-style discussion list for one paper, filtered by
// @Description anchor (parse_revision / page_idx / block_index), content
// @Description scope, type and status. Ordering is newest-first; the
// @Description cursor is the last discussion_id of the page (keyset).
// @Description scope=lean returns public+lean (the Lean view includes
// @Description shared public discussion); status=none selects statusless
// @Description notes. per_page defaults to 20, max 100. Requires
// @Description papers:read or comments:read; system PATs may read.
// @Tags        Comments
// @Produce     json
// @Security    BearerAuth
// @Param       paper_id path string true "qa_ paper id"
// @Param       scope query string false "public|lean (lean includes public)"
// @Param       type query string false "type slug, e.g. transcription_error"
// @Param       status query string false "pending|confirmed|retracted|none"
// @Param       parse_revision query string false "parse revision id"
// @Param       page_idx query int false "0-based page index"
// @Param       block_index query int false "1-based public block index"
// @Param       cursor query string false "keyset cursor (last discussion_id)"
// @Param       per_page query int false "page size (default 20, max 100)"
// @Success     200 {object} map[string]interface{}
// @Failure     400 {object} map[string]string
// @Failure     401 {object} map[string]string
// @Failure     403 {object} map[string]string
// @Failure     503 {object} map[string]string "comment store unavailable"
// @Router      /api/papers/{paper_id}/discussions [get]
func docCommentsListDiscussions() {}

// commentsCreateDiscussion creates a root discussion bound to one parse
// block anchor.
//
// @Summary     Create a discussion
// @Description Creates a root discussion anchored at (parse_revision,
// @Description page_idx, block_index) of the paper. actor and timestamps
// @Description are recorded server-side; the optional `model` string is a
// @Description pure client declaration. status may be set at creation
// @Description (then `reason` is required) or omitted for a statusless
// @Description note. Bodies are capped at comments.max_body_chars (default
// @Description 20000 Unicode chars, 413 beyond, never truncated); requests
// @Description are capped at 1 MiB. Anchor misses 404 — never a
// @Description nearest-block fallback. Idempotency-Key: same key + same
// @Description request replays the original result; different request
// @Description 409s. Requires a user identity (session or user PAT) with
// @Description comments:write — system PATs are read-only and get 403.
// @Tags        Comments
// @Accept      json
// @Produce     json
// @Security    BearerAuth
// @Param       paper_id path string true "qa_ paper id"
// @Param       Idempotency-Key header string false "replay key (SHA-256 of method+path+body compared)"
// @Param       body body object true "{parse_revision, page_idx, block_index, type?, scope?, status?, reason?, body, model?}"
// @Success     201 {object} map[string]interface{}
// @Failure     400 {object} map[string]string
// @Failure     401 {object} map[string]string
// @Failure     403 {object} map[string]string "system PAT / missing comments:write"
// @Failure     404 {object} map[string]string "anchor block not found in parse revision"
// @Failure     409 {object} map[string]string "idempotency key reused with different body"
// @Failure     413 {object} map[string]string "body / request over limit"
// @Failure     503 {object} map[string]string
// @Router      /api/papers/{paper_id}/discussions [post]
func docCommentsCreateDiscussion() {}

// commentsGetDiscussion returns one discussion with a replies page.
//
// @Summary     Discussion detail
// @Description The discussion plus the first (or cursor-continued) page
// @Description of replies, oldest first, keyset-paginated by reply_id.
// @Description The ETag response header carries the body revision integer
// @Description used for If-Match CAS edits.
// @Tags        Comments
// @Produce     json
// @Security    BearerAuth
// @Param       discussion_id path string true "cd_ discussion id"
// @Param       cursor query string false "keyset cursor (last reply_id)"
// @Param       per_page query int false "replies page size (default 20, max 100)"
// @Success     200 {object} map[string]interface{}
// @Failure     401 {object} map[string]string
// @Failure     403 {object} map[string]string
// @Failure     404 {object} map[string]string
// @Failure     503 {object} map[string]string
// @Router      /api/discussions/{discussion_id} [get]
func docCommentsGetDiscussion() {}

// commentsCreateReply replies to a discussion.
//
// @Summary     Reply to a discussion
// @Description Flat reply inside the discussion. Same identity/idempotency/
// @Description body-budget contract as discussion creation.
// @Tags        Comments
// @Accept      json
// @Produce     json
// @Security    BearerAuth
// @Param       discussion_id path string true "cd_ discussion id"
// @Param       Idempotency-Key header string false "replay key"
// @Param       body body object true "{body, model?}"
// @Success     201 {object} map[string]interface{}
// @Failure     400 {object} map[string]string
// @Failure     401 {object} map[string]string
// @Failure     403 {object} map[string]string
// @Failure     404 {object} map[string]string
// @Failure     409 {object} map[string]string
// @Failure     413 {object} map[string]string
// @Failure     503 {object} map[string]string
// @Router      /api/discussions/{discussion_id}/replies [post]
func docCommentsCreateReply() {}

// commentsSetStatus transitions the discussion status.
//
// @Summary     Change discussion status
// @Description Sets the status machine (pending / confirmed / retracted;
// @Description reopen allowed). `reason` is mandatory and every change is
// @Description appended to the status-event trail with the acting account.
// @Description Allowed for the discussion's root author or a platform
// @Description admin (users-record is_admin / is_superadmin — a user PAT
// @Description of an admin works; this is NOT the session-only admin
// @Description console gate). System PATs and everyone else get 403.
// @Tags        Comments
// @Accept      json
// @Produce     json
// @Security    BearerAuth
// @Param       discussion_id path string true "cd_ discussion id"
// @Param       body body object true "{status, reason}"
// @Success     200 {object} map[string]interface{}
// @Failure     400 {object} map[string]string "missing reason / bad status"
// @Failure     401 {object} map[string]string
// @Failure     403 {object} map[string]string
// @Failure     404 {object} map[string]string
// @Failure     503 {object} map[string]string
// @Router      /api/discussions/{discussion_id}/status [patch]
func docCommentsSetStatus() {}

// commentsEditDiscussionBody edits the root body with CAS.
//
// @Summary     Edit discussion body
// @Description Author-only edit (the caller must be the body's author —
// @Description admins do not edit other people's words). Requires
// @Description If-Match with the current revision integer; a stale value
// @Description is a 409, the new body increments the revision and the
// @Description old/new pair is appended to the revision history. Editing
// @Description never reopens the status.
// @Tags        Comments
// @Accept      json
// @Produce     json
// @Security    BearerAuth
// @Param       discussion_id path string true "cd_ discussion id"
// @Param       If-Match header string true "current revision integer (the ETag)"
// @Param       body body object true "{body}"
// @Success     200 {object} map[string]interface{}
// @Failure     400 {object} map[string]string "missing/invalid If-Match"
// @Failure     401 {object} map[string]string
// @Failure     403 {object} map[string]string "not the author"
// @Failure     404 {object} map[string]string
// @Failure     409 {object} map[string]string "revision conflict"
// @Failure     413 {object} map[string]string
// @Failure     503 {object} map[string]string
// @Router      /api/discussions/{discussion_id}/body [patch]
func docCommentsEditDiscussionBody() {}

// commentsEditReplyBody edits a reply body with CAS.
//
// @Summary     Edit reply body
// @Description Same author-only CAS contract as the discussion body edit,
// @Description applied to one reply of the discussion.
// @Tags        Comments
// @Accept      json
// @Produce     json
// @Security    BearerAuth
// @Param       discussion_id path string true "cd_ discussion id"
// @Param       reply_id path string true "cr_ reply id"
// @Param       If-Match header string true "current revision integer"
// @Param       body body object true "{body}"
// @Success     200 {object} map[string]interface{}
// @Failure     400 {object} map[string]string
// @Failure     401 {object} map[string]string
// @Failure     403 {object} map[string]string "not the author"
// @Failure     404 {object} map[string]string
// @Failure     409 {object} map[string]string "revision conflict"
// @Failure     413 {object} map[string]string
// @Failure     503 {object} map[string]string
// @Router      /api/discussions/{discussion_id}/replies/{reply_id}/body [patch]
func docCommentsEditReplyBody() {}

// commentsRevisions returns the revision and status history.
//
// @Summary     Discussion history
// @Description Append-only trail of one discussion: body_revisions (the
// @Description discussion's and its replies' edits: target / target_id /
// @Description old_body / new_body / editor / created_at) and
// @Description status_events (from / to / reason / actor / created_at),
// @Description both oldest-first.
// @Tags        Comments
// @Produce     json
// @Security    BearerAuth
// @Param       discussion_id path string true "cd_ discussion id"
// @Success     200 {object} map[string]interface{}
// @Failure     401 {object} map[string]string
// @Failure     403 {object} map[string]string
// @Failure     404 {object} map[string]string
// @Failure     503 {object} map[string]string
// @Router      /api/discussions/{discussion_id}/revisions [get]
func docCommentsRevisions() {}
