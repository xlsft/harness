// Delivery state of the user's messages and the actions under each one (copy, rewind, fork, cancel, send now).
//
// A message the user typed goes through:
// - queued: waiting in this browser's local queue (typed while a turn runs) or in the client's queue
//   (QueueChanged: sent while a question waits, Send now while a turn runs, or behind other messages);
// - sending: the SendUserMessage call is in flight;
// - sent (one check): the client accepted it and it waits in no queue, the agent has not taken it yet;
// - read (two checks): the agent took it — its turn started with it or it was injected into the running turn.
//   A UserMessage event carries the frontend ids it took (client_uuids), and every user message of the
//   transcript is read, also after a reload.

import type { SessionState, UserItem } from './types'

export type Delivery = 'queued' | 'sending' | 'sent' | 'read'

// A message this browser handed to the client (SendUserMessage), until the stream shows it queued or read.
export interface OutboxEntry {
  id: string
  text: string
  at: number
  state: 'sending' | 'sent'
}

export interface LocalMessage {
  id: string
  text: string
  at?: number
}

// One user message bubble that is not in the transcript yet.
export interface PendingMessage {
  // Frontend message id (SendUserMessageRequest.uuid).
  id: string
  text: string
  delivery: Exclude<Delivery, 'read'>
  // Where it waits: the browser's queue, the client's queue, or handed over (sending / sent).
  where: 'local' | 'client' | 'outbox'
  at: number
  // 1-based place among the waiting (queued) messages, 0 when not queued.
  position: number
}

// The messages after the transcript, oldest first: the ones handed over and not queued (sending, sent), then the
// client's queue, then the browser's queue (it is sent after the client's).
export function pendingMessages(s: SessionState, local: LocalMessage[] = [], outbox: OutboxEntry[] = []): PendingMessage[] {
  const inClient = new Set(s.queue.map(q => q.id))
  const handed = outbox.filter(o => !s.delivered[o.id] && !inClient.has(o.id))
  const sentAt = new Map(outbox.map(o => [o.id, o.at]))
  const waiting = [
    ...s.queue.filter(q => !s.delivered[q.id]).map(q => ({ id: q.id, text: q.text, where: 'client' as const, at: q.at || sentAt.get(q.id) || 0 })),
    ...local.map(q => ({ id: q.id, text: q.text, where: 'local' as const, at: q.at ?? 0 }))
  ]
  return [
    ...handed.map(o => ({ id: o.id, text: o.text, delivery: o.state, where: 'outbox' as const, at: o.at, position: 0 })),
    ...waiting.map((q, i) => ({ ...q, delivery: 'queued' as const, position: i + 1 }))
  ]
}

// Outbox entries still worth keeping: not read, and not given up (a question answered with the queued messages
// takes them without a UserMessage, see pruneOutbox in the store).
export function liveOutbox(s: SessionState, outbox: OutboxEntry[]): OutboxEntry[] {
  return outbox.filter(o => !s.delivered[o.id])
}

// Whether a transcript user message gets the actions row (not the "Stopped" marker of an interrupt).
export function hasActions(item: UserItem): boolean {
  return !/^\[Request interrupted by user[^\]]*\]$/.test(item.text.trim())
}

export type ActionState = 'enabled' | 'disabled' | 'hidden'

export interface MessageActions {
  copy: ActionState
  rewind: ActionState
  fork: ActionState
  cancel: ActionState
  sendNow: ActionState
  // Why Rewind is disabled (its tooltip), empty when it is not.
  rewindHint: string
}

// Which actions a message shows:
// - Copy: always;
// - Rewind: on read messages of a session driven from here, disabled while a turn runs (the client rewinds only
//   an idle session) and on messages not sent yet;
// - Fork: on read and queued messages (a queued one forks the whole conversation and puts its text in the
//   fork's composer), disabled while it is being handed over;
// - Cancel and Send now: only on queued messages.
// A read-only session (another machine's) offers Copy only.
export function messageActions(delivery: Delivery, ctx: { busy: boolean, readOnly: boolean }): MessageActions {
  const queued = delivery === 'queued'
  const read = delivery === 'read'
  if (ctx.readOnly) return { copy: 'enabled', rewind: 'hidden', fork: 'hidden', cancel: 'hidden', sendNow: 'hidden', rewindHint: '' }
  const rewindHint = !read ? 'Not sent yet' : ctx.busy ? 'Stop the turn to rewind' : ''
  return {
    copy: 'enabled',
    rewind: rewindHint ? 'disabled' : 'enabled',
    fork: read || queued ? 'enabled' : 'disabled',
    cancel: queued ? 'enabled' : 'hidden',
    sendNow: queued ? 'enabled' : 'hidden',
    rewindHint
  }
}
