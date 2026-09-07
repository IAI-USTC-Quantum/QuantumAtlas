export type PaperStats = {
  available: boolean
  total?: number
  pending?: number
  ready?: number
  failed?: number
  converted_markdown?: number
}

import { pb } from './pb'

// Attach the current PocketBase auth token (if any) to outbound fetches so
// that protected /api/* endpoints accept us. Reads via the SDK so token
// rotation (authRefresh) is picked up automatically.
function authHeaders(): Record<string, string> {
  const token = pb.authStore.token
  return token ? { Authorization: `Bearer ${token}` } : {}
}

export async function getJson<T>(url: string): Promise<T> {
  const response = await fetch(url, { headers: { ...authHeaders() } })
  if (!response.ok) {
    throw new Error(`${response.status} ${response.statusText}`)
  }
  return response.json() as Promise<T>
}

export async function putJson<T>(url: string, body: unknown): Promise<T> {
  const response = await fetch(url, {
    method: 'PUT',
    headers: { ...authHeaders(), 'content-type': 'application/json' },
    body: JSON.stringify(body),
  })
  if (!response.ok) {
    let detail = ''
    try {
      const j = (await response.json()) as { detail?: string }
      detail = j.detail ?? ''
    } catch {
      // not JSON; ignore
    }
    throw new Error(detail ? `${response.status}: ${detail}` : `${response.status} ${response.statusText}`)
  }
  return response.json() as Promise<T>
}

// Same error-surfacing contract as putJson, for the PATCH verbs on the
// admin user-management surface.
export async function patchJson<T>(url: string, body: unknown): Promise<T> {
  const response = await fetch(url, {
    method: 'PATCH',
    headers: { ...authHeaders(), 'content-type': 'application/json' },
    body: JSON.stringify(body),
  })
  if (!response.ok) {
    let detail = ''
    try {
      const j = (await response.json()) as { detail?: string }
      detail = j.detail ?? ''
    } catch {
      // not JSON; ignore
    }
    throw new Error(detail ? `${response.status}: ${detail}` : `${response.status} ${response.statusText}`)
  }
  return response.json() as Promise<T>
}

export async function postJson<T>(url: string, body: unknown): Promise<T> {
  const response = await fetch(url, {
    method: 'POST',
    headers: { ...authHeaders(), 'content-type': 'application/json' },
    body: JSON.stringify(body),
  })
  if (!response.ok) {
    // Try to surface the server-side detail (qatlasd returns
    // {"detail": "..."} on 4xx/5xx). Fall back to bare status text.
    let detail = ''
    try {
      const j = (await response.json()) as { detail?: string }
      detail = j.detail ?? ''
    } catch {
      // not JSON; ignore
    }
    throw new Error(detail ? `${response.status}: ${detail}` : `${response.status} ${response.statusText}`)
  }
  return response.json() as Promise<T>
}

// --- Paper search (POST /api/search) ---------------------------------------
//
// Multi-paradigm paper search: the server fans the entry out to the
// configured providers (catalog / arxiv / openalex / qdrant), merges the
// hits, and anchors identity-anchored hits to registry papers. Results
// carry a registry paper_id (newly minted papers have created=true);
// candidates are title-only hits that were NOT minted.

export type PaperSearchEntry = {
  text?: string
  title?: string
  doi?: string
  arxiv_id?: string
  max_results?: number
  required_phrases?: string[]
}

export type SearchHit = {
  arxiv_id?: string
  doi?: string
  title?: string
  abstract?: string
  authors?: string[]
  year?: number
  score: number
  source: string
  // Extended per-hit fields carried by the remote/multi search paths
  // (absent on plain /api/search hits).
  url?: string
  venue?: string
  citations?: number
  raw_rank?: number
  raw_score?: number
  // Server-side enrichment on the multi search response: qatlasd
  // resolve-or-mints identity-anchored hits and backfills these. Only
  // minted hits carry them (title-only hits never mint).
  paper_id?: string
  created?: boolean
  has_md?: boolean
  status?: string
}

export type SearchResult = {
  paper_id: string
  hit: SearchHit
  created: boolean
}

export type PaperSearchResponse = {
  results: SearchResult[]
  candidates: SearchHit[]
}

// Requires the papers:read scope (browser sessions carry ScopeMaster).
export async function paperSearch(body: PaperSearchEntry): Promise<PaperSearchResponse> {
  return postJson<PaperSearchResponse>('/api/search', body)
}

