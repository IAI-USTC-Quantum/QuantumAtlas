// Application preview limits, not limits imposed by react-markdown.
// Main-thread parsing is synchronous: these are size/complexity guards, NOT a
// cancellable timeout. See MARKDOWN_PREVIEW.md for measured long-document data.
export const MAX_MARKDOWN_LENGTH = 200_000
export const MAX_FORMULA_LENGTH = 10_000
// The 399-formula stress case spent seconds expanding before hitting the DOM
// cap. Reject that density before KaTeX; the measured mixed corpus has up to153.
export const MAX_FORMULAS = 200
export const MAX_MATH_HTML_LENGTH = 2_000_000
export const MAX_RENDERED_NODES = 20_000
export const MAX_RENDERED_DEPTH = 64
