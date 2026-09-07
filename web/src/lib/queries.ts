import { keepPreviousData, useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import {
  adminAcquisitionFailures,
  adminAssetList,
  adminAssetSearch,
  adminDBSchema,
  adminDBTableRows,
  adminListPlugins,
  adminListUsers,
  adminPlans,
  adminPluginManifest,
  adminPluginUpdateConfig,
  adminUsage,
  adminWhoami,
  agenticSearch,
  deleteSearchKey,
  uploadPaperPDFByDOI,
  downloaderFetch,
  downloaderJobs,
  getJson,
  getSearchBackends,
  listMySearchKeys,
  listPlugins,
  meProfile,
  multiSearch,
  myUsage,
  paperSearch,
  papersList,
  putSearchKey,
  type AdminAcquisitionFailuresResponse,
  type AdminAssetListResponse,
  type AdminAssetSearchResponse,
  type AdminDBRows,
  type AdminDBSchema,
  type AdminPlansResponse,
  type AdminPluginConfigResult,
  type AdminPluginManifest,
  type AdminUsageResponse,
  type AdminUsersResponse,
  type AdminWhoami,
  type DownloaderFetchResponse,
  type DownloaderJobsResponse,
  type MeProfile,
  type MeSearchKeysResponse,
  type MultiSearchEntry,
  type MyUsage,
  type PaperDetail,
  type PaperSearchEntry,
  type PapersListParams,
  type PaperStats,
  type PluginsResponse,
  type SearchBackendsResponse,
} from './api'

export function usePaperStats() {
  return useQuery({
    queryKey: ['paper-stats'],
    queryFn: () => getJson<PaperStats>('/api/papers/stats'),
  })
}

// Multi-paradigm paper search against POST /api/search. Pass `null` to
// keep the hook idle (e.g. while the user is still typing). The caller
// controls the entry fields so the page UI can expose them without
// growing the hook signature each time.
export function usePaperSearch(entry: PaperSearchEntry | null) {
  return useQuery({
    queryKey: ['paper-search', JSON.stringify(entry ?? null)],
    queryFn: () => paperSearch(entry as PaperSearchEntry),
    enabled: entry !== null,
    retry: false,
  })
}

export function usePaperDetail(paperId: string | null) {
  return useQuery({
    queryKey: ['paper', paperId],
    queryFn: () =>
      getJson<PaperDetail>(`/api/papers/${encodeURIComponent(paperId!)}`),
    enabled: Boolean(paperId),
    refetchInterval: (query) =>
      query.state.data?.acquisition?.active ? 2_000 : false,
  })
}

// Paginated registry list. keepPreviousData keeps the old page rendered
// while the next page loads so the table doesn't flash a skeleton on
// every pagination click.
export function usePapersList(params: PapersListParams) {
  return useQuery({
    queryKey: ['papers-list', params],
    queryFn: () => papersList(params),
    placeholderData: keepPreviousData,
  })
}

// Session-only whoami used by the sidebar (show the admin nav entry) and
// the admin page (gate the schema fetch). Cached for a few minutes so
// every sidebar render doesn't hit the server; retry disabled because a
// 401/403 here just means "not a browser session" — hide, don't retry.
export function useAdminWhoami() {
  return useQuery({
    queryKey: ['admin-whoami'],
    queryFn: (): Promise<AdminWhoami> => adminWhoami(),
    staleTime: 5 * 60_000,
    retry: false,
  })
}

export function useAdminAcquisitionFailures(enabled: boolean) {
  return useQuery({
    queryKey: ['admin-acquisition-failures'],
    queryFn: (): Promise<AdminAcquisitionFailuresResponse> => adminAcquisitionFailures(),
    enabled,
    retry: false,
    refetchInterval: enabled ? 15_000 : false,
  })
}

export function useAdminSchema(enabled: boolean) {
  return useQuery({
    queryKey: ['admin-db-schema'],
    queryFn: (): Promise<AdminDBSchema> => adminDBSchema(),
    enabled,
    retry: false,
  })
}

// User-management listing (GET /api/admin/users). Enabled only for
// userAdminGuard passers (whoami.is_user_admin); retry disabled
// because a 401/403 just means the session lacks the role.
export function useAdminUsers(enabled: boolean) {
  return useQuery({
    queryKey: ['admin-users'],
    queryFn: (): Promise<AdminUsersResponse> => adminListUsers(),
    enabled,
    retry: false,
  })
}

// Plugin registry listing — the search page uses the `search-remote` entry
// to decide whether agentic search can be enabled. Cached for a minute;
// no retry because a failure just means "agentic unavailable".
export function usePlugins() {
  return useQuery({
    queryKey: ['plugins'],
    queryFn: (): Promise<PluginsResponse> => listPlugins(),
    staleTime: 60_000,
    retry: false,
  })
}

// Agentic search against POST /api/search/agentic. Same null-to-idle
// convention as usePaperSearch; 429 surfaces as an AgenticSearchError
// carrying the usage block. The entry may pin `sources` (selected
// backends) — the server forwards them to the microservice.
export function useAgenticSearch(entry: (PaperSearchEntry & { sources?: string[] }) | null) {
  return useQuery({
    queryKey: ['agentic-search', JSON.stringify(entry ?? null)],
    queryFn: () => agenticSearch(entry as PaperSearchEntry),
    enabled: entry !== null,
    retry: false,
  })
}

// Backend catalog for the search page's checkbox picker. Only fetched
// when the search-remote plugin is connected (the catalog proxies the
// microservice); retry disabled because a failure just means "picker
// unavailable" and the page falls back to the legacy fused search.
export function useSearchBackends(enabled = true) {
  return useQuery({
    queryKey: ['search-backends'],
    queryFn: (): Promise<SearchBackendsResponse> => getSearchBackends(),
    enabled,
    staleTime: 60_000,
    retry: false,
  })
}

// Per-backend multi search against POST /api/search/multi. Same
// null-to-idle convention; the query key includes the sources so a
// checkbox change refetches.
export function useMultiSearch(entry: MultiSearchEntry | null) {
  return useQuery({
    queryKey: ['multi-search', JSON.stringify(entry ?? null)],
    queryFn: () => multiSearch(entry as MultiSearchEntry),
    enabled: entry !== null,
    retry: false,
  })
}

// Dashboard: the caller's stored third-party search keys (masked).
export function useMySearchKeys() {
  return useQuery({
    queryKey: ['me-search-keys'],
    queryFn: (): Promise<MeSearchKeysResponse> => listMySearchKeys(),
    retry: false,
  })
}

export function useSaveSearchKey() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: ({ backend, key }: { backend: string; key: string }) =>
      putSearchKey(backend, key),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ['me-search-keys'] })
      void qc.invalidateQueries({ queryKey: ['search-backends'] })
      void qc.invalidateQueries({ queryKey: ['multi-search'] })
      void qc.invalidateQueries({ queryKey: ['agentic-search'] })
    },
  })
}

