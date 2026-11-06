<script setup lang="ts">
// Search (⌘K): sessions, files of the open session, projects (folders) and actions in one list. Empty, it shows
// what needs attention, recent sessions and actions; typing filters every group, and looks through the transcripts of
// every session (the client's search, on this machine and the server). ←/→ switch the type, ↑/↓ move, Enter opens, Esc closes.
import type { IconName } from '~/utils/icons'
import type { TranscriptHit } from '~/lib/library/types'

const open = useState('search-open', () => false)
const store = useSessionStore()
const settings = useSettingsModal()
const { show } = useShow()

type Kind = 'all' | 'sessions' | 'code' | 'projects'
const KINDS: { key: Kind, label: string }[] = [
  { key: 'all', label: 'All' },
  { key: 'sessions', label: 'Sessions' },
  { key: 'code', label: 'Code' },
  { key: 'projects', label: 'Projects' }
]
const kind = ref<Kind>('all')
const query = ref('')
const field = ref<HTMLInputElement | null>(null)
const active = ref(0)

interface Item { key: string, icon: IconName, label: string, hint?: string, run: () => void }
interface Group { title: string, items: Item[] }

watch(open, (v) => {
  if (!v) return
  query.value = ''
  kind.value = 'all'
  active.value = 0
  nextTick(() => field.value?.focus())
})

function close() {
  open.value = false
}
function openSession(id: string) {
  close()
  store.open(id)
  if (useRoute().path !== '/') navigateTo({ path: '/', query: { session: id } })
}
const base = (p: string) => p.replace(/\/+$/, '').split('/').pop() || p
const matches = (text: string, q: string) => {
  // Subsequence match, so "hrns pln" finds "harness plan".
  let i = 0
  const t = text.toLowerCase()
  for (const c of q.toLowerCase().replace(/\s+/g, '')) {
    i = t.indexOf(c, i)
    if (i < 0) return false
    i++
  }
  return true
}

const sessions = computed(() => store.order.map(id => store.sessions[id]!).filter(Boolean))
const sessionItem = (s: (typeof sessions.value)[number]): Item => ({
  key: `s:${s.meta.id}`,
  icon: s.meta.remote ? 'monitor' : 'chat',
  label: s.meta.title || 'Untitled session',
  hint: s.meta.noFolder ? 'No folder' : base(s.meta.cwd),
  run: () => openSession(s.meta.id)
})
const projects = computed(() => {
  const seen = new Map<string, number>()
  for (const s of sessions.value) if (!s.meta.remote && !s.meta.noFolder) seen.set(s.meta.cwd, (seen.get(s.meta.cwd) ?? 0) + 1)
  return [...seen].map(([cwd, n]) => ({ cwd, n }))
})

// Files of the open session (the client's file suggestions), fetched as you type.
const files = ref<string[]>([])
let seq = 0
watch([query, kind, open], async () => {
  const q = query.value.trim()
  const current = store.current
  if (!open.value || !q || !current || current.meta.remote || (kind.value !== 'all' && kind.value !== 'code') || !store.transport) {
    files.value = []
    return
  }
  const n = ++seq
  try {
    const res = await store.whenOpen(current.meta.id, () => store.transport!.files().search(current.meta.id, q, kind.value === 'code' ? 50 : 6))
    if (n === seq) files.value = res
  } catch {
    if (n === seq) files.value = []
  }
})

// What was said in sessions: searched by the client, a moment after typing stops.
const transcripts = ref<TranscriptHit[]>([])
let transcriptSeq = 0
let transcriptTimer: ReturnType<typeof setTimeout> | undefined
watch([query, kind, open], () => {
  clearTimeout(transcriptTimer)
  const q = query.value.trim()
  const n = ++transcriptSeq
  if (!open.value || q.length < 2 || (kind.value !== 'all' && kind.value !== 'sessions') || !store.transport) {
    transcripts.value = []
    return
  }
  transcriptTimer = setTimeout(async () => {
    try {
      const res = await store.transport!.library().searchTranscripts(q, kind.value === 'sessions' ? 50 : 8)
      if (n === transcriptSeq) transcripts.value = res
    } catch {
      if (n === transcriptSeq) transcripts.value = []
    }
  }, 200)
})
onBeforeUnmount(() => clearTimeout(transcriptTimer))

