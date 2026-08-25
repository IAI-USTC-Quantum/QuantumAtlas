export type PaperStats = {
  available: boolean
  total?: number
  pending?: number
  ready?: number
  failed?: number
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
  authors?: string[]
  year?: number
  score: number
  source: string
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
  body: PaperSearchEntry,
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

// --- Admin (GET /api/admin/*) ------------------------------------------------
//
// whoami is session-only and always 200 for a signed-in browser user;
// db/schema is adminGuarded (403 for non-admins). Shapes mirror
// internal/routes/admin.go.

export type AdminWhoami = {
  login: string
  is_admin: boolean
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

export function adminWhoami(): Promise<AdminWhoami> {
  return getJson<AdminWhoami>('/api/admin/whoami')
}

export function adminDBSchema(): Promise<AdminDBSchema> {
  return getJson<AdminDBSchema>('/api/admin/db/schema')
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