// --- Agentic search (POST /api/search/agentic) ------------------------------
//
// LLM-backed agentic search served by the qatlas-search microservice and
// proxied by qatlasd. On top of the classic search response it carries an
// optional natural-language conclusion and the caller's daily usage. 429
// means the daily quota is exhausted; the body still carries usage so the
// UI can show "today/limit" instead of a bare error.

export type SearchUsage = {
  today: number
  limit: number
  llm_tokens?: number
}

export type AgenticSearchResponse = {
  results: SearchResult[]
  candidates: SearchHit[]
  conclusion: string | null
  usage: SearchUsage
  errors?: Record<string, string>
}

// Error raised by agenticSearch. Keeps the HTTP status and the usage block
// (present on 429) so the page can render a proper quota message.
export class AgenticSearchError extends Error {
  status: number
  usage?: SearchUsage

  constructor(status: number, detail: string, usage?: SearchUsage) {
    super(detail || `${status}`)
    this.name = 'AgenticSearchError'
    this.status = status
    this.usage = usage
  }
}

export async function agenticSearch(
  body: PaperSearchEntry & { sources?: string[] },
): Promise<AgenticSearchResponse> {
  const response = await fetch('/api/search/agentic', {
    method: 'POST',
    headers: { ...authHeaders(), 'content-type': 'application/json' },
    body: JSON.stringify(body),
  })
  if (!response.ok) {
    let detail = ''
    let usage: SearchUsage | undefined
    try {
      const j = (await response.json()) as {
        detail?: string
        usage?: SearchUsage
      }
      detail = j.detail ?? ''
      usage = j.usage
    } catch {
      // not JSON; ignore
    }
    throw new AgenticSearchError(
      response.status,
      detail || response.statusText,
      usage,
    )
  }
  return response.json() as Promise<AgenticSearchResponse>
}

// --- Multi search + backend catalog (POST /api/search/multi, GET /api/search/backends) ---
//
// The per-backend search path used when agentic search is off: every
// selected backend returns its own raw hit list (the source's own
// order, no cross-backend ranking). The backend catalog drives the
// search page's checkbox picker — key-requiring backends without a
// stored key render disabled (selectable=false).

export type SearchBackendEntry = {
  name: string
  label: string
  category: 'academic' | 'web'
  requires_key: boolean
  user_key: boolean
  server_ready: boolean
  key_configured: boolean
  selectable: boolean
}

export type SearchBackendsResponse = {
  remote: boolean
  keys_enabled: boolean
  backends: SearchBackendEntry[]
}

export function getSearchBackends(): Promise<SearchBackendsResponse> {
  return getJson<SearchBackendsResponse>('/api/search/backends')
}

export type MultiSearchEntry = {
  text: string
  max_results?: number
  sources: string[]
}

export type MultiSearchResponse = {
  results: Record<string, SearchHit[]>
  usage: { llm_tokens: number }
  errors: Record<string, string>
  remote: boolean
}

// Requires the papers:read scope; the server injects the caller's
// stored third-party keys for the requested backends.
export async function multiSearch(
  body: MultiSearchEntry,
): Promise<MultiSearchResponse> {
  return postJson<MultiSearchResponse>('/api/search/multi', body)
}

// --- My search API keys (GET/PUT/DELETE /api/me/search-keys) ------------------
//
// Per-user third-party keys, AES-encrypted at rest on the server. The
// list never returns key material — only a masked hint.

export type MeSearchKey = {
  backend: string
  hint: string
  updated_at: string
}

export type MeSearchKeysResponse = {
  enabled: boolean
  keys: MeSearchKey[]
}

export function listMySearchKeys(): Promise<MeSearchKeysResponse> {
  return getJson<MeSearchKeysResponse>('/api/me/search-keys')
}

export function putSearchKey(
  backend: string,
  key: string,
): Promise<{ ok: boolean }> {
  return putJson<{ ok: boolean }>(
    `/api/me/search-keys/${encodeURIComponent(backend)}`,
    { key },
  )
}

export function deleteSearchKey(backend: string): Promise<{ ok: boolean }> {
  return delJson<{ ok: boolean }>(
    `/api/me/search-keys/${encodeURIComponent(backend)}`,
  )
}

export async function delJson<T>(url: string): Promise<T> {
  const response = await fetch(url, {
    method: 'DELETE',
    headers: { ...authHeaders() },
  })
  if (!response.ok) {
    let detail = ''
    try {
      const j = (await response.json()) as { detail?: string }
      detail = j.detail ?? ''
    } catch {
      // not JSON; ignore
    }
    throw new Error(detail ? `${response.status}: ${detail}` : `${response.status} ${response.statusText}`)
  }
  return response.json() as Promise<T>
}