export function useDeleteSearchKey() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (backend: string) => deleteSearchKey(backend),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ['me-search-keys'] })
      void qc.invalidateQueries({ queryKey: ['search-backends'] })
      void qc.invalidateQueries({ queryKey: ['multi-search'] })
      void qc.invalidateQueries({ queryKey: ['agentic-search'] })
    },
  })
}

// Dashboard profile (GET /api/me). Session-only; retry disabled because
// a 401/403 just means "not a browser session".
export function useMe() {
  return useQuery({
    queryKey: ['me'],
    queryFn: (): Promise<MeProfile> => meProfile(),
    staleTime: 5 * 60_000,
    retry: false,
  })
}

// Dashboard usage (GET /api/me/usage). Short staleTime — the number
// changes with every agentic search call. retry disabled: a 503 just
// means the metering store is unavailable, the panel degrades instead.
export function useMyUsage() {
  return useQuery({
    queryKey: ['me-usage'],
    queryFn: (): Promise<MyUsage> => myUsage(),
    staleTime: 30_000,
    retry: false,
  })
}

export function useAdminUsage(day: string | undefined, enabled: boolean) {
  return useQuery({
    queryKey: ['admin-usage', day ?? ''],
    queryFn: (): Promise<AdminUsageResponse> => adminUsage(day),
    enabled,
    retry: false,
  })
}

export function useAdminPlans(enabled: boolean) {
  return useQuery({
    queryKey: ['admin-plans'],
    queryFn: (): Promise<AdminPlansResponse> => adminPlans(),
    enabled,
    retry: false,
  })
}

