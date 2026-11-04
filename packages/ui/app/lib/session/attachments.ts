// Files attached to a user message, from the composer to SendUserMessage's content blocks. Images and PDFs go as what
// they are; anything else that reads as UTF-8 goes as text under the typed message, since the first text block titles
// a new session.
import { create } from '@bufbuild/protobuf'
import { ContentBlockSchema, DocumentBlockSchema, ImageBlockSchema, TextBlockSchema, type ContentBlock } from '~/gen/harness/llm/v1/content_pb'

export const MAX_ATTACHMENT_BYTES = 50 * 1024 * 1024
// A text file goes into the context whole.
export const MAX_TEXT_ATTACHMENT_BYTES = 1024 * 1024

// What ImageBlock takes (harness.llm.v1).
const IMAGE_TYPES = ['image/png', 'image/jpeg', 'image/gif', 'image/webp']
const BINARY_TYPES = /^(image|video|audio|font)\/|zip|tar|compressed|rar|7z|msdownload|octet-stream/
const UNSENDABLE = 'only images, PDFs and text files can be sent'

export type AttachmentKind = 'image' | 'document' | 'text'

export interface OutgoingAttachment {
  name: string
  kind: AttachmentKind
  mediaType: string
  data: Uint8Array
}

/** How a file travels in a message; null when it cannot. Text is only a guess until the bytes are read. */
export function attachmentKind(f: { name: string, type: string }): AttachmentKind | null {
  if (IMAGE_TYPES.includes(f.type)) return 'image'
  if (f.type === 'application/pdf' || /\.pdf$/i.test(f.name)) return 'document'
  return BINARY_TYPES.test(f.type) ? null : 'text'
}

/** Why a file cannot be attached, shown with it; undefined when it can. */
export function attachmentError(f: { name: string, type: string, size: number }): string | undefined {
  const mb = Math.round(f.size / 1024 / 1024)
  if (f.size > MAX_ATTACHMENT_BYTES) return `too large · ${mb} MB`
  const kind = attachmentKind(f)
  if (!kind) return UNSENDABLE
  if (kind === 'text' && f.size > MAX_TEXT_ATTACHMENT_BYTES) return `too large for a text file · ${mb} MB`
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
    out.push({ name: file.name, kind, mediaType: kind === 'document' ? 'application/pdf' : file.type, data })
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

/** The content blocks of a message: images and documents, the typed text, then text files. */
export function messageContent(text: string, attachments: OutgoingAttachment[] = []): ContentBlock[] {
  const block = (b: ContentBlock['block']) => create(ContentBlockSchema, { block: b })
  return [
    ...attachments.flatMap((a) => {
      if (a.kind === 'image') return [block({ case: 'image', value: create(ImageBlockSchema, { mediaType: a.mediaType, data: a.data as Uint8Array<ArrayBuffer> }) })]
      if (a.kind === 'document') return [block({ case: 'document', value: create(DocumentBlockSchema, { mediaType: a.mediaType, data: a.data as Uint8Array<ArrayBuffer>, title: a.name }) })]
      return []
    }),
    block({ case: 'text', value: create(TextBlockSchema, { text }) }),
    ...attachments.filter(a => a.kind === 'text').map(a => block({ case: 'text', value: create(TextBlockSchema, { text: `<file name="${a.name}">\n${new TextDecoder().decode(a.data)}\n</file>` }) }))
  ]
}
