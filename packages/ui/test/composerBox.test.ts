import { describe, expect, it } from 'vitest'
import { SendUserMessageRequest_Delivery, type SessionEvent } from '~/gen/harness/agent/v1/agent_pb'
import { attachmentError, attachmentKind, messageContent, readAttachments } from '~/lib/session/attachments'
import { textOf } from '~/lib/session/reducer'
import { MockTransport } from '~/lib/transport/mock'
import { fileMentionOptions, folderLookup, folderMention, NEW_SESSION_COMMANDS, SLASH_COMMANDS } from '~/utils/composer'

describe('the composer box before a session exists', () => {
  it('offers only the commands that make sense without a conversation', () => {
    const keys = NEW_SESSION_COMMANDS.map(c => c.key)
    expect(keys).not.toContain('/compact')
    expect(keys).not.toContain('/clear')
    expect(keys).not.toContain('/rewind')
    expect(keys.every(k => SLASH_COMMANDS.some(c => c.key === k))).toBe(true)
    expect(keys.length).toBeGreaterThan(0)
  })

  it('mentions a picked folder relative to the session folder, absolute without one', () => {
    expect(folderMention('/w/app/src', '/w/app')).toBe('@src/ ')
    expect(folderMention('/w/app', '/w/app/')).toBe('@./ ')
    expect(folderMention('/w/other', '/w/app')).toBe('@/w/other/ ')
    expect(folderMention('/w/other', '')).toBe('@/w/other/ ')
  })

  it('reads an @ query as a directory to list and a name to match', () => {
    expect(folderLookup('/w/app', '')).toEqual({ dir: '/w/app', sub: '', match: '' })
    expect(folderLookup('/w/app/', 'Pack')).toEqual({ dir: '/w/app', sub: '', match: 'pack' })
    expect(folderLookup('/w/app', 'packages/u')).toEqual({ dir: '/w/app/packages', sub: 'packages/', match: 'u' })
    expect(folderLookup('/w/app', '/etc/')).toEqual({ dir: '/etc', sub: '/etc/', match: '' })
  })

  it('names mention rows after the last part of the path', () => {
    expect(fileMentionOptions(['packages/ui/', 'docs/PLAN.md']).map(o => [o.label, o.detail, o.icon])).toEqual([
      ['ui/', 'packages', 'folder'],
      ['PLAN.md', 'docs', 'file']
    ])
  })
})

describe('attachments', () => {
  it('sorts files by how they can travel', () => {
    expect(attachmentKind({ name: 'a.png', type: 'image/png' })).toBe('image')
    expect(attachmentKind({ name: 'a.pdf', type: 'application/pdf' })).toBe('document')
    expect(attachmentKind({ name: 'a.ts', type: '' })).toBe('text')
    expect(attachmentKind({ name: 'a.heic', type: 'image/heic' })).toBe(null)
    expect(attachmentKind({ name: 'a.mp4', type: 'video/mp4' })).toBe(null)
    expect(attachmentKind({ name: 'a.zip', type: 'application/zip' })).toBe(null)
  })

  it('says why a file cannot go', () => {
    expect(attachmentError({ name: 'a.png', type: 'image/png', size: 10 })).toBeUndefined()
    expect(attachmentError({ name: 'a.png', type: 'image/png', size: 60 * 1024 * 1024 })).toMatch(/too large/)
    expect(attachmentError({ name: 'a.mp4', type: 'video/mp4', size: 10 })).toMatch(/only images/)
    expect(attachmentError({ name: 'a.log', type: 'text/plain', size: 2 * 1024 * 1024 })).toMatch(/too large/)
  })

  it('reads the bytes, and refuses what only looked like text', async () => {
    const png = new File([new Uint8Array([137, 80, 78, 71])], 'a.png', { type: 'image/png' })
    const [a] = await readAttachments([{ file: png }, {}])
    expect([...a!.data]).toEqual([137, 80, 78, 71])
    expect(a!.mediaType).toBe('image/png')
    const bin = new File([new Uint8Array([0xFF, 0xFE, 0x00])], 'a.bin', { type: '' })
    await expect(readAttachments([{ file: bin }])).rejects.toThrow(/a\.bin/)
  })

  it('becomes content blocks: images and documents first, the typed text before text files', () => {
    const data = new Uint8Array([1, 2, 3])
    const blocks = messageContent('look', [
      { name: 'a.log', kind: 'text', mediaType: 'text/plain', data: new TextEncoder().encode('line') },
      { name: 'a.png', kind: 'image', mediaType: 'image/png', data },
      { name: 'a.pdf', kind: 'document', mediaType: 'application/pdf', data }
    ])
    expect(blocks.map(b => b.block.case)).toEqual(['image', 'document', 'text', 'text'])
    expect(blocks[0]!.block.value).toMatchObject({ mediaType: 'image/png', data })
    expect(blocks[1]!.block.value).toMatchObject({ title: 'a.pdf' })
    expect(textOf(blocks)).toBe('look\n<file name="a.log">\nline\n</file>')
    expect(messageContent('just text').map(b => b.block.case)).toEqual(['text'])
  })

  it('reach the session through the transport, also from the queue', async () => {
    const t = new MockTransport({ tick: 1 })
    const { id } = await t.createSession({ cwd: '' })
    const events: SessionEvent[] = []
    const stop = t.subscribe(id, 0, { onEvent: e => events.push(e) })
    const data = new Uint8Array([9])
    await t.sendUserMessage(id, 'u1', 'first', SendUserMessageRequest_Delivery.QUEUE, [{ name: 'a.png', kind: 'image', mediaType: 'image/png', data }])
    stop()
    const user = events.find(e => e.event.case === 'userMessage')!
    expect(user.event.case === 'userMessage' && user.event.value.content.map(b => b.block.case)).toEqual(['image', 'text'])
  })
})
