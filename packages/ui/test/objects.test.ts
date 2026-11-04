import { afterEach, describe, expect, it, vi } from 'vitest'
import { create } from '@bufbuild/protobuf'
import { ContentBlockSchema } from '~/gen/harness/llm/v1/content_pb'
import { ObjectRefSchema } from '~/gen/harness/common/v1/object_pb'
import { toolImages } from '~/lib/session/computer'
import { fetchObject, objectUrl, setObjectTicketSource, type ObjectTicket } from '~/lib/session/objects'
import { textOf } from '~/lib/session/reducer'

const sha256 = 'ab'.repeat(32)
const ticket = (over: Partial<ObjectTicket> = {}): ObjectTicket => ({
  available: true, ticket: 'exp.sig', expiresAt: new Date(Date.now() + 12 * 3600_000).toISOString(), baseUrl: 'http://127.0.0.1:9/', ...over
})
const tick = () => new Promise(resolve => setTimeout(resolve))

afterEach(() => {
  setObjectTicketSource(null)
  vi.unstubAllGlobals()
})

describe('objectUrl', () => {
  it('is empty until a ticket has arrived, then names the route on the client', async () => {
    setObjectTicketSource(async () => ticket())
    expect(objectUrl({ sha256 })).toBe('')
    await tick()
    expect(objectUrl({ sha256 })).toBe(`http://127.0.0.1:9/objects/${sha256}?ticket=exp.sig`)
    expect(objectUrl(null)).toBe('')
  })

  it('is empty without a server', async () => {
    setObjectTicketSource(async () => ticket({ available: false, ticket: '' }))
    await tick()
    expect(objectUrl({ sha256 })).toBe('')
  })

  it('asks for a new ticket before the old one ends', async () => {
    const source = vi.fn(async () => ticket())
    source.mockResolvedValueOnce(ticket({ ticket: 'old', expiresAt: new Date(Date.now() + 60_000).toISOString() }))
    setObjectTicketSource(source)
    await tick()
    expect(objectUrl({ sha256 })).toContain('ticket=old')
    await tick()
    expect(source).toHaveBeenCalledTimes(2)
    expect(objectUrl({ sha256 })).toContain('ticket=exp.sig')
  })

  it('fetches bytes, and says when they are not there', async () => {
    setObjectTicketSource(async () => ticket())
    vi.stubGlobal('fetch', vi.fn(async () => new Response(new Uint8Array([1, 2, 3]))))
    expect([...await fetchObject({ sha256 })]).toEqual([1, 2, 3])
    vi.stubGlobal('fetch', vi.fn(async () => new Response(null, { status: 404 })))
    await expect(fetchObject({ sha256 })).rejects.toThrow(/not reached/)
  })
})

describe('a reference in a tool result', () => {
  const ref = create(ObjectRefSchema, { sha256, size: 10n, mediaType: 'image/png' })
  const blocks = [
    create(ContentBlockSchema, { block: { case: 'text', value: { text: 'Screenshot' } } }),
    create(ContentBlockSchema, { block: { case: 'text', value: { text: `[image/png stored as object ${sha256}]`, object: ref } } }),
    create(ContentBlockSchema, { block: { case: 'image', value: { mediaType: 'image/png', data: new Uint8Array([1]) } } })
  ]

  it('becomes an image with the reference, and leaves the output', () => {
    const images = toolImages(blocks)
    expect(images).toHaveLength(2)
    expect(images[0]).toEqual({ url: '', object: { sha256, size: 10, mediaType: 'image/png', name: '' } })
    expect(images[1]!.url).toMatch(/^data:image\/png;base64,/)
    expect(textOf(blocks)).toBe('Screenshot')
  })
})
