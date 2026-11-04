// Files attached to a user message, from the composer to SendUserMessage's content blocks and back to what a message
// shows. Images and PDFs go as what they are; anything else that reads as UTF-8 goes as text under the typed message,
// since the first text block titles a new session.
import { create } from '@bufbuild/protobuf'
import { ContentBlockSchema, DocumentBlockSchema, ImageBlockSchema, TextBlockSchema, type ContentBlock } from '~/gen/harness/llm/v1/content_pb'
import type { Attachment } from './types'

export const MAX_ATTACHMENT_BYTES = 50 * 1024 * 1024
// A text file goes into the context whole.
export const MAX_TEXT_ATTACHMENT_BYTES = 1024 * 1024

// What ImageBlock takes (harness.llm.v1).
const IMAGE_TYPES = ['image/png', 'image/jpeg', 'image/gif', 'image/webp']
const BINARY_TYPES = /^(image|video|audio|font)\/|zip|tar|compressed|rar|7z|msdownload|octet-stream/
const UNSENDABLE = 'only images, PDFs and text files can be sent'
const UNSENDABLE_IMAGE = 'only PNG, JPEG, GIF and WebP images can be sent'
// A webview may hand over a file without a type; these are the ones that travel by what they are.
const TYPE_OF_EXTENSION: Record<string, string> = { png: 'image/png', jpg: 'image/jpeg', jpeg: 'image/jpeg', gif: 'image/gif', webp: 'image/webp', pdf: 'application/pdf' }

export type AttachmentKind = 'image' | 'document' | 'text'

export interface OutgoingAttachment {
  name: string
  kind: AttachmentKind
  mediaType: string
  data: Uint8Array
}

/** The lower-case extension of a file name, '' without one. */
export function fileExtension(name: string): string {
  return name.match(/\.([^./\\]+)$/)?.[1]?.toLowerCase() ?? ''
}

/** The file's media type, from its name when it came without one. */
export function attachmentMediaType(f: { name: string, type: string }): string {
  if (f.type === 'image/jpg') return 'image/jpeg'
  return f.type || TYPE_OF_EXTENSION[fileExtension(f.name)] || ''
}

/** How a file travels in a message; null when it cannot. Text is only a guess until the bytes are read. */
export function attachmentKind(f: { name: string, type: string }): AttachmentKind | null {
  const type = attachmentMediaType(f)
  if (IMAGE_TYPES.includes(type)) return 'image'
  if (type === 'application/pdf' || /\.pdf$/i.test(f.name)) return 'document'
  return BINARY_TYPES.test(type) ? null : 'text'
}

/** Why a file cannot be attached, shown with it; undefined when it can. */
export function attachmentError(f: { name: string, type: string, size: number }): string | undefined {
  const mb = Math.round(f.size / 1024 / 1024)
  if (f.size > MAX_ATTACHMENT_BYTES) return `too large · ${mb} MB`
  const kind = attachmentKind(f)
  if (!kind) return f.type.startsWith('image/') ? UNSENDABLE_IMAGE : UNSENDABLE
  if (kind === 'text' && f.size > MAX_TEXT_ATTACHMENT_BYTES) return `too large for a text file · ${mb} MB`
  return undefined
}

/** The image format the bytes are, whatever the name says: a provider refuses a block whose type does not match. */
export function sniffImageType(d: Uint8Array): string | undefined {
  if (d[0] === 0x89 && d[1] === 0x50 && d[2] === 0x4E && d[3] === 0x47) return 'image/png'
  if (d[0] === 0xFF && d[1] === 0xD8 && d[2] === 0xFF) return 'image/jpeg'
  if (d[0] === 0x47 && d[1] === 0x49 && d[2] === 0x46) return 'image/gif'
  if (d[0] === 0x52 && d[1] === 0x49 && d[2] === 0x46 && d[3] === 0x46 && d[8] === 0x57 && d[9] === 0x45 && d[10] === 0x42 && d[11] === 0x50) return 'image/webp'
  return undefined
}

/** The bytes of what is attached; throws, naming the file, when one cannot be sent. */
export async function readAttachments(files: { file?: File }[]): Promise<OutgoingAttachment[]> {
  const out: OutgoingAttachment[] = []
  for (const { file } of files) {
    if (!file) continue
    const kind = attachmentKind(file)
    const data = new Uint8Array(await file.arrayBuffer())
    if (!kind || (kind === 'text' && !isUtf8(data))) throw new Error(`${file.name}: ${UNSENDABLE}.`)
    const mediaType = attachmentMediaType(file)
    out.push({ name: file.name, kind, mediaType: kind === 'document' ? 'application/pdf' : kind === 'image' ? sniffImageType(data) ?? mediaType : mediaType, data })
  }
  return out
}

