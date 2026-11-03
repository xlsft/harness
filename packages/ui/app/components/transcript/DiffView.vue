<script lang="ts">
</script>

<script setup lang="ts">
// Edit diff (Diff.dc.html): unified or split, with the Unified / Split toggle in the header.
// `flush` drops the outer frame (diff pane sections); `commentable` lets the user attach review comments
// to lines of the unified view (v-model:comments, shared by every diff of the pane).
import type { PatchHunk } from '~/utils/tools'
import { locateAnchor, type LineAnchor } from '~/utils/diffAnchor'

export interface DiffComment {
  id: string
  path: string
  // Line in the new file (old file for removed lines), and which side it is on.
  line: number
  side: 'new' | 'old'
  // Index of the hunk in this diff: line numbers repeat when several edits of one file are shown together.
  hunk: number
  // The line's diff sign: '+' added, '-' removed, ' ' context.
  sign: '+' | '-' | ' '
  code: string
  text: string
  // Posted to the agent (the diff pane sends each comment as soon as it is saved).
  sent?: boolean
  // The conversation on this line: what the user wrote and the exact message that carried it.
  thread?: { text: string, message: string }[]
}

export interface ThreadEntry {
  text: string
  // The agent's answer to that message; null while it has not been delivered yet (queued).
  reply: { text: string, streaming: boolean } | null
}

const props = withDefaults(defineProps<{
  path: string
  hunks: PatchHunk[]
  verb?: string
  mode?: 'unified' | 'split'
  icon?: boolean
  flush?: boolean
  // No header (the diff pane draws one continuous diff with its own file separators and view menu).
  bare?: boolean
  commentable?: boolean
  // Thread of a sent comment, with the agent's replies (the diff pane reads them from the transcript).
  threadOf?: (c: DiffComment) => ThreadEntry[]
}>(), { verb: 'Edit', mode: 'unified', icon: true, flush: false, bare: false, commentable: false })

const comments = defineModel<DiffComment[]>('comments', { default: () => [] })
const emit = defineEmits<{ reply: [comment: DiffComment, text: string] }>()

// Reply boxes under sent comments, by comment id.
const replyText = reactive<Record<string, string>>({})
function sendReply(c: DiffComment) {
  const text = (replyText[c.id] ?? '').trim()
  if (!text) return
  emit('reply', c, text)
  replyText[c.id] = ''
}
function onReplyKey(e: KeyboardEvent, c: DiffComment) {
  if (e.key === 'Enter' && !e.shiftKey && !e.isComposing) {
    e.preventDefault()
    sendReply(c)
  }
}

const view = ref(props.mode)
watch(() => props.mode, m => view.value = m)
const stats = computed(() => patchStats(props.hunks))

const ADD = { bg: 'color-mix(in srgb, var(--color-ok) 12%, transparent)', sc: 'var(--color-ok)', tc: 'var(--color-strong)', s: '+' }
const DEL = { bg: 'color-mix(in srgb, var(--color-bad) 12%, transparent)', sc: 'var(--color-bad)', tc: 'var(--color-strong)', s: '−' }
const CTX = { bg: 'transparent', sc: 'var(--color-muted)', tc: 'var(--color-text)', s: ' ' }

interface URow { kind: 'hunk', text: string }
interface LRow { kind: 'line', h: number, a: string, b: string, t: string, bg: string, sc: string, tc: string, s: string }

// Line comments. A comment or draft is anchored to its line's side, number and text, not to a row: when the
// diff changes under it (the agent edits the file again) it follows its text to the nearest matching line,
// and a draft whose line is gone stays open above the diff instead of being lost.
interface Anchor { side: 'new' | 'old', line: number, code: string, sign: DiffComment['sign'] }
const draft = ref<{ anchor: Anchor, text: string, id?: string } | null>(null)
const rowKey = (r: LRow) => `${r.h}:${r.b ? `new:${r.b}` : `old:${r.a}`}`
const anchorOf = (r: LRow): Anchor => {
  const side = r.b ? 'new' : 'old'
  return { side, line: Number(side === 'new' ? r.b : r.a), code: r.t, sign: r.s === '+' ? '+' : r.s === ' ' ? ' ' : '-' }
}
const lineRows = computed(() => unified.value.filter((r): r is LRow => r.kind === 'line'))
const lineAnchors = computed(() => lineRows.value.map(anchorOf))
// The row an anchor sits on now (see locateAnchor), as a row key.
function rowFor(a: LineAnchor): string | null {
  const i = locateAnchor(lineAnchors.value, a)
  return i < 0 ? null : rowKey(lineRows.value[i]!)
}
const draftRow = computed(() => draft.value && rowFor(draft.value.anchor))
const commentsAt = (r: LRow) => comments.value.filter(c => c.path === props.path && rowFor(c) === rowKey(r))

