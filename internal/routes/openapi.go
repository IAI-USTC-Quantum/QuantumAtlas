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
// path entries. The CI drift-guard (see the `swagger` pixi task and the
// generate-and-diff check) regenerates internal/apidocs and fails if these
// annotations and the committed spec disagree, so the spec can never silently
// fall behind the annotations. Keeping the annotations correct relative to the
// actual handler behavior remains a review-time discipline (true of swaggo on
// any router — it never introspects handler bodies).
//
// Each stub is grouped by @Tags matching its source file. When you add or
// change a route, update the matching stub here and run `pixi run swagger`.

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

// serverInfo returns the server mode, version and engine.
//
// @Summary     Server info
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
// @Description papers carry created=true and are picked up by the lazy
// @Description ingestion pipeline; title-only hits return as un-minted
// @Description candidates. Requires the papers:read scope.
// @Tags        Search
// @Accept      json
// @Produce     json
// @Security    BearerAuth
// @Param       body body object true "search entry {text?, title?, doi?, arxiv_id?, max_results?, required_phrases?}"
// @Success     200 {object} map[string]interface{} "{results:[{paper_id,hit,created}], candidates:[...]}"
// @Failure     400 {object} map[string]string
// @Failure     401 {object} map[string]string
// @Failure     403 {object} map[string]string
// @Failure     503 {object} map[string]string "registry unavailable"
// @Router      /api/search [post]
func docSearchPapers() {}

// multiSearch runs the per-backend ("multi") search.
//
// @Summary     Multi-backend search (per-platform raw results)
// @Description Proxies one mode="multi" call to the qatlas-search
// @Description microservice: every requested backend returns its own raw
// @Description hit list (the source's own order), with NO cross-backend
// @Description merge or ranking. The caller's stored third-party API keys
// @Description (configured in the dashboard) are decrypted and forwarded so
// @Description key-requiring backends run under the user's credentials.
// @Description Requires the papers:read scope; 503 when search.remote is
// @Description disabled; 502 when the microservice call fails.
// @Tags        Search
// @Accept      json
// @Produce     json
// @Security    BearerAuth
// @Param       body body object true "{text, max_results?, sources: [backend names]}"
// @Success     200 {object} map[string]interface{} "{results: {backend: [hits]}, usage, errors: {backend: msg}, remote: true}"
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

// papersList returns the paginated registry paper list.
//
// @Summary     List papers
// @Description Paginated list of registry papers (merged tombstones
// @Description excluded). has_md filters on converted markdown present on
// @Description the paper's default asset (the "converted papers" page);
// @Description status filters by lifecycle; q is a case-insensitive title
// @Description substring. Sorted by created_at (default) or updated_at,
// @Description descending. Requires the papers:read scope.
// @Tags        Papers
// @Produce     json
// @Security    BearerAuth
// @Param       has_md   query bool   false "true = only papers with converted markdown on the default asset"
// @Param       status   query string false "pending | ready | failed"
// @Param       q        query string false "title substring (case-insensitive)"
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
// @Description asset rows for the surrogate paper_id ("qa_" + ULID).
// @Tags        Papers
// @Produce     json
// @Security    BearerAuth
// @Param       paper_id path string true "surrogate paper id (qa_...)"
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
// @Description corpus. Returns per-ref {ref,title,authors,year,hosted,resolved}
// @Description plus corpus_available. Exact-by-id only; fuzzy search is a
// @Description separate deferred capability.
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
// @Description against the IANA prefix `10.<registrant>/...`). Only
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
// @Failure     503 {object} map[string]interface{} "cache-only mode (no MinerU keys), or DOI resolution unavailable (QATLAS_OPENALEX_MAILTO unset)"
// @Router      /api/papers/{id_or_doi}/markdown [get]
func docPaperMarkdown() {}

// paperMarkdownStatus reports current markdown / conversion state.
//
// @Summary     Get markdown conversion status
// @Description Side-effect-free poll surface. Never starts a job and
// @Description never triggers a fetch. Only registered when
// @Description QATLAS_PAPER_ACCESS_ENABLED=true on the server.
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
// @Description /api/papers/{id_or_doi}/markdown instead. Only
// @Description registered when QATLAS_PAPER_ACCESS_ENABLED=true on the
// @Description server.
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
// @Description 410 Gone); the body no longer carries a pdf_url. Only
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
// @Router      /api/papers/{id_or_doi}/pdf/status [get]
func docPaperPDFStatus() {}

// paperImagesZip downloads the paper's images bundle (one zip produced
// by the MinerU conversion).
//
// @Summary     Get paper images zip
// @Description Returns the images zip (application/zip) for the given
// @Description arxiv id or DOI — the bundle the MinerU conversion
// @Description produced alongside the markdown. Only registered when
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
// Admin console API. Session-token auth only (PATs rejected); the db
// schema endpoint additionally requires the caller's github_login /
// gitea_login to be on the matching config admin allowlist
// (auth.admin_logins / auth.gitea_admin_logins). See internal/routes/admin.go.

// adminWhoami reports the caller's provider login and admin status.
//
// @Summary     Admin whoami
// @Description Returns {login, is_admin, is_user_admin, is_superadmin}
// @Description for the signed-in session user. login is the github_login,
// @Description falling back to gitea_login when only that is stamped.
// @Description is_admin is the config allowlist gate (ops dashboard);
// @Description is_user_admin / is_superadmin mirror the /api/admin/users
// @Description guard for the user-management nav. The SPA uses these to
// @Description decide which admin surfaces to render; non-admins get false
// @Description flags rather than a 403. Session-token auth only (PAT auth
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
// @Description Admin-only: session token + github_login on the
// @Description QATLAS_ADMIN_GITHUB_LOGINS allowlist.
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
// @Description docs for 12h. Admin-only: session token + github_login on the
// @Description QATLAS_ADMIN_GITHUB_LOGINS allowlist.
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
// @Description Requires a session AND one of: is_admin, is_superadmin, or the
// @Description config admin allowlist (userAdminGuard — PATs rejected).
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
// @Description admin-or-above; is_admin needs superadmin (env-allowlist admins
// @Description count as superadmin). Self-protection: nobody may disable
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
