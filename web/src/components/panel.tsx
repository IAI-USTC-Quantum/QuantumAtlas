import type { ReactNode } from 'react'
import type { LucideIcon } from 'lucide-react'

import {
  Card,
  CardContent,
  CardHeader,
  CardTitle,
} from '@/components/ui/card'
import { cn } from '@/lib/utils'

/**
 * A titled panel used throughout the app for grouping content under a
 * single heading with an icon. Implemented as a shadcn `Card` so it
 * picks up the global border / shadow / radius / dark-mode tokens.
 */
export function Panel({
  title,
  icon: Icon,
  suffix,
  children,
  className,
  contentClassName,
}: {
  title: string
  icon: LucideIcon
  suffix?: ReactNode
  children: ReactNode
  className?: string
  contentClassName?: string
}) {
  return (
    <Card className={cn('gap-4 py-5', className)}>
      <CardHeader className="flex flex-row items-center justify-between gap-3 px-5">
        <CardTitle className="flex items-center gap-2 text-base font-semibold">
          <Icon className="size-4 text-primary" /> {title}
        </CardTitle>
        {suffix !== undefined && suffix !== '' && (
          <span className="text-xs font-medium text-muted-foreground">
            {suffix}
          </span>
        )}
      </CardHeader>
      <CardContent className={cn('px-5', contentClassName)}>
        {children}
      </CardContent>
    </Card>
  )
}