const root = ref<HTMLElement | null>(null)

function startComment(r: LRow, c?: DiffComment) {
  // A second click on the same line closes an empty box.
  if (!c && draftRow.value === rowKey(r) && !draft.value?.text.trim()) {
    draft.value = null
    return
  }
  draft.value = { anchor: c ? { side: c.side, line: c.line, code: c.code, sign: c.sign } : anchorOf(r), text: c?.text ?? '', id: c?.id }
  // autofocus does not fire on a field inserted later.
  nextTick(() => root.value?.querySelector<HTMLTextAreaElement>('textarea[data-draft]')?.focus())
}

// Saves the draft where its anchor sits now (or where it was, when that line is gone from the diff).
function saveComment() {
  const d = draft.value
  if (!d) return
  const text = d.text.trim()
  if (d.id) {
    comments.value = text
      ? comments.value.map(c => c.id === d.id ? { ...c, text } : c)
      : comments.value.filter(c => c.id !== d.id)
  } else if (text) {
    const row = lineRows.value.find(r => rowKey(r) === draftRow.value)
    const a = row ? anchorOf(row) : d.anchor
    comments.value = [...comments.value, { id: `${props.path}:${a.side}:${a.line}:${Date.now()}`, path: props.path, line: a.line, side: a.side, hunk: row?.h ?? 0, sign: a.sign, code: a.code, text }]
  }
  draft.value = null
}

function onDraftKey(e: KeyboardEvent) {
  if (e.key === 'Escape') draft.value = null
  else if (e.key === 'Enter' && (e.metaKey || e.ctrlKey)) {
    e.preventDefault()
    saveComment()
  }
}

const unified = computed(() => {
  const rows: (URow | LRow)[] = []
  for (const [hi, h] of props.hunks.entries()) {
    rows.push({ kind: 'hunk', text: `@@ -${h.oldStart},${h.oldLines} +${h.newStart},${h.newLines} @@${h.header ? ' ' + h.header : ''}` })
    let a = h.oldStart
    let b = h.newStart
    for (const l of h.lines) {
      const t = l.slice(1)
      if (l.startsWith('+')) rows.push({ kind: 'line', h: hi, a: '', b: String(b++), t, ...ADD })
      else if (l.startsWith('-')) rows.push({ kind: 'line', h: hi, a: String(a++), b: '', t, ...DEL })
      else rows.push({ kind: 'line', h: hi, a: String(a++), b: String(b++), t, ...CTX })
    }
  }
  return rows
})

interface Side { n: string, t: string, bg: string, c: string }
const EMPTY: Side = { n: '', t: '', bg: 'var(--color-hover)', c: 'var(--color-text)' }

const split = computed(() => {
  const rows: { l: Side, r: Side }[] = []
  for (const h of props.hunks) {
    let a = h.oldStart
    let b = h.newStart
    let dels: Side[] = []
    let adds: Side[] = []
    const flush = () => {
      for (let i = 0; i < Math.max(dels.length, adds.length); i++) rows.push({ l: dels[i] ?? EMPTY, r: adds[i] ?? EMPTY })
      dels = []
      adds = []
    }
    for (const line of h.lines) {
      const t = line.slice(1)
      if (line.startsWith('-')) dels.push({ n: String(a++), t, bg: 'color-mix(in srgb, var(--color-bad) 12%, transparent)', c: 'var(--color-strong)' })
      else if (line.startsWith('+')) adds.push({ n: String(b++), t, bg: 'color-mix(in srgb, var(--color-ok) 12%, transparent)', c: 'var(--color-strong)' })
      else {
        flush()
        rows.push({ l: { n: String(a++), t, bg: 'transparent', c: 'var(--color-text)' }, r: { n: String(b++), t, bg: 'transparent', c: 'var(--color-text)' } })
      }
    }
    flush()
  }
  return rows
})
</script>