const actions = computed<Item[]>(() => [
  { key: 'a:new', icon: 'plus', label: 'New session', run: () => {
    close()
    navigateTo({ path: '/', query: { new: '1' } })
  } },
  { key: 'a:settings', icon: 'settings', label: 'Settings', run: () => {
    close()
    settings.open()
  } }
])

const groups = computed<Group[]>(() => {
  const q = query.value.trim()
  const k = kind.value
  const out: Group[] = []
  if (!q) {
    if (k === 'all' || k === 'sessions') {
      const attention = sessions.value.filter(s => s.meta.status === 'needs_input' || s.meta.status === 'error' || s.meta.unread).slice(0, 4)
      if (attention.length) out.push({ title: 'Needs attention', items: attention.map(sessionItem) })
      const seen = new Set(attention.map(s => s.meta.id))
      out.push({ title: 'Recents', items: sessions.value.filter(s => !seen.has(s.meta.id)).slice(0, k === 'all' ? 6 : 30).map(sessionItem) })
    }
    if (k === 'projects' || k === 'all') {
      const ps = projects.value.slice(0, k === 'all' ? 0 : 50)
      if (ps.length) out.push({ title: 'Projects', items: ps.map(projectItem) })
    }
    if (k === 'code') out.push({ title: 'Files', items: [] })
    if (k === 'all') out.push({ title: 'Actions', items: actions.value })
    return out.filter(g => g.items.length || g.title === 'Files')
  }
  if (k === 'all' || k === 'sessions') {
    const ss = sessions.value.filter(s => matches(`${s.meta.title} ${base(s.meta.cwd)}`, q))
    if (ss.length) out.push({ title: 'Sessions', items: ss.slice(0, k === 'all' ? 6 : 50).map(sessionItem) })
    if (transcripts.value.length) {
      out.push({ title: 'In transcripts', items: transcripts.value.map(h => ({
        key: `t:${h.sessionId}`,
        icon: h.remote ? 'monitor' as IconName : 'chat' as IconName,
        label: store.sessions[h.sessionId]?.meta.title || h.title || 'Untitled session',
        hint: h.snippet,
        run: () => openSession(h.sessionId)
      })) })
    }
  }
  if ((k === 'all' || k === 'code') && files.value.length) {
    out.push({ title: 'Code', items: files.value.map(path => ({
      key: `f:${path}`,
      icon: 'file' as IconName,
      label: base(path),
      hint: path,
      run: () => {
        close()
        show({ kind: 'file', path, lineStart: 0, lineEnd: 0 })
      }
    })) })
  }
  if (k === 'all' || k === 'projects') {
    const ps = projects.value.filter(p => matches(base(p.cwd), q))
    if (ps.length) out.push({ title: 'Projects', items: ps.slice(0, k === 'all' ? 4 : 50).map(projectItem) })
  }
  if (k === 'all') {
    const as = actions.value.filter(a => matches(a.label, q))
    if (as.length) out.push({ title: 'Actions', items: as })
  }
  return out
})
function projectItem(p: { cwd: string, n: number }): Item {
  return {
    key: `p:${p.cwd}`,
    icon: 'folder',
    label: base(p.cwd),
    hint: `${p.cwd.replace(/^\/Users\/[^/]+/, '~')} · ${p.n} ${p.n === 1 ? 'session' : 'sessions'}`,
    run: () => {
      close()
      navigateTo({ path: '/', query: { new: '1', cwd: p.cwd } })
    }
  }
}

const flat = computed(() => groups.value.flatMap(g => g.items))
watch(flat, () => (active.value = 0))

function onKey(e: KeyboardEvent) {
  if (!open.value) return
  if (e.key === 'Escape') {
    e.preventDefault()
    close()
  } else if (e.key === 'ArrowDown' || e.key === 'ArrowUp') {
    e.preventDefault()
    const n = flat.value.length
    if (n) active.value = (active.value + (e.key === 'ArrowDown' ? 1 : n - 1)) % n
    nextTick(() => document.querySelector('[data-search-active="true"]')?.scrollIntoView({ block: 'nearest' }))
  } else if ((e.key === 'ArrowLeft' || e.key === 'ArrowRight') && (!query.value || e.altKey)) {
    e.preventDefault()
    const i = KINDS.findIndex(k => k.key === kind.value)
    kind.value = KINDS[(i + (e.key === 'ArrowRight' ? 1 : KINDS.length - 1)) % KINDS.length]!.key
  } else if (e.key === 'Enter') {
    e.preventDefault()
    flat.value[active.value]?.run()
  }
}
watch(open, (v) => {
  if (v) document.addEventListener('keydown', onKey, true)
  else document.removeEventListener('keydown', onKey, true)
})
onBeforeUnmount(() => document.removeEventListener('keydown', onKey, true))
defineShortcuts({ meta_k: () => (open.value = !open.value) })
</script>

