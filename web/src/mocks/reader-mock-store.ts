// Mutable in-memory store behind the mock reader client (plan §12.1
// mock-first, §12.2 write contract, §12.1 status/maintainer decisions).
//
// The seed mirrors src/mocks/fixtures/*; write operations implement the
// SAME semantics the live server will have so the UI can be built and
// tested against them:
//   - Idempotency-Key: replay the original result for the same
//     key+fingerprint, 409 for the same key with a different request
//     (fingerprint = SHA-256 of method+path+body, §12.2).
//   - If-Match CAS on body edits: stale revision ⇒ 409.
//   - Status changes: root author or admin only, reason mandatory.
//   - Body edits: per-item author only (§12.1.2).
//   - Body budget: 1..20,000 Unicode chars (§12.2), 413 beyond.
import { BLOCKS_A, BLOCKS_B, DISCUSSIONS, PARSES, REPLY_FEED, REVISION_A, REVISION_B } from './reader-mock-seed'
import { ReaderApiError } from '@/lib/reader-shared'
import type {
  CreateDiscussionInput,
  DiscussionEvent,
  DiscussionSummary,
  ReaderBlock,
  ReplyEntry,
  ReplyInput,
  SetStatusInput,
} from '@/lib/reader-types'

export const MAX_BODY_CHARS = 20_000
export const TYPE_SLUG = /^[a-z0-9_:-]{1,64}$/

export type MockActor = { id: string; is_admin: boolean }

export type StoredDiscussion = DiscussionSummary & {
  replies: ReplyEntry[]
  events: DiscussionEvent[]
}

type State = {
  discussions: Map<string, StoredDiscussion>
  idempotency: Map<string, { fingerprint: string; result: unknown }>
  actor: MockActor
  clock: number
  seq: number
}

const DEFAULT_ACTOR: MockActor = { id: 'user_alma', is_admin: false }

let state: State | null = null

export function resetMockStore(): void {
  state = null
}

function store(): State {
  if (!state) {
    state = {
      discussions: new Map(
        DISCUSSIONS.map((d) => [d.discussion_id, {
          ...d,
          replies: (REPLY_FEED[d.discussion_id] ?? []).map((reply) => ({
            ...reply,
            discussion_id: d.discussion_id,
          })),
          events: [
            { kind: 'created' as const, actor: d.created_by, created_at: d.created_at },
            ...((REPLY_FEED[d.discussion_id] ?? []).map((reply) => ({
              kind: 'reply' as const,
              reply_id: reply.reply_id,
              actor: reply.created_by,
              created_at: reply.created_at,
            }))),
          ],
        }] as [string, StoredDiscussion]),
      ),
      idempotency: new Map(),
      actor: { ...DEFAULT_ACTOR },
      clock: Date.parse('2026-09-24T00:00:00Z'),
      seq: 0,
    }
  }
  return state
}

// Deterministic timestamps keep browser tests stable.
function nextStamp(): string {
  const s = store()
  s.clock += 1_000
  return new Date(s.clock).toISOString()
}

function nextId(prefix: string): string {
  const s = store()
  s.seq += 1
  return `${prefix}_${s.seq.toString(36).padStart(4, '0')}`
}

export function setMockActor(actor: MockActor): void {
  store().actor = { ...actor }
}

export function getMockActor(): MockActor {
  return { ...store().actor }
}

async function fingerprint(method: string, path: string, body: unknown): Promise<string> {
  const raw = new TextEncoder().encode(`${method}\n${path}\n${JSON.stringify(body ?? null)}`)
  const digest = await crypto.subtle.digest('SHA-256', raw)
  return Array.from(new Uint8Array(digest))
    .map((b) => b.toString(16).padStart(2, '0'))
    .join('')
}

// §12.2: same key + same request ⇒ replay the stored result; same key +
// different request ⇒ 409. Distinct keys never collide.
async function withIdempotency<T>(key: string, method: string, path: string, body: unknown, run: () => T): Promise<T> {
  const s = store()
  const print = await fingerprint(method, path, body)
  const seen = s.idempotency.get(key)
  if (seen) {
    if (seen.fingerprint !== print) {
      throw new ReaderApiError(409, 'idempotency key reused with a different request')
    }
    return seen.result as T
  }
  const result = run()
  s.idempotency.set(key, { fingerprint: print, result })
  return result
}

