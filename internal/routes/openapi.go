package routes

// OpenAPI / Swagger annotations.
//
// PocketBase registers routes as closures on se.Router (see RegisterWiki,
// RegisterGraph, RegisterShares, RegisterPAT, RegisterPapers and the
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

// lint returns wiki lint issues (placeholder: not yet ported to Go).
//
// @Summary     Lint wiki
// @Tags        Wiki
// @Produce     json
// @Success     200 {object} map[string]interface{}
// @Security    BearerAuth
// @Failure     401 {object} map[string]string
// @Failure     403 {object} map[string]string
// @Router      /api/lint [get]
func docLint() {}

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

// paperResources returns the resource manifest (pdf/md/json) for one paper.
//
// @Summary     Paper resources
// @Tags        Papers
// @Produce     json
// @Param       arxiv_id path string true "arXiv identifier"
// @Success     200 {object} map[string]interface{}
// @Failure     404 {object} map[string]string
// @Security    BearerAuth
// @Failure     401 {object} map[string]string
// @Router      /api/papers/{arxiv_id}/resources [get]
func docPaperResources() {}

// paperMarkdown returns the converted markdown for one paper.
//
// @Summary     Paper markdown
// @Tags        Papers
// @Produce     json
// @Param       arxiv_id path string true "arXiv identifier"
// @Success     200 {object} map[string]interface{}
// @Failure     404 {object} map[string]string
// @Security    BearerAuth
// @Failure     401 {object} map[string]string
// @Router      /api/papers/{arxiv_id}/markdown [get]
func docPaperMarkdown() {}

// paperMarkdownStatus reports whether server-side conversion is in progress.
//
// @Summary     Paper markdown status
// @Tags        Papers
// @Produce     json
// @Param       arxiv_id path string true "arXiv identifier"
// @Success     200 {object} map[string]interface{}
// @Security    BearerAuth
// @Failure     401 {object} map[string]string
// @Router      /api/papers/{arxiv_id}/markdown/status [get]
func docPaperMarkdownStatus() {}

// uploadPDF stores a paper PDF.
//
// @Summary     Upload paper PDF
// @Description Content-addressed upload with sha256 idempotency. 200 when
// @Description bytes are unchanged, 201 when written, 409 on a content
// @Description conflict without overwrite=true.
// @Tags        Papers
// @Accept      mpfd
// @Produce     json
// @Security    BearerAuth
// @Param       arxiv_id        path     string true  "arXiv identifier"
// @Param       overwrite       query    bool   false "overwrite on content conflict"
// @Param       expected_sha256 query    string false "client-computed PDF sha256 (in-transit guard)"
// @Param       pdf             formData file   true  "PDF file"
// @Success     201 {object} map[string]interface{} "created"
// @Success     200 {object} map[string]interface{} "unchanged"
// @Failure     400 {object} map[string]interface{}
// @Failure     409 {object} map[string]interface{}
// @Router      /api/papers/{arxiv_id}/upload-pdf [post]
func docUploadPDF() {}

// uploadMineRU stores a MinerU result zip (markdown + images bundle) for a paper.
//
// @Summary     Upload paper MinerU bundle
// @Description Accepts the entire MinerU result zip exactly as returned by `full_zip_url`. Server extracts `full.md` plus every `images/*` entry and stores them to the markdown and images object buckets respectively. Images are written before the markdown so any reader that observes the markdown also observes all referenced images. Replaces the v0.7.x `upload-markdown` endpoint (which only accepted a single .md file and silently dropped images).
// @Tags        Papers
// @Accept      mpfd
// @Produce     json
// @Security    BearerAuth
// @Param       arxiv_id        path     string true  "arXiv identifier (must include version suffix vN)"
// @Param       overwrite       query    bool   false "overwrite on content conflict"
// @Param       expected_sha256 query    string false "client-computed zip sha256 (in-transit integrity check)"
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
// @Success     200 {object} map[string]interface{}
// @Router      /api/papers/{arxiv_id}/mineru-claim [post]
func docMineruClaim() {}

// mineruClaimRelease releases a previously acquired MinerU claim.
//
// @Summary     Release MinerU claim
// @Tags        Papers
// @Produce     json
// @Security    BearerAuth
// @Param       arxiv_id path string true "arXiv identifier"
// @Param       claim_id path string true "claim id"
// @Success     200 {object} map[string]interface{}
// @Failure     404 {object} map[string]string
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

// --- Shares ------------------------------------------------------------------

// createShare mints a share token for one or more asset paths.
//
// @Summary     Create share
// @Tags        Shares
// @Accept      json
// @Produce     json
// @Security    BearerAuth
// @Param       body body object true "{paths: []string, label: string, expires_in: int}"
// @Success     200 {object} map[string]interface{}
// @Failure     400 {object} map[string]string
// @Router      /api/shares/ [post]
func docCreateShare() {}

// listShares lists all share records.
//
// @Summary     List shares
// @Tags        Shares
// @Produce     json
// @Security    BearerAuth
// @Success     200 {object} map[string]interface{}
// @Router      /api/shares/ [get]
func docListShares() {}

// deleteShare revokes a share token.
//
// @Summary     Delete share
// @Tags        Shares
// @Produce     json
// @Security    BearerAuth
// @Param       token path string true "share token"
// @Success     200 {object} map[string]bool
// @Failure     404 {object} map[string]string
// @Router      /api/shares/{token} [delete]
func docDeleteShare() {}

// publicShare serves a shared asset (or its index) without auth.
//
// @Summary     Public share access
// @Description Public download surface. /share/{token} shows the index;
// @Description /share/{token}/{path} serves (or 307-redirects to a presigned
// @Description URL for) the asset. No auth — the token IS the credential.
// @Tags        Shares
// @Param       token path string true "share token"
// @Success     200 {string} string "asset bytes or index html"
// @Success     307 {string} string "redirect to presigned URL"
// @Failure     404 {object} map[string]string
// @Failure     410 {object} map[string]string "expired"
// @Router      /share/{token} [get]
func docPublicShare() {}

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
