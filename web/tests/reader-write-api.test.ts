import { beforeEach, describe, expect, it } from 'vitest'
import { mockClient, REVISION_A, REVISION_B } from '@/mocks/reader-mock-api'
import {
  getMockActor,
  resetMockStore,
  setMockActor,
} from '@/mocks/reader-mock-store'
import { MOCK_PAPER_ID } from '@/lib/reader-shared'

// §12.2 write-contract tests against the mock store. The mock implements
// the same semantics the live server will enforce, so these double as the
// executable contract for Q5 integration.
const client = mockClient()

beforeEach(() => {
  globalThis.fetch = (async () => new Response(new Uint8Array([1, 2, 3]), { status: 200 })) as typeof fetch
  resetMockStore()
})

async function expectApiError(promise: Promise<unknown>, status: number) {
  await expect(promise).rejects.toMatchObject({ status })
}

const ANCHOR = { parse_revision: REVISION_A, page_idx: 0, block_index: 5 }

function createInput(overrides: Record<string, unknown> = {}) {
  return {
    ...ANCHOR,
    type: 'normal',
    scope: 'public' as const,
    status: null,
    body: 'A verification note with evidence.',
    model: null,
    ...overrides,
  }
}

describe('mock write: createDiscussion', () => {
  it('creates a discussion bound to the exact anchor', async () => {
    const created = await client.createDiscussion(MOCK_PAPER_ID, createInput(), 'key-1')
    expect(created.discussion_id).toMatch(/^dsc_/)
    expect(created.created_by).toBe(getMockActor().id)
    expect(created.status).toBeNull()
    expect(created.revision).toBe(1)
    // Visible through the combined block read.
    const reading = await client.readBlock(MOCK_PAPER_ID, REVISION_A, 0, 5)
    expect(reading.discussions.map((d) => d.discussion_id)).toContain(created.discussion_id)
  })

  it('replays the same result for a repeated idempotency key', async () => {
    const input = createInput()
    const first = await client.createDiscussion(MOCK_PAPER_ID, input, 'idem-a')
    const replay = await client.createDiscussion(MOCK_PAPER_ID, input, 'idem-a')
    expect(replay.discussion_id).toBe(first.discussion_id)
    const reading = await client.readBlock(MOCK_PAPER_ID, REVISION_A, 0, 5)
    expect(reading.discussions).toHaveLength(1)
  })

  it('409s when the same key carries a different request', async () => {
    await client.createDiscussion(MOCK_PAPER_ID, createInput(), 'idem-b')
    await expectApiError(
      client.createDiscussion(MOCK_PAPER_ID, createInput({ body: 'different' }), 'idem-b'),
      409,
    )
  })

  it('404s for anchors that do not exist (golden anchors, no fallback)', async () => {
    await expectApiError(
      client.createDiscussion(MOCK_PAPER_ID, createInput({ block_index: 3 }), 'k'),
      404,
    )
    await expectApiError(
      client.createDiscussion(MOCK_PAPER_ID, createInput({ parse_revision: REVISION_B, page_idx: 1 }), 'k2'),
      404,
    )
  })

  it('validates type slug, scope, status and the body budget', async () => {
    await expectApiError(client.createDiscussion(MOCK_PAPER_ID, createInput({ type: 'Bad Slug' }), 'k'), 400)
    await expectApiError(client.createDiscussion(MOCK_PAPER_ID, createInput({ type: 'x'.repeat(65) }), 'k'), 400)
    await expectApiError(client.createDiscussion(MOCK_PAPER_ID, createInput({ scope: 'secret' }), 'k'), 400)
    await expectApiError(client.createDiscussion(MOCK_PAPER_ID, createInput({ status: 'done' }), 'k'), 400)
    await expectApiError(client.createDiscussion(MOCK_PAPER_ID, createInput({ body: '' }), 'k'), 400)
    await expectApiError(
      client.createDiscussion(MOCK_PAPER_ID, createInput({ body: 'x'.repeat(20_001) }), 'k'),
      413,
    )
  })
})

describe('mock write: replies', () => {
  it('adds a reply, bumps counts and replays idempotently', async () => {
    const reply1 = await client.addReply('dsc_03', { body: 'first reply', model: null }, 'r-key')
    expect(reply1.reply_id).toMatch(/^rpl_/)
    const replay = await client.addReply('dsc_03', { body: 'first reply', model: null }, 'r-key')
    expect(replay.reply_id).toBe(reply1.reply_id)
    const detail = await client.getDiscussion('dsc_03')
    expect(detail.replies).toHaveLength(1)
    expect(detail.reply_count).toBe(1)
    await expectApiError(client.addReply('dsc_missing', { body: 'x', model: null }, 'rk'), 404)
  })
})