// --- Plugins (GET /api/v1/plugins) ------------------------------------------
//
// Registry summaries; shape mirrors internal/plugin/registry.go Summary.
// The `search-remote` entry represents the qatlas-search microservice —
// the search page uses it to decide whether agentic search is available.

export type PluginSummary = {
  id: string
  name?: string
  version?: string
  kind?: string
  status: string // connected | disconnected | disabled | incompatible
  enabled: boolean
  error?: string
  // Optional registry metadata, present when the plugin advertises it.
  transport?: string
  contributes?: {
    capabilities?: string[]
    subscribes?: string[]
    publishes?: string[]
  }
  needs?: string[]
}

export type PluginsResponse = {
  plugins: PluginSummary[]
}

export function listPlugins(): Promise<PluginsResponse> {
  return getJson<PluginsResponse>('/api/v1/plugins')
}

export function isSearchRemoteAvailable(
  plugins: PluginSummary[] | undefined,
): boolean {
  const entry = plugins?.find((p) => p.id === 'search-remote')
  return Boolean(entry && entry.enabled && entry.status === 'connected')
}

// The `downloader` builtin plugin entry — gates the downloader page the
// same way isSearchRemoteAvailable gates agentic search.
export function isDownloaderAvailable(
  plugins: PluginSummary[] | undefined,
): boolean {
  const entry = plugins?.find((p) => p.id === 'downloader')
  return Boolean(entry && entry.enabled && entry.status === 'connected')
}

// --- Paper detail (GET /api/papers/{paper_id}) ------------------------------

export type PaperAsset = {
  asset_id: number
  source: string // 'arxiv' | 'published'
  arxiv_version?: number // arxiv assets only
  pdf_path?: string
  pdf_size?: number
  pdf_sha256?: string
  mineru_md_path?: string
  mineru_json_path?: string
  image_count?: number
  fetched_at?: string
  lease_id?: string
  lease_holder?: string
  lease_expires_at?: string
}

export type PaperDetail = {
  paper_id: string
  status: string // pending | ready | failed | merged
  arxiv_id?: string
  doi?: string
  openalex_id?: string
  paper_ref?: string
  title?: string
  authors?: string[]
  created_at?: string
  updated_at?: string
  assets: PaperAsset[]
  acquisition: AcquisitionStatus
}

export type AcquisitionEvent = {
  phase: string
  state: string
  at: string
  detail?: string
}

export type AcquisitionStatus = {
  state: string
  phase: string
  active: boolean
  updated_at?: string
  error?: string
  events: AcquisitionEvent[]
  queue?: {
    position: number
    ahead_of_me: number
    running_count: number
    max_concurrent: number
    eta_seconds: number
  }
}

// --- Papers list (GET /api/papers) ------------------------------------------
//
// Paginated registry listing backing the "converted papers" page. Filters
// mirror the backend handler (internal/routes/papers_list.go): has_md
// (converted markdown present), status, title substring; sorted by
// created_at descending server-side.

export type PapersListParams = {
  has_md?: boolean
  status?: 'pending' | 'ready' | 'failed'
  q?: string
  page?: number // 1-based
  per_page?: number
}

export type PapersListItem = {
  paper_id: string
  arxiv_id?: string
  doi?: string
  title?: string
  status: string
  has_pdf: boolean
  has_md: boolean
  image_count: number
  created_at: string
  updated_at: string
}

export type PapersListResponse = {
  items: PapersListItem[]
  total: number
  page: number
  per_page: number
}

export async function papersList(
  params: PapersListParams,
): Promise<PapersListResponse> {
  const qs = new URLSearchParams()
  if (params.has_md !== undefined) qs.set('has_md', String(params.has_md))
  if (params.status) qs.set('status', params.status)
  if (params.q) qs.set('q', params.q)
  if (params.page && params.page > 1) qs.set('page', String(params.page))
  if (params.per_page) qs.set('per_page', String(params.per_page))
  const suffix = qs.toString()
  return getJson<PapersListResponse>(`/api/papers${suffix ? `?${suffix}` : ''}`)
}

