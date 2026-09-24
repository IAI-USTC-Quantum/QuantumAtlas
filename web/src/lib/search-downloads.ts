import type { SearchHit } from '@/lib/api'
import { parseIdentifier } from '@/lib/identifiers'

type Identifier = { input: string; alias: string }

// Conservative subset of internal/downloader/identifiers.go: generic web URLs
// are not downloadable unless they contain a DOI. Never submit catalog IDs.
function identifier(value: string): Identifier | undefined {
  const parsed = parseIdentifier(value)
  if (parsed.kind === 'doi') return { input: parsed.label!, alias: `doi:${parsed.label!.toLowerCase()}` }
  if (parsed.kind === 'arxiv') {
    const bare = value.trim().replace(/^arxiv:\s*/i, '')
    if (bare !== parsed.label) return
    return { input: bare, alias: `arxiv:${bare.replace(/v\d+$/i, '').toLowerCase()}` }
  }
  if (parsed.kind !== 'url') return
  try {
    const url = new URL(value)
    if (!['http:', 'https:'].includes(url.protocol) || url.username || url.password) return
    const path = decodeURIComponent(url.pathname)
    if (/^(?:www\.)?(?:arxiv\.org|export\.arxiv\.org)$/i.test(url.hostname)) {
      const id = /^\/(?:abs|pdf|format)\//.test(path)
        ? path.replace(/^\/(?:abs|pdf|format)\//, '').replace(/\/$/, '').replace(/\.pdf$/i, '')
        : url.searchParams.get('id_list')
      return id ? identifier(id) : undefined
    }
    if (url.hostname.toLowerCase() === 'pmc.ncbi.nlm.nih.gov') {
      const pmc = path.match(/^\/articles\/(PMC\d+)(?:\/|$)/i)?.[1]
      if (pmc) return { input: url.href, alias: `pmc:${pmc.toUpperCase()}` }
    }
    const doi = parseIdentifier(path)
    if (doi.kind === 'doi') return { input: doi.label!, alias: `doi:${doi.label!.toLowerCase()}` }
  } catch { /* malformed or unsupported URL */ }
}

// Titles, paper IDs, OpenAlex IDs and generic web links are not identifiers.
export function downloadInput(hit: SearchHit): string | undefined {
  for (const value of [hit.arxiv_id, hit.doi, hit.url]) {
    const parsed = value ? identifier(value) : undefined
    if (parsed) return parsed.input
  }
}

export function downloadAliases(hit: SearchHit): string[] {
  const aliases: string[] = []
  if (hit.paper_id) aliases.push(`paper:${hit.paper_id}`)
  for (const value of [hit.arxiv_id, hit.doi, hit.url]) {
    const parsed = value ? identifier(value) : undefined
    if (parsed) aliases.push(parsed.alias)
  }
  return aliases
}

export type DownloadChoice = { key: string; input: string; title: string; aliases: Set<string> }

// Merge all aliases, including bridge hits carrying both DOI and arXiv IDs.
// Tab mounting/unmounting must not change the unique paper count.
export function downloadChoices(hits: SearchHit[]): DownloadChoice[] {
  const choices: DownloadChoice[] = []
  for (const hit of hits) {
    const input = downloadInput(hit)
    if (!input) continue
    const aliases = downloadAliases(hit)
    const matches = choices.filter((choice) => aliases.some((alias) => choice.aliases.has(alias)))
    const choice = matches[0] ?? { key: aliases[0] ?? input, input, title: hit.title || input, aliases: new Set<string>() }
    if (!matches.length) choices.push(choice)
    for (const alias of aliases) choice.aliases.add(alias)
    for (const duplicate of matches.slice(1)) {
      for (const alias of duplicate.aliases) choice.aliases.add(alias)
      choices.splice(choices.indexOf(duplicate), 1)
    }
  }
  return choices
}