describe('mock write: status machine', () => {
  it('lets the root author change status with a mandatory reason', async () => {
    setMockActor({ id: 'user_bo', is_admin: false }) // dsc_02 root author
    const updated = await client.setStatus('dsc_02', { status: 'confirmed', reason: 'verified against v3 bytes' })
    expect(updated.status).toBe('confirmed')
    const revisions = await client.listRevisions('dsc_02')
    const events = revisions.events.filter((e) => e.kind === 'status')
    expect(events).toHaveLength(1)
    expect(events[0]).toMatchObject({ from: 'pending', to: 'confirmed', reason: 'verified against v3 bytes', actor: 'user_bo' })
  })

  it('403s for other users, allows admins, and 400s without a reason', async () => {
    setMockActor({ id: 'user_cleo', is_admin: false })
    await expectApiError(client.setStatus('dsc_02', { status: 'confirmed', reason: 'nope' }), 403)
    setMockActor({ id: 'user_cleo', is_admin: true })
    const updated = await client.setStatus('dsc_02', { status: 'retracted', reason: 'admin retraction with evidence' })
    expect(updated.status).toBe('retracted')
    setMockActor({ id: 'user_bo', is_admin: false })
    await expectApiError(client.setStatus('dsc_02', { status: 'pending', reason: '   ' }), 400)
  })

  it('keeps two discussions on the same block independent', async () => {
    setMockActor({ id: 'user_alma', is_admin: false })
    await client.setStatus('dsc_01', { status: 'pending', reason: 'reopen for new evidence' })
    const reading = await client.readBlock(MOCK_PAPER_ID, REVISION_A, 0, 2)
    const statuses = Object.fromEntries(
      reading.discussions.map((d) => [d.discussion_id, d.status]),
    )
    expect(statuses['dsc_01']).toBe('pending')
    expect(statuses['dsc_02']).toBe('pending') // untouched: it was already pending
    await client.setStatus('dsc_01', { status: 'retracted', reason: 'actually a scan artifact' })
    const after = await client.readBlock(MOCK_PAPER_ID, REVISION_A, 0, 2)
    expect(after.discussions.find((d) => d.discussion_id === 'dsc_02')?.status).toBe('pending')
  })
})

describe('mock write: body edits under If-Match CAS', () => {
  it('author edits succeed and record old/new in history', async () => {
    setMockActor({ id: 'user_alma', is_admin: false })
    const updated = await client.editDiscussionBody('dsc_01', 'revised root body', 3)
    expect(updated.body).toBe('revised root body')
    expect(updated.revision).toBe(4)
    const revisions = await client.listRevisions('dsc_01')
    const bodyEvent = revisions.events.find((e) => e.kind === 'body')
    expect(bodyEvent).toMatchObject({ target: 'discussion', old: expect.stringContaining('transcribed exponent'), new: 'revised root body', editor: 'user_alma' })
  })

  it('409s on stale revisions and 403s for non-authors', async () => {
    setMockActor({ id: 'user_alma', is_admin: false })
    await expectApiError(client.editDiscussionBody('dsc_01', 'stale write', 1), 409)
    setMockActor({ id: 'user_bo', is_admin: false })
    await expectApiError(client.editDiscussionBody('dsc_01', 'not my post', 3), 403)
  })

  it('reply edits are author-only and CAS-guarded', async () => {
    setMockActor({ id: 'user_bo', is_admin: false }) // rpl_0101 author
    const updated = await client.editReplyBody('dsc_01', 'rpl_0101', 'checked again, still faithful', 1)
    expect(updated.body).toBe('checked again, still faithful')
    expect(updated.revision).toBe(2)
    await expectApiError(client.editReplyBody('dsc_01', 'rpl_0102', 'hijack', 1), 403)
    await expectApiError(client.editReplyBody('dsc_01', 'rpl_0101', 'stale', 1), 409)
    await expectApiError(client.editReplyBody('dsc_01', 'rpl_XXXX', 'missing', 1), 404)
  })
})

describe('mock write: default actor and reset', () => {
  it('defaults to user_alma and resets cleanly', async () => {
    expect(getMockActor()).toEqual({ id: 'user_alma', is_admin: false })
    const created = await client.createDiscussion(MOCK_PAPER_ID, createInput(), 'k')
    expect(created.discussion_id).toBeTruthy()
    resetMockStore()
    const reading = await client.readBlock(MOCK_PAPER_ID, REVISION_A, 0, 5)
    expect(reading.discussions).toHaveLength(0)
  })
})
