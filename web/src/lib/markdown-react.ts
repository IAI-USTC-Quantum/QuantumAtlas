import type { ReactNode } from 'react'
import { Fragment, jsx, jsxs } from 'react/jsx-runtime'
import { toJsxRuntime } from 'hast-util-to-jsx-runtime'
import { decodeMarkdownReply } from './markdown-tree'

// The same HAST->JSX utility used by react-markdown/rehype-react, without parsing
// the source a second time on the UI thread. No component map, evaluater, raw
// HTML sink, passNode or Worker-supplied JSX/props. Decode BEFORE conversion.
export function markdownReplyToReact(reply: unknown): ReactNode {
  return toJsxRuntime(decodeMarkdownReply(reply), {
    Fragment, jsx, jsxs, elementAttributeNameCase: 'react',
    tableCellAlignToStyle: false,
  }) as ReactNode
}
