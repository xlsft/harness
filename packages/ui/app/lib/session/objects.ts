// The server's object store, as frontends read it. An <img> or a <video> cannot send the agent token, and frontends hold
// no server credentials, so the client signs read tickets (AgentService.CreateObjectTicket) and serves the bytes itself:
// GET <agent url>/objects/<sha256>?ticket=... The answer is safe to point a tag at: raster images and PDFs render, a
// <video> plays, anything else comes back as opaque bytes.

export interface ObjectRef {
  sha256: string
  size: number
  mediaType: string
  name?: string
}

export interface ObjectTicket {
  available: boolean
  ticket: string
  // RFC 3339.
  expiresAt: string
  baseUrl: string
}

export type ObjectTicketSource = () => Promise<ObjectTicket>

// A ticket is asked for again this long before it ends.
const RENEW_MS = 60 * 60_000

// Where the current ticket is kept: a shallowRef from the store, which is what makes objectUrl reactive.
let ticket: { value: ObjectTicket | null } = { value: null }
let source: ObjectTicketSource | null = null
let pending = false

function renew() {
  if (!source || pending) return
  const cell = ticket
  pending = true
  source().then((t) => {
    cell.value = t
  }).catch(() => {
    // No client right now: the next objectUrl asks again.
  }).finally(() => {
    // An answer for a transport that has been replaced says nothing about the new one.
    if (cell === ticket) pending = false
  })
}

/** Set by the session store with its transport. */
export function setObjectTicketSource(s: ObjectTicketSource | null, cell: { value: ObjectTicket | null } = { value: null }) {
  source = s
  ticket = cell
  pending = false
  renew()
}

/**
 * The URL of an object for a tag or a fetch, for anything that holds a reference: `ToolImage.object`, a sent file, an
 * avatar. '' until the first ticket has arrived, and when the client has no server. Reactive: a template using it
 * re-renders when the ticket comes or is renewed.
 */
export function objectUrl(ref: Pick<ObjectRef, 'sha256'> | null | undefined): string {
  const t = ticket.value
  if (!t || (t.available && Date.parse(t.expiresAt) - Date.now() < RENEW_MS)) renew()
  if (!t?.available || !ref?.sha256) return ''
  return `${t.baseUrl.replace(/\/+$/, '')}/objects/${ref.sha256}?ticket=${encodeURIComponent(t.ticket)}`
}

/** The bytes of an object; throws when they are not there (not uploaded yet, or no server). */
export async function fetchObject(ref: Pick<ObjectRef, 'sha256'>): Promise<Uint8Array<ArrayBuffer>> {
  if (!ticket.value && source) {
    const t = await source()
    ticket.value = t
  }
  const url = objectUrl(ref)
  if (!url) throw new Error('The object store is not available')
  const res = await fetch(url)
  if (!res.ok) throw new Error(res.status === 404 ? 'The file has not reached the server yet' : `The object store answered ${res.status}`)
  return new Uint8Array(await res.arrayBuffer())
}

// Where an image of a tool result can be shown from: its data URL, or the object store once the ticket is there.
export function imageSrc(img: { url: string, object?: ObjectRef }): string {
  return img.object ? objectUrl(img.object) : img.url
}