function isUtf8(data: Uint8Array) {
  try {
    new TextDecoder('utf-8', { fatal: true }).decode(data)
    return true
  } catch {
    return false
  }
}

/**
 * The files a paste carries; none means the paste stays text. Rich text with a picture of itself (a spreadsheet's cells,
 * a selection on a web page) is text; a file copied in a file manager also has its name as plain text, and is a file.
 */
export function clipboardFiles(c: Pick<DataTransfer, 'types' | 'files' | 'items' | 'getData'>): File[] {
  const plain = c.getData('text/plain')
  if (plain.trim() && (c.types.includes('text/html') || c.types.includes('text/rtf'))) return []
  if (c.files.length) return Array.from(c.files)
  const out: File[] = []
  for (const item of Array.from(c.items)) {
    const f = item.kind === 'file' ? item.getAsFile() : null
    if (f) out.push(f)
  }
  return out
}

// A text file's block: the name is kept in an attribute, so it cannot hold the quote that ends it.
const FILE_BLOCK = /^<file name="([^"]*)">\n([\s\S]*)\n<\/file>$/

/** The content blocks of a message: images and documents, the typed text, then text files. */
export function messageContent(text: string, attachments: OutgoingAttachment[] = []): ContentBlock[] {
  const block = (b: ContentBlock['block']) => create(ContentBlockSchema, { block: b })
  return [
    ...attachments.flatMap((a) => {
      if (a.kind === 'image') return [block({ case: 'image', value: create(ImageBlockSchema, { mediaType: a.mediaType, data: a.data as Uint8Array<ArrayBuffer> }) })]
      if (a.kind === 'document') return [block({ case: 'document', value: create(DocumentBlockSchema, { mediaType: a.mediaType, data: a.data as Uint8Array<ArrayBuffer>, title: a.name }) })]
      return []
    }),
    // A provider refuses an empty text block: a message of files alone has none.
    ...(text.trim() || !attachments.length ? [block({ case: 'text', value: create(TextBlockSchema, { text }) })] : []),
    ...attachments.filter(a => a.kind === 'text').map(a => block({ case: 'text', value: create(TextBlockSchema, { text: `<file name="${a.name.replaceAll('"', '\'')}">\n${new TextDecoder().decode(a.data)}\n</file>` }) }))
  ]
}

/** Bytes as base64. */
export function base64(data: Uint8Array): string {
  let bin = ''
  const step = 0x8000
  for (let i = 0; i < data.length; i += step) bin += String.fromCharCode(...data.subarray(i, i + step))
  return btoa(bin)
}

const dataUrl = (mediaType: string, data: Uint8Array) => `data:${mediaType};base64,${base64(data)}`

/** Whether a text block is an attached text file rather than something typed. */
export function isFileBlock(text: string): boolean {
  return FILE_BLOCK.test(text)
}

/** What a message's blocks attach: inline bytes, a reference to the object store, or a text file. */
export function blockAttachments(blocks: ContentBlock[]): Attachment[] {
  return blocks.flatMap((b): Attachment[] => {
    if (b.block.case === 'image') {
      const { mediaType, data } = b.block.value
      return [{ kind: 'image', name: 'image', mediaType, size: data.length, url: dataUrl(mediaType, data) }]
    }
    if (b.block.case === 'document') {
      const { mediaType, data, title } = b.block.value
      return [{ kind: 'document', name: title || 'document', mediaType, size: data.length, url: dataUrl(mediaType, data) }]
    }
    if (b.block.case !== 'text') return []
    const o = b.block.value.object
    if (o) {
      const object = { sha256: o.sha256, size: Number(o.size), mediaType: o.mediaType, name: o.name }
      const image = o.mediaType.startsWith('image/')
      return [{ kind: image ? 'image' : 'document', name: o.name || (image ? 'image' : 'document'), mediaType: o.mediaType, size: object.size, object }]
    }
    const file = b.block.value.text.match(FILE_BLOCK)
    if (!file) return []
    const bytes = new TextEncoder().encode(file[2]!)
    return [{ kind: 'document', name: file[1]!, mediaType: 'text/plain', size: bytes.length, url: dataUrl('text/plain', bytes) }]
  })
}

/** Attachments that hold their bytes, as files again: a queued message taken back into the composer. */
export async function attachmentFiles(attachments: Attachment[]): Promise<File[]> {
  const held = attachments.filter(a => a.url)
  return Promise.all(held.map(async (a) => {
    const blob = await (await fetch(a.url!)).blob()
    const name = a.kind === 'image' && !fileExtension(a.name) ? `${a.name}.${a.mediaType.split('/').pop()}` : a.name
    return new File([blob], name, { type: a.mediaType })
  }))
}
