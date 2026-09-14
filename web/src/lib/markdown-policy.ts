import {
  MAX_FORMULAS, MAX_RENDERED_DEPTH, MAX_RENDERED_NODES, MAX_MATH_HTML_LENGTH,
} from './markdown-limits'

// Narrower than react-markdown's safe default: this app also blocks navigation
// to relative API/assets, credential URLs, and control/format characters.
export function isAllowedMarkdownLink(value: string): boolean {
  if (!value || /[\s\p{Cc}\p{Cf}\\]/u.test(value) || /%(?:0[\da-f]|1[\da-f]|7f|5c)/i.test(value)) return false
  if (value.startsWith('#')) return true
  if (/^mailto:[^?]+/i.test(value)) return true
  if (!/^https?:\/\//i.test(value)) return false
  try {
    const url = new URL(value)
    return Boolean(url.hostname) && !url.username && !url.password
  } catch {
    return false
  }
}

type TreeNode = { type: string; children?: TreeNode[]; value?: string; properties?: object }

// Resource preflight over library-produced trees, NOT a second renderer or a
// decoder of Worker messages. No raw/MDX parser is enabled; raw nodes are left
// for react-markdown's own visible-text handling. Iterative to avoid recursing
// into a deep tree before checking its depth. Count raw/text and prop bytes too.
export function checkMarkdownTreeBudget(tree: TreeNode, checkFormulas = false): number {
  const stack = [{ node: tree, depth: 0 }]
  let nodes = 0
  let characters = 0
  let formulas = 0
  while (stack.length) {
    const { node, depth } = stack.pop()!
    if (++nodes > MAX_RENDERED_NODES || depth > MAX_RENDERED_DEPTH) throw new RangeError('Markdown structure too large')
    if (node.value) characters += node.value.length
    if (node.properties) {
      for (const value of Object.values(node.properties)) {
        if (typeof value === 'string') characters += value.length
        else if (Array.isArray(value)) characters += value.join(' ').length
      }
    }
    if (characters > MAX_MATH_HTML_LENGTH) throw new RangeError('Markdown output too large')
    if (checkFormulas && (node.type === 'math' || node.type === 'inlineMath') && ++formulas > MAX_FORMULAS) {
      throw new RangeError('Too many formulas for one preview')
    }
    if (node.children) {
      if (node.children.length + nodes > MAX_RENDERED_NODES) throw new RangeError('Markdown structure too large')
      for (const child of node.children) stack.push({ node: child, depth: depth + 1 })
    }
  }
  return nodes
}