<template>
  <div
    ref="root"
    class="bg-surface"
    :class="bare ? '' : flush ? 'border-b border-default' : 'border border-default'"
  >
    <div
      v-if="!bare"
      class="flex flex-wrap items-center gap-[9px] border-b border-default py-[7px] pr-2 pl-3 text-[12px]"
    >
      <HIcon
        v-if="icon"
        name="edit"
        class="text-muted"
      />
      <span class="text-muted">{{ verb }}</span><span class="min-w-0 truncate text-strong">{{ path }}</span>
      <span class="flex-1" />
      <span class="text-ok">+{{ stats.add }}</span><span class="text-bad">−{{ stats.del }}</span>
      <div
        class="flex border border-default text-[11px]"
        role="group"
        aria-label="Diff view"
      >
        <button
          type="button"
          class="px-[7px] py-px"
          :class="view === 'unified' ? 'bg-raised text-strong' : 'text-muted'"
          @click="view = 'unified'"
        >
          Unified
        </button>
        <button
          type="button"
          class="px-[7px] py-px"
          :class="view === 'split' ? 'bg-raised text-strong' : 'text-muted'"
          @click="view = 'split'"
        >
          Split
        </button>
      </div>
    </div>
    <div
      v-if="view === 'unified'"
      class="@container overflow-x-auto text-[12px] leading-[1.7]"
    >
      <!-- A draft whose line left the diff (the file changed under it): kept open here with its text. -->
      <div
        v-if="commentable && draft && !draftRow"
        class="sticky left-0 flex w-[100cqw] flex-col gap-1.5 border border-accent bg-raised px-3 py-2"
      >
        <span class="text-[11px] text-muted">Line {{ draft.anchor.line }} changed: <span class="text-toned">{{ draft.anchor.code.trim() }}</span></span>
        <textarea
          v-model="draft.text"
          rows="3"
          placeholder="What should change here?"
          aria-label="Comment"
          data-draft
          class="w-full resize-y bg-transparent text-[12px] leading-[1.6] whitespace-pre-wrap text-strong outline-none placeholder:text-muted"
          @keydown="onDraftKey"
        />
        <div class="flex items-center gap-2 text-[11.5px]">
          <span class="flex-1" />
          <button
            type="button"
            class="border border-default px-2 py-0.5 text-toned hover:text-strong"
            @click="draft = null"
          >
            Cancel
          </button>
          <button
            type="button"
            class="bg-accent px-2 py-0.5 text-on-accent disabled:opacity-50"
            :disabled="!draft.text.trim() && !draft.id"
            @click="saveComment"
          >
            {{ draft.id ? 'Save' : 'Comment' }}
          </button>
        </div>
      </div>
      <!-- Rows are as wide as the longest line, so backgrounds span the whole scroll width. -->
      <div class="w-max min-w-full">
        <template
          v-for="(r, i) in unified"
          :key="i"
        >
          <div
            v-if="r.kind === 'hunk'"
            class="bg-cyan/10 px-3 py-0.5 text-cyan"
          >
            {{ r.text }}
          </div>
          <template v-else>
            <div
              class="group/line relative flex whitespace-pre"
              :style="{ background: r.bg }"
            >
              <button
                v-if="commentable"
                type="button"
                :aria-label="`Comment on line ${r.b || r.a}`"
                class="absolute top-1/2 left-0.5 hidden size-4 -translate-y-1/2 items-center justify-center bg-accent text-on-accent group-hover/line:flex"
                @click="startComment(r)"
              >
                <HIcon
                  name="plus"
                  :size="11"
                />
              </button>
              <span class="w-[38px] shrink-0 pr-1.5 text-right text-dimmed">{{ r.a }}</span>
              <span class="w-[38px] shrink-0 pr-2.5 text-right text-dimmed">{{ r.b }}</span>
              <span
                class="w-3.5 shrink-0"
                :style="{ color: r.sc }"
              >{{ r.s }}</span>
              <span :style="{ color: r.tc }">{{ r.t }}</span>
            </div>
            <template v-if="commentable">
              <div
                v-for="c in commentsAt(r)"
                v-show="draft?.id !== c.id"
                :key="c.id"
                class="sticky left-0 flex w-[100cqw] flex-col gap-1.5 border-y border-l-2 border-default border-l-accent bg-raised px-3 py-1.5 whitespace-pre-wrap text-toned"
              >
                <template v-if="c.sent && threadOf">
                  <template
                    v-for="(e, ei) in threadOf(c)"
                    :key="ei"
                  >
                    <div class="flex items-start gap-2">
                      <span class="w-10 shrink-0 text-[11px] text-muted">you</span>
                      <span class="min-w-0 flex-1 text-strong">{{ e.text }}</span>
                    </div>
                    <div class="flex items-start gap-2">
                      <span class="w-10 shrink-0 text-[11px] text-accent">agent</span>
                      <span
                        v-if="e.reply?.text"
                        class="max-h-60 min-w-0 flex-1 overflow-y-auto"
                      >{{ e.reply.text }}</span>
                      <span
                        v-else
                        class="inline-flex items-center gap-1.5 text-muted"
                      ><HIcon
                        name="loading"
                        :size="12"
                        spin
                      />{{ e.reply ? 'working' : 'queued' }}</span>
                    </div>
                  </template>
                  <textarea
                    v-model="replyText[c.id]"
                    rows="1"
                    placeholder="Reply…"
                    aria-label="Reply"
                    class="ml-12 resize-none border border-default bg-surface px-2 py-1 text-[12px] leading-[1.6] text-strong outline-none placeholder:text-muted focus:border-accent"
                    @keydown="onReplyKey($event, c)"
                  />
                </template>
                <div
                  v-else
                  class="flex items-start gap-2"
                >
                  <span class="min-w-0 flex-1">{{ c.text }}</span>
                  <span
                    v-if="c.sent"
                    class="inline-flex shrink-0 items-center gap-1 text-[11px] text-muted"
                  ><HIcon
                    name="check"
                    :size="11"
                  />sent</span>
                  <template v-else>
                    <button
                      type="button"
                      class="text-[11px] text-muted hover:text-strong"
                      @click="startComment(r, c)"
                    >
                      Edit
                    </button>
                    <button
                      type="button"
                      class="text-[11px] text-muted hover:text-bad"
                      @click="comments = comments.filter(x => x.id !== c.id)"
                    >
                      Delete
                    </button>
                  </template>
                </div>
              </div>
              <div
                v-if="draft && draftRow === rowKey(r)"
                class="sticky left-0 flex w-[100cqw] flex-col gap-1.5 border border-accent bg-raised px-3 py-2"
              >
                <textarea
                  v-model="draft.text"
                  rows="3"
                  placeholder="What should change here?"
                  aria-label="Comment"
                  data-draft
                  class="w-full resize-y bg-transparent text-[12px] leading-[1.6] whitespace-pre-wrap text-strong outline-none placeholder:text-muted"
                  @keydown="onDraftKey"
                />
                <div class="flex items-center gap-2 text-[11.5px]">
                  <span class="inline-flex items-center gap-1 text-muted"><HKeys keys="meta+enter" />save<HKeys keys="escape" />cancel</span>
                  <span class="flex-1" />
                  <button
                    type="button"
                    class="border border-default px-2 py-0.5 text-toned hover:text-strong"
                    @click="draft = null"
                  >
                    Cancel
                  </button>
                  <button
                    type="button"
                    class="bg-accent px-2 py-0.5 text-on-accent disabled:opacity-50"
                    :disabled="!draft.text.trim() && !draft.id"
                    @click="saveComment"
                  >
                    {{ draft.id ? 'Save' : 'Comment' }}
                  </button>
                </div>
              </div>
            </template>
          </template>
        </template>
      </div>
    </div>
    <div
      v-else
      class="overflow-x-auto text-[12px] leading-[1.7]"
    >
      <div
        v-if="commentable && comments.some(c => c.path === path)"
        class="border-b border-default px-3 py-1 text-[11.5px] text-muted"
      >
        Comments on this file show in the Unified view.
      </div>
      <div
        v-for="(r, i) in split"
        :key="i"
        class="grid grid-cols-2 whitespace-pre"
      >
        <div
          class="flex overflow-hidden border-r border-default"
          :style="{ background: r.l.bg }"
        >
          <span class="w-9 shrink-0 pr-2.5 text-right text-dimmed">{{ r.l.n }}</span><span :style="{ color: r.l.c }">{{ r.l.t }}</span>
        </div>
        <div
          class="flex overflow-hidden"
          :style="{ background: r.r.bg }"
        >
          <span class="w-9 shrink-0 pr-2.5 text-right text-dimmed">{{ r.r.n }}</span><span :style="{ color: r.r.c }">{{ r.r.t }}</span>
        </div>
      </div>
    </div>
  </div>
</template>
