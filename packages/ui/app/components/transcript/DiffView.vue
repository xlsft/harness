<script lang="ts">
</script>

<script setup lang="ts">
// Edit diff (Diff.dc.html): unified or split, with the Unified / Split toggle in the header.
// `flush` drops the outer frame (diff pane sections); `commentable` lets the user attach review comments
// to lines of the unified view (v-model:comments, shared by every diff of the pane).
import type { PatchHunk } from '~/utils/tools'

export interface DiffComment {
  id: string
  path: string
  // Line in the new file (old file for removed lines), and which side it is on.
  line: number
  side: 'new' | 'old'
  // Index of the hunk in this diff: line numbers repeat when several edits of one file are shown together.
  hunk: number
  code: string
  text: string
  // Posted to the agent (the diff pane sends each comment as soon as it is saved).
  sent?: boolean
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
}>(), { verb: 'Edit', mode: 'unified', icon: true, flush: false, bare: false, commentable: false })

const comments = defineModel<DiffComment[]>('comments', { default: () => [] })

const view = ref(props.mode)
watch(() => props.mode, m => view.value = m)
const stats = computed(() => patchStats(props.hunks))

const ADD = { bg: '#00bc7d1f', sc: '#00bc7d', tc: '#fff', s: '+' }
const DEL = { bg: '#ff20561f', sc: '#ff2056', tc: '#fff', s: '−' }
const CTX = { bg: 'transparent', sc: '#8f8f8f', tc: '#d4d4d4', s: ' ' }

interface URow { kind: 'hunk', text: string }
interface LRow { kind: 'line', h: number, a: string, b: string, t: string, bg: string, sc: string, tc: string, s: string }

// Line comments: the row being commented on (draft) and the comments of this file by row key.
const draft = ref<{ key: string, text: string, id?: string } | null>(null)
const rowKey = (r: LRow) => `${r.h}:${r.b ? `new:${r.b}` : `old:${r.a}`}`
const commentsAt = (r: LRow) => comments.value.filter(c => c.path === props.path && `${c.hunk}:${c.side}:${c.line}` === rowKey(r))

const root = ref<HTMLElement | null>(null)

function startComment(r: LRow, c?: DiffComment) {
  draft.value = { key: rowKey(r), text: c?.text ?? '', id: c?.id }
  // autofocus does not fire on a field inserted later.
  nextTick(() => root.value?.querySelector<HTMLTextAreaElement>('textarea[data-draft]')?.focus())
}

function saveComment(r: LRow) {
  const d = draft.value
  if (!d) return
  const text = d.text.trim()
  if (d.id) {
    comments.value = text
      ? comments.value.map(c => c.id === d.id ? { ...c, text } : c)
      : comments.value.filter(c => c.id !== d.id)
  } else if (text) {
    const side = r.b ? 'new' : 'old'
    comments.value = [...comments.value, { id: `${props.path}:${rowKey(r)}:${Date.now()}`, path: props.path, line: Number(side === 'new' ? r.b : r.a), side, hunk: r.h, code: r.t, text }]
  }
  draft.value = null
}

function onDraftKey(e: KeyboardEvent, r: LRow) {
  if (e.key === 'Escape') draft.value = null
  else if (e.key === 'Enter' && (e.metaKey || e.ctrlKey)) {
    e.preventDefault()
    saveComment(r)
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
const EMPTY: Side = { n: '', t: '', bg: '#131313', c: '#d4d4d4' }

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
      if (line.startsWith('-')) dels.push({ n: String(a++), t, bg: '#ff20561f', c: '#fff' })
      else if (line.startsWith('+')) adds.push({ n: String(b++), t, bg: '#00bc7d1f', c: '#fff' })
      else {
        flush()
        rows.push({ l: { n: String(a++), t, bg: 'transparent', c: '#d4d4d4' }, r: { n: String(b++), t, bg: 'transparent', c: '#d4d4d4' } })
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
      <span class="text-muted">{{ verb }}</span><span class="min-w-0 truncate text-white">{{ path }}</span>
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
          :class="view === 'unified' ? 'bg-raised text-white' : 'text-muted'"
          @click="view = 'unified'"
        >
          Unified
        </button>
        <button
          type="button"
          class="px-[7px] py-px"
          :class="view === 'split' ? 'bg-raised text-white' : 'text-muted'"
          @click="view = 'split'"
        >
          Split
        </button>
      </div>
    </div>
    <div
      v-if="view === 'unified'"
      class="overflow-x-auto text-[12px] leading-[1.7]"
    >
      <template
        v-for="(r, i) in unified"
        :key="i"
      >
        <div
          v-if="r.kind === 'hunk'"
          class="bg-[#071a20] px-3 py-0.5 text-cyan"
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
              class="absolute top-1/2 left-0.5 hidden size-4 -translate-y-1/2 items-center justify-center bg-accent text-white group-hover/line:flex"
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
              class="group/comment mx-2 my-1 flex items-start gap-2 border border-l-2 border-default border-l-accent bg-raised px-2.5 py-1.5 whitespace-pre-wrap text-toned"
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
                  class="text-[11px] text-muted hover:text-white"
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
            <div
              v-if="draft?.key === rowKey(r)"
              class="mx-2 my-1 flex flex-col gap-1.5 border border-accent bg-raised p-2"
            >
              <textarea
                v-model="draft.text"
                rows="3"
                placeholder="What should change here?"
                aria-label="Comment"
                data-draft
                class="w-full resize-y bg-transparent text-[12px] leading-[1.6] whitespace-pre-wrap text-white outline-none placeholder:text-muted"
                @keydown="onDraftKey($event, r)"
              />
              <div class="flex items-center gap-2 text-[11.5px]">
                <span class="inline-flex items-center gap-1 text-muted"><HKeys keys="meta+enter" />save<HKeys keys="escape" />cancel</span>
                <span class="flex-1" />
                <button
                  type="button"
                  class="border border-default px-2 py-0.5 text-toned hover:text-white"
                  @click="draft = null"
                >
                  Cancel
                </button>
                <button
                  type="button"
                  class="bg-accent px-2 py-0.5 text-white disabled:opacity-50"
                  :disabled="!draft.text.trim() && !draft.id"
                  @click="saveComment(r)"
                >
                  {{ draft.id ? 'Save' : 'Comment' }}
                </button>
              </div>
            </div>
          </template>
        </template>
      </template>
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
