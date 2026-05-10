import type { ReactNode } from 'react'
import type { LucideIcon } from 'lucide-react'

export function Panel({
  title,
  icon: Icon,
  suffix,
  children,
}: {
  title: string
  icon: LucideIcon
  suffix?: string
  children: ReactNode
}) {
  return (
    <section className="panel">
      <div className="panel-heading">
        <h2><Icon size={18} /> {title}</h2>
        {suffix && <span className="panel-suffix">{suffix}</span>}
      </div>
      {children}
    </section>
  )
}
