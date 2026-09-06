// Client-side mirror of the server's identifier classifier
// (internal/downloader/identifiers.go). It only drives the input
// preview badges on the downloader page — the server re-parses every
// line authoritatively, so a divergence here never loses data.

export type IdentifierKind = 'doi' | 'arxiv' | 'url' | 'invalid'

export type ParsedIdentifier = {
  kind: IdentifierKind
  /** The extracted DOI / arXiv id (or the full URL for url inputs). */
  label?: string
}

// Same shapes as the server: DOI prefix is case-insensitive per the DOI
// handbook; arXiv ids accept an optional "arXiv:" decoration and vN suffix.
const DOI_RE = /\b10\.\d{4,9}\/[^\s"<>]+/i
const ARXIV_PREFIX_RE = /^arxiv:\s*(.+)$/i
const ARXIV_NEW_RE = /\b\d{4}\.\d{4,5}(v\d+)?\b/i
const ARXIV_OLD_RE = /\b[a-z-]+(?:\.[a-z]{2})?\/\d{7}(v\d+)?\b/i

export function parseIdentifier(input: string): ParsedIdentifier {
  let s = input.trim()
  s = s.replace(/^["'`]+/, '').replace(/["'`]+$/, '')
  s = s.replace(/[,;]+$/, '')
  if (!s) return { kind: 'invalid' }

  if (/^https?:\/\//i.test(s)) {
    return { kind: 'url', label: s }
  }

  const doi = s.match(DOI_RE)
  if (doi) {
    return { kind: 'doi', label: doi[0].replace(/\.$/, '') }
  }

  let id = s
  const prefixed = s.match(ARXIV_PREFIX_RE)
  if (prefixed) id = prefixed[1]
  const arxiv = id.match(ARXIV_NEW_RE) ?? id.match(ARXIV_OLD_RE)
  if (arxiv) {
    return { kind: 'arxiv', label: arxiv[0] }
  }

  return { kind: 'invalid' }
}
