// Shared with the UI without importing the parser or KaTeX into its bundle.
export const MAX_MARKDOWN_LENGTH = 200_000
export const MAX_RENDERED_TREE_LENGTH = 2_000_000
export const MAX_RENDERED_NODES = 20_000
export const MAX_RENDERED_DEPTH = 64
export const MARKDOWN_RENDER_TIMEOUT_MS = 5_000

// A bounded string lets the receiver check size BEFORE parsing/traversing an
// untrusted object graph. JSX, components, and executable properties never cross.
export type MarkdownWorkerReply =
  | { ok: true; treeJson: string }
  | { ok: false }
