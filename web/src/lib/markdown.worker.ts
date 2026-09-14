import { renderMarkdownToTree } from './markdown-renderer'
import { type MarkdownWorkerReply } from './markdown-protocol'

// One disposable worker per preview. Parsing, HTML->HAST for KaTeX only, and
// macro expansion cannot block the UI. The caller terminates us on timeout,
// source changes, or unmount. There is no main-thread parse fallback.
self.onmessage = (event: MessageEvent<unknown>) => {
  let reply: MarkdownWorkerReply
  try {
    if (typeof event.data !== 'string') throw new Error('Invalid markdown input')
    reply = { ok: true, treeJson: JSON.stringify(renderMarkdownToTree(event.data)) }
  } catch {
    // Never forward parser exceptions or untrusted source as error markup.
    reply = { ok: false }
  }
  self.postMessage(reply)
}