// --- Manual PDF contribution (POST /api/papers/{doi}/upload-pdf) -------------
// Human-in-the-loop lane for papers the automated ladder could not fetch:
// the admin failures table pairs the DOI with a locally-downloaded PDF;
// qatlasd verifies the metadata against OpenAlex, resolve-or-mints the
// paper and registers the asset (same path as `qatlas contrib`).
export async function uploadPaperPDFByDOI(
  doi: string,
  file: File,
): Promise<{ paper_id: string }> {
  const form = new FormData()
  form.append('pdf', file)
  const response = await fetch(
    `/api/papers/${encodeURIComponent(doi)}/upload-pdf`,
    {
      method: 'POST',
      headers: { ...authHeaders() },
      body: form,
    },
  )
  if (!response.ok) {
    let detail = ''
    try {
      const j = (await response.json()) as { detail?: string }
      detail = j.detail ?? ''
    } catch {
      // not JSON; ignore
    }
    throw new Error(detail ? `${response.status}: ${detail}` : `${response.status} ${response.statusText}`)
  }
  return response.json() as Promise<{ paper_id: string }>
}

// --- Admin (GET /api/admin/*) ------------------------------------------------
//
// whoami is session-only and always 200 for a signed-in browser user;
// db/schema is adminGuarded (403 for non-admins). Shapes mirror
// internal/routes/admin.go.

export type AdminWhoami = {
  login: string
  is_admin: boolean
  // Role fields mirroring the /api/admin/users guard (userAdminGuard):
  // is_user_admin = env admin OR is_admin OR is_superadmin;
  // is_superadmin = env admin OR is_superadmin.
  is_user_admin: boolean
  is_superadmin: boolean
}

// GET /api/admin/users — every users record with role/availability
// flags (admin_users.go).
export type AdminUser = {
  id: string
  name: string
  email: string
  github_login: string
  gitea_login: string
  is_admin: boolean
  is_superadmin: boolean
  disabled: boolean
  created: string
  updated: string
}

export type AdminUsersResponse = {
  users: AdminUser[]
  total: number
}

export type AdminSchemaColumn = {
  name: string
  data_type: string
  nullable: boolean
  default: string | null
  is_pk: boolean
}

export type AdminSchemaIndex = {
  name: string
  definition: string
}

export type AdminSchemaConstraint = {
  name: string
  kind: string // PRIMARY KEY | FOREIGN KEY | UNIQUE | CHECK | EXCLUDE
  definition: string
}

export type AdminSchemaTable = {
  name: string
  row_estimate: number
  total_size: string // pg_size_pretty output, e.g. "16 kB"
  columns: AdminSchemaColumn[]
  indexes: AdminSchemaIndex[]
  constraints: AdminSchemaConstraint[]
}

export type AdminDBSchema = {
  database: string
  tables: AdminSchemaTable[]
}

// --- Me (user dashboard) -----------------------------------------------------

// GET /api/me — the signed-in user's own profile. avatar is the raw
// PocketBase file name; build the display URL via pb.files.getURL.
export type MeProfile = {
  id: string
  email: string
  name: string
  avatar: string
  github_login: string
  gitea_login: string
  github_bound: boolean
  gitea_bound: boolean
  is_admin: boolean
  is_superadmin: boolean
  created: string
}

// GET /api/me/usage — agentic-search metering state for today; mirrors
// the usage block of the agentic search response.
export type MyUsage = {
  metric: string
  today: number
  limit: number
  llm_tokens: number
}

export function meProfile(): Promise<MeProfile> {
  return getJson<MeProfile>('/api/me')
}

export function myUsage(): Promise<MyUsage> {
  return getJson<MyUsage>('/api/me/usage')
}

export function adminWhoami(): Promise<AdminWhoami> {
  return getJson<AdminWhoami>('/api/admin/whoami')
}

// PATCH /api/admin/users/{id} — toggle availability and/or is_admin.
// is_admin changes are rejected server-side unless the caller is
// superadmin; omit the key rather than sending it when not allowed.
export function adminUpdateUser(
  id: string,
  body: { disabled?: boolean; is_admin?: boolean },
): Promise<AdminUser> {
  return patchJson<AdminUser>(`/api/admin/users/${id}`, body)
}

export function adminListUsers(): Promise<AdminUsersResponse> {
  return getJson<AdminUsersResponse>('/api/admin/users')
}

export function adminDBSchema(): Promise<AdminDBSchema> {
  return getJson<AdminDBSchema>('/api/admin/db/schema')
}


export type AdminAcquisitionFailure = {
  paper_id: string
  arxiv_id?: string
  doi?: string
  title?: string
  stage?: string
  reason?: string
  failed_at: string
  attempts: number
}

export type AdminAcquisitionFailuresResponse = {
  items: AdminAcquisitionFailure[]
}

