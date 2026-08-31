import { keepPreviousData, useMutation, useQuery } from '@tanstack/react-query'
import {
  adminAcquisitionFailures,
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
  getJson,
  listPlugins,
  meProfile,
  myUsage,
  paperSearch,
  papersList,
  type AdminAcquisitionFailuresResponse,
  type AdminDBRows,
  type AdminDBSchema,
  type AdminPlansResponse,
  type AdminPluginConfigResult,
  type AdminPluginManifest,
  type AdminUsageResponse,
  type AdminUsersResponse,
  type AdminWhoami,
  type MeProfile,
  type MyUsage,
  type PaperDetail,
  type PaperSearchEntry,
  type PapersListParams,
  type PaperStats,
  type PluginsResponse,
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
// carrying the usage block.
export function useAgenticSearch(entry: PaperSearchEntry | null) {
  return useQuery({
    queryKey: ['agentic-search', JSON.stringify(entry ?? null)],
    queryFn: () => agenticSearch(entry as PaperSearchEntry),
    enabled: entry !== null,
    retry: false,
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

export function useAdminPluginSaveConfig(id: string) {
  return useMutation({
    mutationFn: (updates: Record<string, unknown>): Promise<AdminPluginConfigResult> =>
      adminPluginUpdateConfig(id, updates),
  })
}
