import { describe, expect, it } from 'vitest'
import { SendUserMessageRequest_Delivery, type SessionEvent } from '~/gen/harness/agent/v1/agent_pb'
import { attachmentError, attachmentFiles, attachmentKind, blockAttachments, clipboardFiles, messageContent, readAttachments, sniffImageType } from '~/lib/session/attachments'
import { textOf } from '~/lib/session/reducer'
import { MockTransport } from '~/lib/transport/mock'
import { fileMentionOptions, folderLookup, folderMention } from '~/utils/composer'

describe('the composer box before a session exists', () => {
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

  it('takes a type from the name when a webview gives none', () => {
    expect(attachmentKind({ name: 'shot.PNG', type: '' })).toBe('image')
    expect(attachmentKind({ name: 'a.jpg', type: 'image/jpg' })).toBe('image')
    expect(attachmentKind({ name: 'a.pdf', type: '' })).toBe('document')
    expect(attachmentError({ name: 'a.heic', type: 'image/heic', size: 1 })).toMatch(/PNG, JPEG/)
  })

  it('sends an image as what its bytes are, whatever its name says', async () => {
    const jpeg = new File([new Uint8Array([0xFF, 0xD8, 0xFF, 0xE0])], 'a.png', { type: 'image/png' })
    const [a] = await readAttachments([{ file: jpeg }])
    expect(a!.mediaType).toBe('image/jpeg')
    expect(sniffImageType(new Uint8Array([1, 2, 3]))).toBeUndefined()
  })

  it('goes without text when there is none: a provider refuses an empty block', () => {
    const data = new Uint8Array([1])
    expect(messageContent('  ', [{ name: 'a.png', kind: 'image', mediaType: 'image/png', data }]).map(b => b.block.case)).toEqual(['image'])
  })

  it('shows in the message: an image with its bytes, a text file as a file rather than as words', () => {
    const data = new Uint8Array([1, 2, 3])
    const blocks = messageContent('look', [
      { name: 'a.png', kind: 'image', mediaType: 'image/png', data },
      { name: 'a.log', kind: 'text', mediaType: 'text/plain', data: new TextEncoder().encode('line') }
    ])
    const [image, file] = blockAttachments(blocks)
    expect(image).toMatchObject({ kind: 'image', mediaType: 'image/png', size: 3, url: 'data:image/png;base64,AQID' })
    expect(file).toMatchObject({ kind: 'document', name: 'a.log', mediaType: 'text/plain', size: 4 })
  })

  it('come back as files with their bytes when a queued message is taken back', async () => {
    const [f] = await attachmentFiles([{ kind: 'image', name: 'image', mediaType: 'image/png', size: 3, url: 'data:image/png;base64,AQID' }])
    expect(f!.name).toBe('image.png')
    expect([...new Uint8Array(await f!.arrayBuffer())]).toEqual([1, 2, 3])
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

describe('pasting', () => {
  const png = new File([new Uint8Array([1])], 'image.png', { type: 'image/png' })
  const clip = (d: { types?: string[], files?: File[], items?: { kind: string, getAsFile: () => File | null }[], plain?: string }) => ({
    types: d.types ?? [],
    files: d.files ?? [],
    items: d.items ?? [],
    getData: (t: string) => t === 'text/plain' ? d.plain ?? '' : ''
  }) as unknown as DataTransfer

  it('leaves plain text to the field', () => {
    expect(clipboardFiles(clip({ types: ['text/plain'], plain: 'hello' }))).toEqual([])
  })

  it('takes an image from the clipboard', () => {
    expect(clipboardFiles(clip({ types: ['Files'], files: [png] }))).toEqual([png])
    expect(clipboardFiles(clip({ types: ['text/html', 'Files'], files: [png] }))).toEqual([png])
  })

  it('finds it among the items when the list of files is empty', () => {
    expect(clipboardFiles(clip({ types: ['Files'], items: [{ kind: 'string', getAsFile: () => null }, { kind: 'file', getAsFile: () => png }] }))).toEqual([png])
  })

  it('takes a file copied in a file manager, whose name comes along as text', () => {
    expect(clipboardFiles(clip({ types: ['text/plain', 'Files'], files: [png], plain: 'image.png' }))).toEqual([png])
  })

  it('keeps cells and rich text as text, though they carry a picture of themselves', () => {
    expect(clipboardFiles(clip({ types: ['text/plain', 'text/html', 'Files'], files: [png], plain: 'a\tb' }))).toEqual([])
  })
})