<template>
  <Teleport to="body">
    <div
      v-if="open"
      class="fixed inset-0 z-40 flex items-start justify-center bg-ground/60 px-6 pt-[12vh]"
      @pointerdown.self="close"
    >
      <div
        role="dialog"
        aria-modal="true"
        aria-label="Search"
        class="flex max-h-[70vh] w-[min(640px,100%)] flex-col overflow-hidden rounded-panel border border-line-strong bg-default"
      >
        <div class="flex items-center gap-2 px-5 pt-4">
          <HIcon
            name="search"
            class="text-muted"
          />
          <input
            ref="field"
            v-model="query"
            placeholder="Search"
            aria-label="Search"
            autocapitalize="off"
            autocorrect="off"
            spellcheck="false"
            class="min-w-0 flex-1 bg-transparent text-[15px] text-strong outline-none placeholder:text-muted"
          >
          <HIconBtn
            icon="close"
            label="Close"
            kbd="esc"
            @click="close"
          />
        </div>
        <div
          role="tablist"
          aria-label="Type"
          class="flex gap-1 border-b border-line px-4 pt-3 pb-3"
        >
          <button
            v-for="k in KINDS"
            :key="k.key"
            type="button"
            role="tab"
            :aria-selected="kind === k.key"
            class="h-7 px-2.5 text-[12.5px]"
            :class="kind === k.key ? 'bg-raised text-strong' : 'text-muted hover:text-strong'"
            @click="kind = k.key; field?.focus()"
          >
            {{ k.label }}
          </button>
        </div>
        <div class="min-h-0 flex-1 overflow-y-auto px-2 py-2">
          <template
            v-for="g in groups"
            :key="g.title"
          >
            <div class="px-3 pt-2 pb-1 text-[11.5px] text-muted">
              {{ g.title }}
            </div>
            <p
              v-if="!g.items.length"
              class="m-0 px-3 py-2 text-[12.5px] text-dimmed"
            >
              Type to find files in this session.
            </p>
            <button
              v-for="it in g.items"
              :key="it.key"
              type="button"
              :data-search-active="flat[active]?.key === it.key"
              class="flex h-9 w-full items-center gap-3 px-3 text-left text-[13px]"
              :class="flat[active]?.key === it.key ? 'bg-raised text-strong' : 'text-toned hover:bg-hover'"
              @mousemove="active = flat.indexOf(it)"
              @click="it.run()"
            >
              <HIcon
                :name="it.icon"
                class="shrink-0 text-muted"
              />
              <span class="min-w-0 truncate">{{ it.label }}</span>
              <span
                v-if="it.hint"
                class="min-w-0 flex-1 truncate text-[11.5px] text-dimmed"
              >{{ it.hint }}</span>
              <span
                v-else
                class="flex-1"
              />
              <HIcon
                v-if="flat[active]?.key === it.key"
                name="enter"
                :size="13"
                class="shrink-0 text-muted"
              />
            </button>
          </template>
          <p
            v-if="!groups.length"
            class="m-0 px-3 py-6 text-center text-[12.5px] text-muted"
          >
            Nothing matches.
          </p>
        </div>
        <div class="flex items-center gap-4 border-t border-line px-5 py-2.5 text-[11.5px] text-muted">
          <span class="flex items-center gap-1.5">Close <HKeys keys="escape" /></span>
          <span class="flex items-center gap-1.5">Change type <HKeys keys="arrowleft" /><HKeys keys="arrowright" /></span>
          <span class="flex items-center gap-1.5">Open <HKeys keys="enter" /></span>
        </div>
      </div>
    </div>
  </Teleport>
</template>
