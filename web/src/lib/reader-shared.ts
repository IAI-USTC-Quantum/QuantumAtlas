// Leaf module shared by the reader API contract and the mock client.
// Keeping these runtime values here breaks the import cycle
// reader-api.ts ⇄ reader-mock-api.ts (the mock dataset evaluates
// module-level consts that must not touch a half-initialized module).
import type { DiscussionFilters, DiscussionScope, DiscussionStatus } from './reader-types'
// The mock dataset is bound to one synthetic paper (repo fixture
// tests/fixtures/blockcomments); any other id answers a structured
// not-found so the UI shows "reader data unavailable" instead of faking
// coverage.
export const MOCK_PAPER_ID = 'qa_01J5SYNTHETICFIXTURE0001'
export const MOCK_PDF_PATH = '/fixtures/blockcomments/minimal-2page.pdf'

export class ReaderApiError extends Error {
  status: number
  constructor(status: number, message: string) {
    super(message)
    this.name = 'ReaderApiError'
    this.status = status
  }
}

// Scope read resolution (plan §12.1): no filter = everything; Lean filter =
// public + Lean. Applied identically by the mock and (later) the server.
export function scopeMatches(filter: DiscussionFilters['scope'], scope: DiscussionScope): boolean {
  if (!filter) return true
  if (filter === 'lean') return scope === 'public' || scope === 'lean'
  return scope === filter
}

export function statusMatches(
  filter: DiscussionFilters['status'],
  status: DiscussionStatus | null,
): boolean {
  if (!filter) return true
  if (filter === 'none') return status === null
  return status === filter
}