// Admin-guarded plugin registry listing (GET /api/admin/plugins) — same
// PluginSummary shape as the public /api/v1/plugins endpoint.
export function useAdminPlugins(enabled: boolean) {
  return useQuery({
    queryKey: ['admin-plugins'],
    queryFn: (): Promise<PluginsResponse> => adminListPlugins(),
    enabled,
    retry: false,
  })
}

// Raw rows of one registry table. keepPreviousData keeps the old page
// rendered while the next page loads, like usePapersList.
export function useAdminDBRows(
  table: string,
  page: number,
  perPage: number,
  enabled: boolean,
) {
  return useQuery({
    queryKey: ['admin-db-rows', table, page, perPage],
    queryFn: (): Promise<AdminDBRows> => adminDBTableRows(table, page, perPage),
    enabled,
    retry: false,
    placeholderData: keepPreviousData,
  })
}

// Per-plugin admin manifest. retry disabled because a 404 just means
// "this plugin has no admin page" — render a note, don't retry.
export function useAdminPluginManifest(id: string, enabled: boolean) {
  return useQuery({
    queryKey: ['admin-plugin-manifest', id],
    queryFn: (): Promise<AdminPluginManifest> => adminPluginManifest(id),
    enabled,
    retry: false,
  })
}

// Manual PDF contribution from the admin failures table (paper fetched
// by a human, paired with its DOI).
export function useUploadFailurePDF() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: ({ doi, file }: { doi: string; file: File }) =>
      uploadPaperPDFByDOI(doi, file),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ['admin-acquisition-failures'] })
    },
  })
}

export function useAdminPluginSaveConfig(id: string) {
  return useMutation({
    mutationFn: (updates: Record<string, unknown>): Promise<AdminPluginConfigResult> =>
      adminPluginUpdateConfig(id, updates),
  })
}

// --- Admin asset browser -------------------------------------------------------

// Paper search over papers that have stored assets (GET
// /api/admin/assets/search). The page debounces `q` before calling; an
// empty/whitespace query keeps the hook idle. keepPreviousData keeps the
// previous results rendered while the next keystroke's query runs.
export function useAdminAssetSearch(q: string, enabled: boolean) {
  return useQuery({
    queryKey: ['admin-asset-search', q],
    queryFn: (): Promise<AdminAssetSearchResponse> => adminAssetSearch(q),
    enabled: enabled && q.trim() !== '',
    retry: false,
    placeholderData: keepPreviousData,
  })
}

// Asset listing for one selected paper (GET /api/admin/assets/{paper_id}).
// Null keeps the hook idle while nothing is expanded.
export function useAdminAssetList(paperId: string | null) {
  return useQuery({
    queryKey: ['admin-asset-list', paperId],
    queryFn: (): Promise<AdminAssetListResponse> => adminAssetList(paperId!),
    enabled: Boolean(paperId),
    retry: false,
  })
}

// Downloader job snapshot (GET /api/downloader/jobs). Polls every 2s
// while any job is active (derived from the data itself, like
// usePaperDetail) or while the caller reports a recent submit — the
// page passes `active` for its 30s post-submit window so polling starts
// before the snapshot has caught up with the new jobs.
export function useDownloaderJobs(active: boolean, enabled = true) {
  return useQuery({
    queryKey: ['downloader-jobs'],
    queryFn: (): Promise<DownloaderJobsResponse> => downloaderJobs(),
    enabled,
    retry: false,
    refetchInterval: (query) =>
      active || query.state.data?.jobs.some((job) => job.active) ? 2_000 : false,
  })
}

// Batch submit for the downloader page (POST /api/downloader/fetch).
// Toasts the enqueued count (503 surfaces its detail via postJson) and
// refreshes the job snapshot.
export function useDownloaderSubmit() {
  const qc = useQueryClient()
  const { t } = useTranslation('downloader')
  return useMutation({
    mutationFn: (items: string[]) => downloaderFetch(items),
    onSuccess: (data: DownloaderFetchResponse) => {
      toast.success(t('toasts.submitted', { count: data.enqueued }))
      void qc.invalidateQueries({ queryKey: ['downloader-jobs'] })
    },
    onError: (error: Error) => toast.error(error.message),
  })
}