export function adminAcquisitionFailures(): Promise<AdminAcquisitionFailuresResponse> {
  return getJson<AdminAcquisitionFailuresResponse>('/api/admin/acquisition/failures?limit=100')
}

// Admin plugin listing: same PluginSummary shape as GET /api/v1/plugins,
// served under the admin-guarded path.
export function adminListPlugins(): Promise<PluginsResponse> {
  return getJson<PluginsResponse>('/api/admin/plugins')
}

// Raw row browsing for one table (paginated). 404 = unknown table,
// 503 = database unavailable.
export type AdminDBRows = {
  table: string
  columns: string[]
  rows: unknown[][]
  total: number
  page: number
  per_page: number
}

export function adminDBTableRows(
  table: string,
  page: number,
  perPage: number,
): Promise<AdminDBRows> {
  const qs = new URLSearchParams()
  if (page > 1) qs.set('page', String(page))
  qs.set('per_page', String(perPage))
  return getJson<AdminDBRows>(
    `/api/admin/db/tables/${encodeURIComponent(table)}/rows?${qs.toString()}`,
  )
}

// Per-plugin admin manifest: describes the configurable sections/fields
// the plugin exposes. Secret fields come back masked ("••••••••") or null.
// 404 = the plugin has no admin page; 503 = plugin unreachable.
export type AdminPluginManifestField = {
  key: string
  label: string
  type: 'str' | 'int' | 'float' | 'bool'
  description?: string
  value: unknown // string | number | boolean | null (masked for secrets)
  secret?: boolean
}

export type AdminPluginManifestSection = {
  key: string
  title: string
  description?: string
  fields: AdminPluginManifestField[]
}

export type AdminPluginManifest = {
  title: string
  version?: string
  status?: {
    agent_configured?: boolean
    backends?: Record<string, boolean>
  }
  sections: AdminPluginManifestSection[]
}

export function adminPluginManifest(id: string): Promise<AdminPluginManifest> {
  return getJson<AdminPluginManifest>(
    `/api/admin/plugins/${encodeURIComponent(id)}/manifest`,
  )
}

export type AdminPluginConfigResult = {
  applied: string[]
  restart_required: boolean
}

// Sends only the fields the user actually changed; the server answers
// with the applied keys and whether a restart is needed to take effect.
export function adminPluginUpdateConfig(
  id: string,
  updates: Record<string, unknown>,
): Promise<AdminPluginConfigResult> {
  return putJson<AdminPluginConfigResult>(
    `/api/admin/plugins/${encodeURIComponent(id)}/config`,
    { updates },
  )
}

// --- Admin: search usage & quota plans --------------------------------------
//
// Per-user agentic-search metering and plan management. Shapes mirror the
// qatlasd admin handlers: usage is counted per UTC day; `cost` is USD.

export type AdminUsageRow = {
  user_id: string
  login: string
  plan: string
  effective_limit: number
  count: number
  llm_tokens: number
  cost: number // USD
}

export type AdminUsageResponse = {
  rows: AdminUsageRow[]
}

// `day` is YYYY-MM-DD (UTC); omit for today.
export function adminUsage(day?: string): Promise<AdminUsageResponse> {
  const suffix = day ? `?day=${encodeURIComponent(day)}` : ''
  return getJson<AdminUsageResponse>(`/api/admin/usage${suffix}`)
}

export type AdminPlan = {
  name: string
  daily_agentic_search_limit: number
  description?: string
}

export type AdminPlansResponse = {
  plans: AdminPlan[]
}

export function adminPlans(): Promise<AdminPlansResponse> {
  return getJson<AdminPlansResponse>('/api/admin/plans')
}

export function adminPutPlan(
  name: string,
  body: { daily_agentic_search_limit: number; description?: string },
): Promise<unknown> {
  return putJson(`/api/admin/plans/${encodeURIComponent(name)}`, body)
}

// daily_limit_override: number sets it, null clears it (back to plan limit).
export function adminPutQuota(
  userId: string,
  body: { plan?: string; daily_limit_override?: number | null },
): Promise<unknown> {
  return putJson(`/api/admin/quotas/${encodeURIComponent(userId)}`, body)
}

// --- Admin: MinerU scheduler status (GET /api/admin/mineru/status) -----------
//
// Snapshot of the MinerU conversion scheduler: whether a run is active,
// when the next run fires, the last run's counters, and today's daily-cap
// consumption. All fields but `running` may be absent on a fresh server.

