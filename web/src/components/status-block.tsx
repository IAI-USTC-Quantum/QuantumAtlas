import type { ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { AlertCircle } from 'lucide-react'

import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Skeleton } from '@/components/ui/skeleton'

/**
 * Renders loading / error / empty states with consistent visuals so
 * every page doesn't reinvent its own "no data" markup. Translated via
 * the `common.status.*` namespace.
 */
export function StatusBlock({
  loading,
  error,
  empty,
  children,
  emptyMessage,
}: {
  loading: boolean
  error: string
  empty: boolean
  children: ReactNode
  emptyMessage?: string
}) {
  const { t } = useTranslation('common')

  if (loading) {
    return (
      <div className="space-y-3">
        <Skeleton className="h-5 w-2/3" />
        <Skeleton className="h-5 w-1/2" />
        <Skeleton className="h-5 w-3/4" />
      </div>
    )
  }
  if (error) {
    return (
      <Alert variant="destructive">
        <AlertCircle className="size-4" />
        <AlertTitle>{t('status.errorTitle')}</AlertTitle>
        <AlertDescription>{error}</AlertDescription>
      </Alert>
    )
  }
  if (empty) {
    return (
      <div className="rounded-md border border-dashed border-border bg-muted/30 px-4 py-6 text-center text-sm text-muted-foreground">
        {emptyMessage ?? t('status.empty')}
      </div>
    )
  }
  return <>{children}</>
}
