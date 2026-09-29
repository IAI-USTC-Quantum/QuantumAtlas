import { useTranslation } from 'react-i18next'
import type { DiscussionFilters as FilterState } from '@/lib/reader-api'
import { PRESET_TYPES } from './badges'

type StatusFilter = FilterState['status']
type ScopeFilter = FilterState['scope']

// Shared filter bar for the Issue-style discussion list (plan §7.3: filter
// by type / scope / status). Scope follows §12.1: no filter = all; "Lean"
// resolves to public+lean, not lean-only.
export function DiscussionFilterBar({
  value,
  onChange,
  extraTypes = [],
}: {
  value: { scope: ScopeFilter; status: StatusFilter; type: string }
  onChange: (next: { scope: ScopeFilter; status: StatusFilter; type: string }) => void
  extraTypes?: string[]
}) {
  const { t } = useTranslation('reader')
  const customTypes = Array.from(
    new Set(extraTypes.filter((type) => !(PRESET_TYPES as readonly string[]).includes(type))),
  )
  return (
    <div className="flex flex-wrap items-center gap-3" data-testid="discussion-filters">
      <label className="flex items-center gap-1.5 text-sm">
        <span className="text-muted-foreground">{t('filters.scope')}</span>
        <select
          data-testid="filter-scope"
          value={value.scope}
          onChange={(e) => onChange({ ...value, scope: e.target.value as ScopeFilter })}
          className="h-8 rounded-md border border-border bg-transparent px-2 text-sm"
        >
          <option value="">{t('filters.scopeAll')}</option>
          <option value="public">{t('scope.public')}</option>
          <option value="lean">{t('filters.scopeLean')}</option>
        </select>
      </label>
      <label className="flex items-center gap-1.5 text-sm">
        <span className="text-muted-foreground">{t('filters.status')}</span>
        <select
          data-testid="filter-status"
          value={value.status}
          onChange={(e) => onChange({ ...value, status: e.target.value as StatusFilter })}
          className="h-8 rounded-md border border-border bg-transparent px-2 text-sm"
        >
          <option value="">{t('filters.statusAll')}</option>
          <option value="pending">{t('status.pending')}</option>
          <option value="confirmed">{t('status.confirmed')}</option>
          <option value="retracted">{t('status.retracted')}</option>
          <option value="none">{t('filters.statusNone')}</option>
        </select>
      </label>
      <label className="flex items-center gap-1.5 text-sm">
        <span className="text-muted-foreground">{t('filters.type')}</span>
        <select
          data-testid="filter-type"
          value={value.type}
          onChange={(e) => onChange({ ...value, type: e.target.value })}
          className="h-8 rounded-md border border-border bg-transparent px-2 text-sm"
        >
          <option value="">{t('filters.typeAll')}</option>
          {PRESET_TYPES.map((type) => (
            <option key={type} value={type}>
              {t(`type.${type}`)}
            </option>
          ))}
          {customTypes.map((type) => (
            <option key={type} value={type}>
              {type}
            </option>
          ))}
        </select>
      </label>
    </div>
  )
}

