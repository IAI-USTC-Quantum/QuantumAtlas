package routes

// OpenAPI / Swagger annotations.
//
// PocketBase registers routes as closures on se.Router (see RegisterWiki,
// RegisterGraph, RegisterPAT, RegisterPapers and the
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
// @Description Liveness plus parallel dependency probes (rawstore, neo4j,
// @Description wiki). HTTP status is always 200; read data.status for the
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

// --- Wiki --------------------------------------------------------------------

// listPages lists browsable wiki entries (source pages excluded by default).
//
// @Summary     List wiki pages
// @Description Lists wiki entries. Source pages (processed papers) are
// @Description treated as citations and excluded unless page_type=source.
// @Tags        Wiki
// @Produce     json
// @Param       page_type query string false "filter by type (concept|entity|comparison|source)"
// @Param       status    query string false "filter by status"
// @Param       tags      query string false "comma-separated tag filter"
// @Success     200 {object} map[string]interface{}
// @Security    BearerAuth
// @Failure     401 {object} map[string]string
// @Failure     403 {object} map[string]string
// @Router      /api/pages [get]
func docListPages() {}

// getPage returns a single wiki page including its markdown content.
//
// @Summary     Get wiki page
// @Tags        Wiki
// @Produce     json
// @Param       page_id path string true "page id"
// @Success     200 {object} map[string]interface{}
// @Failure     404 {object} map[string]string
// @Security    BearerAuth
// @Failure     401 {object} map[string]string
// @Router      /api/pages/{page_id} [get]
func docGetPage() {}

// wikiStats returns aggregate counts over the wiki corpus.
//
// @Summary     Wiki statistics
// @Tags        Wiki
// @Produce     json
// @Success     200 {object} map[string]interface{}
// @Security    BearerAuth
// @Failure     401 {object} map[string]string
// @Failure     403 {object} map[string]string
// @Router      /api/stats [get]
func docWikiStats() {}

// searchWiki does a full-text search over wiki entries.
//
// @Summary     Search wiki
// @Tags        Wiki
// @Produce     json
// @Param       q               query string true  "query string"
// @Param       limit           query int    false "max results (default 10)"
// @Param       include_sources query bool   false "include source pages (default false)"
// @Success     200 {object} map[string]interface{}
// @Security    BearerAuth
// @Failure     401 {object} map[string]string
// @Failure     403 {object} map[string]string
// @Router      /api/search [get]
func docSearchWiki() {}

// wikiSyncStatusOp reports the wiki git HEAD / ahead / behind.
//
// @Summary     Wiki sync status
// @Description Branch/commit/ahead/behind of the server's wiki checkout.
// @Description Requires the wiki:read scope (the knowledge base is not
// @Description anonymously readable).
// @Tags        Wiki
// @Produce     json
// @Security    BearerAuth
// @Success     200 {object} map[string]interface{}
// @Failure     401 {object} map[string]string
// @Failure     403 {object} map[string]string
// @Router      /api/wiki/sync/status [get]
func docWikiSyncStatus() {}

// wikiSyncPull fast-forwards the server's wiki checkout and refreshes cache.
//
// @Summary     Wiki sync pull
// @Description Runs `git fetch --prune` + `git pull --ff-only` on the server
// @Description wiki checkout, then refreshes the in-memory cache. Mutates
// @Description server state, so it requires the wiki:write scope.
// @Tags        Wiki
// @Produce     json
// @Security    BearerAuth
// @Success     200 {object} map[string]interface{}
// @Failure     401 {object} map[string]string
// @Failure     403 {object} map[string]string
// @Failure     409 {object} map[string]string "non-fast-forward / wiki dir missing"
// @Router      /api/wiki/sync/pull [post]
func docWikiSyncPull() {}

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