function requireBody(body: string): string {
  const trimmed = body.trim()
  if (trimmed.length === 0) throw new ReaderApiError(400, 'body must not be empty')
  if ([...body].length > MAX_BODY_CHARS) {
    throw new ReaderApiError(413, `body exceeds ${MAX_BODY_CHARS} characters`)
  }
  return body
}

export function blocksForRevision(revision: string): ReaderBlock[] {
  if (revision === REVISION_A) return BLOCKS_A
  if (revision === REVISION_B) return BLOCKS_B
  throw new ReaderApiError(404, `unknown parse revision ${revision}`)
}

function requireDiscussion(id: string): StoredDiscussion {
  const found = store().discussions.get(id)
  if (!found) throw new ReaderApiError(404, `discussion ${id} not found`)
  return found
}

function canManage(d: StoredDiscussion): boolean {
  const actor = store().actor
  return actor.is_admin || d.created_by === actor.id
}

// Public summary view of a stored discussion (no internal replies/events).
function toSummary(d: StoredDiscussion): DiscussionSummary {
  const summary = { ...d } as Partial<StoredDiscussion>
  delete summary.replies
  delete summary.events
  return summary as DiscussionSummary
}

// --- Write operations ---------------------------------------------------------

export async function mockCreateDiscussion(
  paperId: string,
  input: CreateDiscussionInput,
  idempotencyKey: string,
): Promise<DiscussionSummary> {
  return withIdempotency(idempotencyKey, 'POST', `/api/papers/${paperId}/discussions`, input, () => {
    const s = store()
    if (!input.parse_revision || !PARSES.some((p) => p.revision_id === input.parse_revision)) {
      throw new ReaderApiError(404, `unknown parse revision ${input.parse_revision}`)
    }
    // The anchor block must exist in that revision — no discussing
    // fictional blocks (golden-anchor semantics, §12.5).
    const block = blocksForRevision(input.parse_revision).find(
      (b) => b.page_idx === input.page_idx && b.index === input.block_index,
    )
    if (!block) {
      throw new ReaderApiError(
        404,
        `block page_idx=${input.page_idx} block_index=${input.block_index} not found in ${input.parse_revision}`,
      )
    }
    if (!TYPE_SLUG.test(input.type)) {
      throw new ReaderApiError(400, `invalid type slug: ${input.type}`)
    }
    if (input.scope !== 'public' && input.scope !== 'lean') {
      throw new ReaderApiError(400, `invalid scope: ${input.scope}`)
    }
    if (
      input.status !== undefined &&
      input.status !== null &&
      !['pending', 'confirmed', 'retracted'].includes(input.status)
    ) {
      throw new ReaderApiError(400, `invalid status: ${input.status}`)
    }
    requireBody(input.body)
    const actor = s.actor
    const now = nextStamp()
    const created: StoredDiscussion = {
      discussion_id: nextId('dsc'),
      paper_id: paperId,
      parse_revision: input.parse_revision,
      page_idx: input.page_idx,
      block_index: input.block_index,
      type: input.type,
      scope: input.scope,
      status: input.status ?? null,
      body: input.body,
      created_by: actor.id,
      model: input.model ?? null,
      revision: 1,
      reply_count: 0,
      created_at: now,
      updated_at: now,
      replies: [],
      events: [{ kind: 'created', actor: actor.id, created_at: now }],
    }
    if (input.status) {
      created.events.push({
        kind: 'status',
        from: null,
        to: input.status,
        reason: 'initial status',
        actor: actor.id,
        created_at: now,
      })
    }
    s.discussions.set(created.discussion_id, created)
    return toSummary(created)
  })
}

