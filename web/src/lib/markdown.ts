import { Marked, type Tokens } from 'marked'
import markedKatex from 'marked-katex-extension'
import DOMPurify from 'dompurify'

// Obsidian-style wikilink: [[id]] or [[id|label]]. We render these as
// internal anchors to the wiki detail route. The href is intercepted by
// a click handler on the rendering container (see WikiContent) so SPA
// navigation is preserved instead of a full page reload.
const WIKILINK_RE = /^\[\[([^\]|]+)(?:\|([^\]]+))?\]\]/

type WikilinkToken = Tokens.Generic & {
  type: 'wikilink'
  id: string
  label: string
}

function wikilinkExtension(lang: string) {
  return {
    name: 'wikilink',
    level: 'inline' as const,
    start(src: string) {
      const i = src.indexOf('[[')
      return i < 0 ? undefined : i
    },
    tokenizer(src: string): WikilinkToken | undefined {
      const m = WIKILINK_RE.exec(src)
      if (!m) return undefined
      const id = m[1].trim()
      const label = (m[2] ?? m[1]).trim()
      return { type: 'wikilink', raw: m[0], id, label }
    },
    renderer(token: Tokens.Generic) {
      const tok = token as WikilinkToken
      const href = `/${lang}/wiki/page/${encodeURIComponent(tok.id)}`
      const label = escapeHtml(tok.label)
      return `<a href="${href}" data-wikilink="true" class="wikilink">${label}</a>`
    },
  }
}

function escapeHtml(s: string) {
  return s
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;')
}

// One Marked instance per language (wikilink hrefs are lang-scoped).
const instances = new Map<string, Marked>()

function markedFor(lang: string): Marked {
  let m = instances.get(lang)
  if (!m) {
    m = new Marked()
    m.use(markedKatex({ throwOnError: false, nonStandard: true }))
    m.use({ extensions: [wikilinkExtension(lang)] })
    instances.set(lang, m)
  }
  return m
}

/**
 * Render wiki markdown to sanitized HTML. Supports:
 *  - GitHub-flavoured markdown (via marked)
 *  - `$...$` / `$$...$$` math (via KaTeX)
 *  - `[[id|label]]` wikilinks -> internal /{lang}/wiki/page/{id} anchors
 *
 * Output is DOMPurify-sanitized. We allow the `data-wikilink` attribute
 * (used for SPA click interception) and KaTeX's MathML output, which
 * DOMPurify supports natively.
 */
export function renderMarkdown(content: string, lang: string): string {
  const raw = markedFor(lang).parse(content ?? '', { async: false }) as string
  return DOMPurify.sanitize(raw, { ADD_ATTR: ['data-wikilink'] })
}