// paperResources stanzas were removed in v0.9.0 — the server no longer
// serves PDF or image bytes outbound by default. paperMarkdown /
// paperMarkdownStatus / paperPDF / paperPDFStatus are conditional on
// QATLAS_PAPER_ACCESS_ENABLED=true (default off). See the
// RegisterPapers doc comment for the compliance rationale.

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
// @Description docs/reference/upload-api.md §Canonical resolution.
// @Description
// @Description Long-running operation semantics: on cache miss the
// @Description server may transparently fetch the PDF from arxiv.org
// @Description (silent_fetch) and trigger a MinerU conversion. The
// @Description first call returns 202 with `Operation-Location:
// @Description /api/papers/{id}/markdown/status` and `Retry-After: 5`;
// @Description clients poll the status endpoint until state=cached then
// @Description re-GET this resource for the bytes.
// @Tags        Papers
// @Produce     plain
// @Security    BearerAuth
// @Param       id_or_doi path string true "arXiv canonical id with vN suffix, or a DOI (e.g. 10.1103/PhysRevLett.103.150502)"
// @Param       force_arxiv query string false "1/true: bypass the DOI-canonical default and serve the arxiv twin (or 409 if no twin exists)"
// @Success     200 {string} string "markdown bytes (text/markdown)"
// @Success     202 {object} map[string]interface{} "long-running operation started; poll status_url"
// @Failure     400 {object} map[string]string "invalid arxiv_id or DOI"
// @Failure     401 {object} map[string]string
// @Failure     403 {object} map[string]string
// @Failure     404 {object} map[string]interface{} "DOI not in OpenAlex / not on arxiv; or paper unknown and silent fetch unavailable"
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

// paperPDF serves the cached PDF bytes for a paper, with silent
// fetch from arxiv.org on cache miss.
//
// @Summary     Get paper PDF
// @Description Returns the cached PDF (application/pdf) for the given
// @Description arxiv id or DOI. Only registered when
// @Description QATLAS_PAPER_ACCESS_ENABLED=true on the server.
// @Description
// @Description Canonical resolution: a DOI contribution ALWAYS wins
// @Description over its arxiv twin when both exist — the dispatcher
// @Description serves the DOI PDF for either id form. Pass
// @Description `?force_arxiv=1` to opt out per request (DOI input
// @Description without an arxiv twin then returns 409). See
// @Description docs/reference/upload-api.md §Canonical resolution.
// @Description
// @Description Long-running operation semantics mirror /markdown: cache
// @Description miss returns 202 with Operation-Location pointing at
// @Description /pdf/status. The fetch path uses a separate semaphore
// @Description from MinerU conversion (QATLAS_ARXIV_FETCH_CONCURRENT)
// @Description and a polite-pool rate limiter (QATLAS_ARXIV_FETCH_RPS).
// @Tags        Papers
// @Produce     application/pdf
// @Security    BearerAuth
// @Param       id_or_doi path string true "arXiv canonical id with vN suffix, or a DOI"
// @Param       force_arxiv query string false "1/true: bypass DOI-canonical default; return 409 if DOI has no arxiv twin"
// @Success     200 {string} string "PDF bytes (application/pdf)"
// @Success     202 {object} map[string]interface{} "silent fetch started; poll status_url"
// @Failure     400 {object} map[string]string "invalid arxiv_id or DOI"
// @Failure     401 {object} map[string]string
// @Failure     403 {object} map[string]string
// @Failure     404 {object} map[string]interface{} "arxiv 404 or DOI not on arxiv"
// @Failure     409 {object} map[string]interface{} "force_arxiv requested but DOI has no arxiv twin"
// @Failure     502 {object} map[string]interface{} "arxiv upstream error / OpenAlex upstream error"
// @Failure     503 {object} map[string]interface{} "silent fetch disabled (no fetcher), DOI resolution unavailable"
// @Router      /api/papers/{id_or_doi}/pdf [get]
func docPaperPDF() {}