export async function mockAddReply(
  discussionId: string,
  input: ReplyInput,
  idempotencyKey: string,
): Promise<ReplyEntry> {
  return withIdempotency(
    idempotencyKey,
    'POST',
    `/api/discussions/${discussionId}/replies`,
    input,
    () => {
    const s = store()
    const discussion = requireDiscussion(discussionId)
    requireBody(input.body)
    const actor = s.actor
    const now = nextStamp()
    const reply: ReplyEntry = {
      reply_id: nextId('rpl'),
      discussion_id: discussionId,
      body: input.body,
      created_by: actor.id,
      model: input.model ?? null,
      revision: 1,
      created_at: now,
      updated_at: now,
    }
    discussion.replies.push(reply)
    discussion.reply_count += 1
    discussion.updated_at = now
    discussion.events.push({ kind: 'reply', reply_id: reply.reply_id, actor: actor.id, created_at: now })
    return { ...reply }
  })
}

export function mockSetStatus(discussionId: string, input: SetStatusInput): DiscussionSummary {
  const s = store()
  const discussion = requireDiscussion(discussionId)
  if (!canManage(discussion)) {
    throw new ReaderApiError(403, 'only the root author or a maintainer may change the status')
  }
  if (input.status !== null && !['pending', 'confirmed', 'retracted'].includes(input.status)) {
    throw new ReaderApiError(400, `invalid status: ${input.status}`)
  }
  if (typeof input.reason !== 'string' || input.reason.trim().length === 0) {
    throw new ReaderApiError(400, 'status changes require a reason')
  }
  const now = nextStamp()
  discussion.events.push({
    kind: 'status',
    from: discussion.status,
    to: input.status,
    reason: input.reason,
    actor: s.actor.id,
    created_at: now,
  })
  discussion.status = input.status
  discussion.revision += 1
  discussion.updated_at = now
  return toSummary(discussion)
}

export function mockEditDiscussionBody(
  discussionId: string,
  body: string,
  ifMatchRevision: number,
): DiscussionSummary {
  const s = store()
  const discussion = requireDiscussion(discussionId)
  if (discussion.created_by !== s.actor.id) {
    throw new ReaderApiError(403, 'only the author may edit this body')
  }
  if (ifMatchRevision !== discussion.revision) {
    throw new ReaderApiError(409, `revision mismatch: expected ${discussion.revision}`)
  }
  requireBody(body)
  const now = nextStamp()
  discussion.events.push({
    kind: 'body',
    target: 'discussion',
    target_id: discussionId,
    old: discussion.body,
    new: body,
    editor: s.actor.id,
    created_at: now,
  })
  discussion.body = body
  discussion.revision += 1
  discussion.updated_at = now
  return toSummary(discussion)
}

export function mockEditReplyBody(
  discussionId: string,
  replyId: string,
  body: string,
  ifMatchRevision: number,
): ReplyEntry {
  const s = store()
  const discussion = requireDiscussion(discussionId)
  const reply = discussion.replies.find((r) => r.reply_id === replyId)
  if (!reply) throw new ReaderApiError(404, `reply ${replyId} not found`)
  if (reply.created_by !== s.actor.id) {
    throw new ReaderApiError(403, 'only the author may edit this body')
  }
  if (ifMatchRevision !== reply.revision) {
    throw new ReaderApiError(409, `revision mismatch: expected ${reply.revision}`)
  }
  requireBody(body)
  const now = nextStamp()
  discussion.events.push({
    kind: 'body',
    target: 'reply',
    target_id: replyId,
    old: reply.body,
    new: body,
    editor: s.actor.id,
    created_at: now,
  })
  reply.body = body
  reply.revision += 1
  reply.updated_at = now
  return { ...reply }
}

// --- Read helpers over mutable state -------------------------------------------

export function listStoredDiscussions(): DiscussionSummary[] {
  return Array.from(store().discussions.values()).map(toSummary)
}

export function storedDiscussion(id: string): StoredDiscussion {
  return requireDiscussion(id)
}

export function storedRevisions(discussionId: string): { discussion_id: string; events: DiscussionEvent[] } {
  const discussion = requireDiscussion(discussionId)
  return { discussion_id: discussionId, events: [...discussion.events] }
}