export type MineruStatus = {
  running: boolean
  next_run_at?: string
  last_run_started?: string
  last_run_finished?: string
  last_stop_reason?: string
  last_run?: {
    processed?: number
    succeeded?: number
    failed?: number
    skipped?: number
  }
  daily_cap?: number
  converted_today?: number
  cap_day?: string
}

export type AdminMineruStatusResponse = MineruStatus

export function adminMineruStatus(): Promise<AdminMineruStatusResponse> {
  return getJson<AdminMineruStatusResponse>('/api/admin/mineru/status')
}

// --- Admin: asset browser (GET /api/admin/assets/*) --------------------------
//
// Ops surface over the object-store bytes behind each registry paper.
// search finds papers that have assets; list enumerates a paper's stored
// objects; url mints a short-lived presigned S3 URL. download/inline are
// plain authenticated GETs (the PocketBase session cookie carries <a>/iframe
// navigations), so the SPA builds those URLs as template strings.

export type AdminAssetEntry = {
  kind: 'pdf' | 'markdown'
  object_key: string
  size: number
  sha256?: string
  content_type?: string
  presigned_url?: string
  presign_supported: boolean
}

export type AdminAssetListResponse = {
  paper_id: string
  title?: string
  arxiv_id?: string
  doi?: string
  status?: string
  assets: AdminAssetEntry[]
}

export type AdminAssetSearchResponse = {
  papers: {
    paper_id: string
    arxiv_id?: string
    doi?: string
    title?: string
    status?: string
  }[]
}

export type AdminAssetURLResponse = {
  kind: string
  url: string
  expires_at: string
  object_key: string
}

export function adminAssetSearch(q: string): Promise<AdminAssetSearchResponse> {
  const qs = new URLSearchParams({ q, limit: '50' })
  return getJson<AdminAssetSearchResponse>(`/api/admin/assets/search?${qs}`)
}

export function adminAssetList(paperId: string): Promise<AdminAssetListResponse> {
  return getJson<AdminAssetListResponse>(
    `/api/admin/assets/${encodeURIComponent(paperId)}`,
  )
}

export function adminAssetURL(
  paperId: string,
  kind: string,
): Promise<AdminAssetURLResponse> {
  return getJson<AdminAssetURLResponse>(
    `/api/admin/assets/${encodeURIComponent(paperId)}/${encodeURIComponent(kind)}/url?ttl=1h`,
  )
}

// --- Robust Downloader (POST /api/downloader/fetch, GET /api/downloader/jobs) ---
//
// Batch paper acquisition driven by the `downloader` builtin plugin:
// every input line is parsed (DOI / arXiv id / paper URL), resolve-or-
// minted into the registry, and enqueued on the strategy ladder
// (arXiv → OA APIs → publisher patterns → landing page → agent). The
// routes stay mounted when the plugin is off: fetch answers 503 with a
// detail, jobs degrades to an empty snapshot.

export type DownloaderFetchItem = {
  input: string
  kind: 'doi' | 'arxiv' | 'url' | 'invalid'
  paper_id?: string
  created: boolean
  error?: string
}

export type DownloaderFetchResponse = {
  items: DownloaderFetchItem[]
  enqueued: number
}

// One strategy attempt, successful or not (downloader.Attempt).
export type DownloaderAttempt = {
  strategy: string
  url?: string
  error?: string
  ms?: number
}

export type DownloaderJobEvent = {
  phase: string
  state: string
  at: string
  detail?: string
}

// One job from GET /api/downloader/jobs (downloader.Progress).
export type DownloaderJob = {
  paper_id: string
  input?: string
  kind?: string
  state: 'queued' | 'running' | 'done' | 'failed'
  phase: string
  active: boolean
  strategy?: string
  error?: string
  submitted_at: string
  updated_at: string
  finished_at?: string
  trace?: DownloaderAttempt[]
  events: DownloaderJobEvent[]
}

export type DownloaderJobsResponse = {
  jobs: DownloaderJob[]
  counters: {
    queued?: number
    in_flight?: number
    succeeded?: number
    failed?: number
    skipped?: number
  }
}

// Requires the papers:write scope; max 50 items per batch.
export function downloaderFetch(
  items: string[],
): Promise<DownloaderFetchResponse> {
  return postJson<DownloaderFetchResponse>('/api/downloader/fetch', { items })
}

// Requires the papers:read scope.
export function downloaderJobs(): Promise<DownloaderJobsResponse> {
  return getJson<DownloaderJobsResponse>('/api/downloader/jobs')
}