// paperPDFStatus reports current PDF / fetch state.
//
// @Summary     Get PDF fetch status
// @Description Side-effect-free poll surface for /pdf. Same shape as
// @Description /markdown/status but states are restricted to the
// @Description fetch-only flow — no convert phase. Only registered when
// @Description QATLAS_PAPER_ACCESS_ENABLED=true.
// @Description
// @Description Canonical resolution: same DOI-wins rule as
// @Description /api/papers/{id_or_doi}/pdf. Pass `?force_arxiv=1` to
// @Description query the arxiv-side status instead.
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
// @Description namespace and cross-checked against the DOI's OpenAlex metadata:
// @Description pass `title` and/or `authors` (semicolon-separated) form fields
// @Description to verify. The outcome is reported in `X-QAtlas-Verification` and
// @Description the JSON `verification` block; `verify=strict` rejects a mismatch
// @Description or unknown DOI with 409.
// @Tags        Papers
// @Accept      mpfd
// @Produce     json
// @Security    BearerAuth
// @Param       arxiv_id        path     string true  "arXiv identifier (with vN) OR DOI (10.x/...)"
// @Param       overwrite       query    bool   false "overwrite on content conflict"
// @Param       expected_sha256 query    string false "client-computed PDF sha256 (in-transit guard)"
// @Param       verify          query    string false "DOI only: 'strict' rejects metadata mismatch (default warn)"
// @Param       pdf             formData file   true  "PDF file"
// @Param       title           formData string false "DOI only: expected paper title to verify against OpenAlex"
// @Param       authors         formData string false "DOI only: expected authors (semicolon-separated) to verify"
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
// @Description The {arxiv_id} slot also accepts a DOI (`10.<registrant>/<suffix>`) to contribute the converted *published* version. DOI bundles are stored under the `markdown/doi/...` + `images/doi/...` namespace and verified against OpenAlex metadata via the `title`/`authors` form fields (see upload-pdf; `verify=strict` rejects mismatches). The result is reported in `X-QAtlas-Verification`.
// @Tags        Papers
// @Accept      mpfd
// @Produce     json
// @Security    BearerAuth
// @Param       arxiv_id        path     string true  "arXiv identifier (with vN) OR DOI (10.x/...)"
// @Param       overwrite       query    bool   false "overwrite on content conflict"
// @Param       expected_sha256 query    string false "client-computed zip sha256 (in-transit integrity check)"
// @Param       pdf_sha256      query    string false "sha256 of the source PDF that was converted (cross-checked against stored PDF)"
// @Param       verify          query    string false "DOI only: 'strict' rejects metadata mismatch (default warn)"
// @Param       source          query    string false "short label of the contributor's MinerU run (truncated to 64 chars)"
// @Param       mineru_zip      formData file   true  "MinerU result zip (must contain full.md; optional images/*)"
// @Param       title           formData string false "DOI only: expected paper title to verify against OpenAlex"
// @Param       authors         formData string false "DOI only: expected authors (semicolon-separated) to verify"
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
// @Failure     503 {object} map[string]string      "catalog unavailable (Neo4j unreachable)"
// @Router      /api/papers/{arxiv_id}/mineru-claim [post]
func docMineruClaim() {}

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
// @Failure     503 {object} map[string]string "catalog unavailable (Neo4j unreachable)"
// @Router      /api/papers/{arxiv_id}/mineru-claim/{claim_id} [delete]
func docMineruClaimRelease() {}

// --- Graph -------------------------------------------------------------------

// graphStats returns node/relationship counts from Neo4j.
//
// @Summary     Graph statistics
// @Description Returns 200 with {"error":...} when Neo4j is unreachable
// @Description (the UI renders a friendly banner rather than a crash page).
// @Description Requires the graph:read scope.
// @Tags        Graph
// @Produce     json
// @Security    BearerAuth
// @Success     200 {object} map[string]interface{}
// @Failure     401 {object} map[string]string
// @Failure     403 {object} map[string]string
// @Router      /api/graph/stats [get]
func docGraphStats() {}

// graphQuery runs a read-only Cypher query.
//
// @Summary     Graph query
// @Description Executes caller-supplied read-only Cypher. graph:read scope.
// @Tags        Graph
// @Accept      json
// @Produce     json
// @Security    BearerAuth
// @Param       body body object true "{query: string, limit: int}"
// @Success     200 {object} map[string]interface{}
// @Failure     400 {object} map[string]string
// @Router      /api/graph/query [post]
func docGraphQuery() {}

// graphSchema returns the Neo4j labels and relationship types.
//
// @Summary     Graph schema
// @Description Requires the graph:read scope.
// @Tags        Graph
// @Produce     json
// @Security    BearerAuth
// @Success     200 {object} map[string]interface{}
// @Failure     401 {object} map[string]string
// @Failure     403 {object} map[string]string
// @Router      /api/graph/schema [get]
func docGraphSchema() {}

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
