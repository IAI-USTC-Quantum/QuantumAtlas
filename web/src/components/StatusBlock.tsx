import type { ReactNode } from 'react'

export function StatusBlock({
  loading,
  error,
  empty,
  children,
}: {
  loading: boolean
  error: string
  empty: boolean
  children: ReactNode
}) {
  if (loading) return <div className="notice">Loading...</div>
  if (error) return <div className="notice danger">{error}</div>
  if (empty) return <div className="notice">No records yet.</div>
  return <>{children}</>
}
