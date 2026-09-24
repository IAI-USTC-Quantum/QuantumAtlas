// Search input is intentionally stricter than the downloader's classifier:
// only an entire DOI or DOI-resolver URL becomes an identity lookup. A topic
// mentioning a DOI must stay a text query; extracting a substring loses intent.
export type SearchInput =
  | { doi: string; text?: never }
  | { text: string; doi?: never }

const DOI = /^10\.\d{4,9}\/\S+$/u
const CONTROL = /\p{Cc}/u

export function parseSearchInput(input: string): SearchInput {
  const text = input.trim()
  let candidate = text

  if (/^https?:\/\//i.test(text)) {
    // URL() silently removes tabs/newlines; don't let that turn prose or a
    // malformed pasted link into an unintended exact lookup.
    if (/\s/u.test(text) || CONTROL.test(text)) return { text }
    try {
      const url = new URL(text)
      if (!['doi.org', 'dx.doi.org'].includes(url.hostname.toLowerCase()) ||
          url.username || url.password || url.port) return { text }
      // Only decode resolver paths, once. Literal DOI suffixes can contain
      // percent signs, punctuation and slashes; never strip those heuristically.
      candidate = decodeURIComponent(url.pathname.slice(1))
    } catch {
      return { text }
    }
  } else {
    candidate = candidate.replace(/^doi:\s*/i, '')
  }

  if (!DOI.test(candidate) || CONTROL.test(candidate)) return { text }
  return { doi: candidate.toLowerCase() }
}
