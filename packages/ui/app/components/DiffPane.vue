<script setup lang="ts">
// Diff pane (header "Diff" button). Not drawn as its own artboard: built from Diff.dc.html —
// the turn summary on top, then every changed file of the session as a unified / split diff, edge to edge.
// The list is what the client measures on disk (GetSessionDiff), so shell commands' edits show; it refreshes on
// open, after any tool finishes, when a turn ends, on a light poll while visible and by the header's button. Sessions
// of another machine, and a client that cannot answer, show what the transcript's Edit / Write calls did instead.
// Review comments: hover a line, add a comment; it goes to the agent as soon as it is saved (queued while
// a turn runs) and stays on its line marked as sent.
import type { SessionState, ToolItem } from '~/lib/session/types'
import type { DiffComment, ThreadEntry } from '~/components/transcript/DiffView.vue'
import type { FileChange, PatchHunk } from '~/utils/tools'
import { isDesktop, openWithDefaultApp, revealInFolder } from '~/lib/desktop'
import { Virtualizer, VList } from 'virtua/vue'
import { SessionDiffLoader, type DiffBaseChoice, type DiffBases, type SessionDiff } from '~/lib/files/sessionDiff'

const props = defineProps<{ session: SessionState }>()
const { closePane } = usePanes()
const store = useSessionStore()

const relative = (path: string) => {
  const cwd = props.session.meta.cwd.replace(/\/+$/, '')
  return cwd && path.startsWith(`${cwd}/`) ? path.slice(cwd.length + 1) : path
}

// Comments survive closing and reopening the pane within the session (not a reload).
const comments = useState<DiffComment[]>(`diff-comments-${props.session.meta.id}`, () => [])
function reviewMessage(list: DiffComment[]) {
  return formatReview(list.map(c => ({ path: relative(c.path), line: c.line, sign: c.sign ?? (c.side === 'old' ? '-' : '+'), code: c.code, text: c.text })))
}

// Saved comments that have not gone out yet are sent right away, then marked; each keeps the message
// that carried it, to find the agent's answer.
watch(comments, async (list) => {
  const fresh = list.filter(c => !c.sent)
  if (!fresh.length) return
  const message = reviewMessage(fresh)
  comments.value = list.map(c => c.sent ? c : { ...c, sent: true, thread: [{ text: c.text, message }] })
  await store.send(props.session.meta.id, message)
})

// The agent's answer to a message: the last text it wrote between that message and the next user message.
function replyTo(message: string): ThreadEntry['reply'] {
  const items = props.session.items
  const at = items.findLastIndex(i => i.kind === 'user' && i.text.trim() === message.trim())
  if (at < 0) return null
  let reply: ThreadEntry['reply'] = { text: '', streaming: true }
  for (const it of items.slice(at + 1)) {
    if (it.kind === 'user') break
    if (it.kind !== 'assistant') continue
    const text = it.blocks.filter(b => b.type === 'text').map(b => b.text).join('\n').trim()
    if (text) reply = { text, streaming: it.streaming }
  }
  return reply
}

const threadOf = (c: DiffComment): ThreadEntry[] => (c.thread ?? []).map(e => ({ text: e.text, reply: replyTo(e.message) }))

// A reply continues the thread: same line, the user's new words.
async function onReply(c: DiffComment, text: string) {
  const message = reviewMessage([{ ...c, text }])
  comments.value = comments.value.map(x => x.id === c.id ? { ...x, thread: [...(x.thread ?? []), { text, message }] } : x)
  await store.send(props.session.meta.id, message)
}

// --- What the client measures ---
const sid = computed(() => props.session.meta.id)
const remote = computed(() => !!props.session.meta.remote)
// The last answer, per session: reopening the pane shows it at once. A failed refresh keeps it, a client that never
// answered leaves it null (the transcript's list shows).
const measured = useState<Record<string, SessionDiff>>('diff-measured', () => ({}))
const live = computed<SessionDiff | null>(() => remote.value ? null : measured.value[sid.value] ?? null)
const refreshing = ref(false)
// What the changes are compared with: all of them by default. Per session, kept while the app is open.
const base = useState<DiffBaseChoice>(`diff-base-${props.session.meta.id}`, () => ({ kind: 'all' }))
const loader = new SessionDiffLoader({
  base: base.value,
  // The client sets the pace; a hidden page does not ask.
  shouldPoll: () => document.visibilityState === 'visible',
  fetch: (ifNoneMatch, b) => {
    const t = store.transport
    if (!t) return Promise.reject(new Error('no transport'))
    return store.whenOpen(sid.value, () => t.sessionDiff(sid.value, { ifNoneMatch, base: b }))
  },
  onChange: (diff) => {
    measured.value = { ...measured.value, [sid.value]: diff }
  },
  onError: () => {
    // The transcript's list, or the last answer, stays on show.
  }
})

async function refresh() {
  if (remote.value) return
  refreshing.value = true
  await loader.load()
  refreshing.value = false
}

function restart() {
  loader.reset(measured.value[sid.value]?.version ?? '')
  void refresh()
}
onMounted(restart)
watch(sid, restart)
watch(() => store.transport, (t, old) => t && t !== old && restart())

// Any tool may have changed files (Bash above all): once one finishes, refresh, debounced over a burst.
const finishedTools = computed(() => props.session.items
  .filter(i => i.kind === 'tool' && i.status !== 'running')
  .map(i => i.id)
  .join(','))
let toolTimer: ReturnType<typeof setTimeout> | undefined
watch(finishedTools, (now, before) => {
  if (now === before) return
  clearTimeout(toolTimer)
  toolTimer = setTimeout(() => void refresh(), 500)
})
watch(() => props.session.meta.status, (s, before) => {
  if (before === 'working' && s !== 'working') void refresh()
})
onMounted(() => {
  if (!remote.value) loader.start()
})
onBeforeUnmount(() => {
  loader.stop()
  clearTimeout(toolTimer)
})

// The comparison menu: the choices come from the client when it opens.
const baseMenu = ref(false)
const baseSub = ref<'' | 'ref' | 'commits'>('')
const bases = ref<DiffBases | null>(null)
const branchFilter = ref('')
watch(baseMenu, async (on) => {
  baseSub.value = ''
  branchFilter.value = ''
  if (!on || remote.value || !store.transport) return
  const t = store.transport
  bases.value = await store.whenOpen(sid.value, () => t.listDiffBases(sid.value)).catch(() => bases.value)
})
const branches = computed(() => {
  const q = branchFilter.value.trim().toLowerCase()
  return (bases.value?.branches ?? []).filter(b => !q || b.name.toLowerCase().includes(q))
})
function pickBase(b: DiffBaseChoice) {
  base.value = b
  baseMenu.value = false
  measured.value = Object.fromEntries(Object.entries(measured.value).filter(([k]) => k !== sid.value))
  loadedHunks.value = {}
  loader.setBase(b)
  void refresh()
}
const baseTitle = computed(() => live.value?.baseLabel ? `${live.value.baseLabel} → ${live.value.headLabel || 'working tree'}` : '')

// A large diff comes without hunks: a file's arrive when it is opened, once per version of the diff.
const loadedHunks = ref<Record<string, { version: string, hunks: PatchHunk[] }>>({})
const hunksOf = (g: DiffGroup) => live.value?.large ? loadedHunks.value[g.path]?.hunks ?? [] : g.hunks
const fetching = new Set<string>()
watch(() => live.value?.large ? groups.value.filter(g => !isCollapsed(g.path) && !g.binary && !g.tooLarge).map(g => g.path) : [], async (open) => {
  const t = store.transport
  const version = live.value?.version ?? ''
  if (!t) return
  for (const path of open) {
    if (loadedHunks.value[path]?.version === version || fetching.has(path)) continue
    fetching.add(path)
    try {
      const f = await t.sessionFileDiff(sid.value, path, base.value)
      loadedHunks.value = { ...loadedHunks.value, [path]: { version, hunks: f.hunks } }
    } catch {
      loadedHunks.value = { ...loadedHunks.value, [path]: { version, hunks: [] } }
    } finally {
      fetching.delete(path)
    }
  }
})

// --- From the transcript ---
const edits = computed(() => props.session.items.filter((i): i is ToolItem => i.kind === 'tool' && isEditTool(i.name) && i.status === 'ok' && patchOf(i).length > 0))
const files = computed(() => fileChanges(edits.value))

interface DiffGroup {
  path: string
  label: string
  kind: FileChange['kind']
  oldPath: string
  hunks: PatchHunk[]
  stats: { add: number, del: number }
  binary: boolean
  tooLarge: boolean
}

// One continuous diff: one net diff per file.
const groups = computed<DiffGroup[]>(() => {
  if (live.value) {
    return live.value.files.map(f => ({
      path: f.path, label: f.label, kind: f.kind, oldPath: f.oldPath, hunks: f.hunks, stats: { add: f.add, del: f.del }, binary: f.binary, tooLarge: f.tooLarge
    }))
  }
  // Files in first-edit order.
  const map = new Map<string, { path: string, label: string, tools: ToolItem[] }>()
  for (const t of edits.value) {
    const path = editedPath(t)
    if (!map.has(path)) map.set(path, { path, label: toolTarget(t), tools: [] })
    map.get(path)!.tools.push(t)
  }
  // The file's net change when the edits can be replayed from the recorded original, else each edit's hunks.
  return [...map.values()].map(({ tools, path, label }) => {
    const hunks = netPatch(tools) ?? tools.flatMap(patchOf)
    const kind = files.value.find(f => f.path === path)?.kind ?? 'M'
    return { path, label: relative(label), kind, oldPath: '', hunks, stats: patchStats(hunks), binary: false, tooLarge: false }
  }).filter(g => g.hunks.length)
})

const VIEW_KEY = 'harness-diff-view'
const view = ref<'unified' | 'split'>('unified')
const viewMenu = ref(false)
onMounted(() => {
  try {
    if (localStorage.getItem(VIEW_KEY) === 'split') view.value = 'split'
  } catch {
    // Storage unavailable: unified.
  }
})
function setView(v: 'unified' | 'split') {
  view.value = v
  viewMenu.value = false
  try {
    localStorage.setItem(VIEW_KEY, v)
  } catch {
    // Not remembered.
  }
}

// Totals from the net diffs, not the sum of every edit.
const summary = computed(() => groups.value.map(g => ({ kind: g.kind, path: g.path, add: g.stats.add, del: g.stats.del })))

// Per-file state for this session: collapsed, and viewed (which also collapses it).
const collapsed = useState<Record<string, boolean>>(`diff-collapsed-${props.session.meta.id}`, () => ({}))
const viewed = useState<Record<string, boolean>>(`diff-viewed-${props.session.meta.id}`, () => ({}))
const fileMenu = ref('')
const desktop = isDesktop()
const copyMenu = ref(false)
const { openPane } = usePanes()
const filesOpen = useState<string>('files-open-path', () => '')
const attach = useState<string[]>('composer-attach', () => [])

function toggleViewed(path: string) {
  viewed.value[path] = !viewed.value[path]
  collapsed.value[path] = viewed.value[path]!
}

// A collapsed file mounts no diff. Until the user says otherwise, a large diff starts with every file collapsed.
const isCollapsed = (path: string) => collapsed.value[path] ?? !!live.value?.large

// Long files open in parts: a diff of many thousand lines is more than a page should lay out at once.
const LINES_STEP = 1000
const shownLines = reactive<Record<string, number>>({})
const lineCount = (g: DiffGroup) => hunksOf(g).reduce((n, h) => n + h.lines.length, 0)
function visibleHunks(g: DiffGroup): PatchHunk[] {
  let left = shownLines[g.path] ?? LINES_STEP
  if (lineCount(g) <= left) return hunksOf(g)
  const out: PatchHunk[] = []
  for (const h of hunksOf(g)) {
    if (left <= 0) break
    out.push(h.lines.length > left ? { ...h, lines: h.lines.slice(0, left) } : h)
    left -= h.lines.length
  }
  return out
}
const hiddenLines = (g: DiffGroup) => Math.max(0, lineCount(g) - (shownLines[g.path] ?? LINES_STEP))

// The pane's list: the summary, the note, then a row per file; only what is on screen is rendered.
type DiffRow = { kind: 'summary' } | { kind: 'truncated' } | { kind: 'file', group: DiffGroup }
const rows = computed<DiffRow[]>(() => [
  { kind: 'summary' },
  ...(live.value?.truncated ? [{ kind: 'truncated' as const }] : []),
  ...groups.value.map(group => ({ kind: 'file' as const, group }))
])

function openFile(path: string) {
  filesOpen.value = path
  openPane('files')
  fileMenu.value = ''
}

// Show: scroll to one file's changes (expanded), or to the top for the whole diff.
const list = ref<InstanceType<typeof VList> | null>(null)
const { target: showTarget } = useShow()
watch(showTarget, async (t) => {
  if (t?.kind !== 'diff') return
  showTarget.value = null
  if (t.path) collapsed.value[t.path] = false
  await nextTick()
  const at = t.path ? rows.value.findIndex(r => r.kind === 'file' && r.group.path === t.path) : -1
  list.value?.scrollToIndex(Math.max(at, 0))
}, { immediate: true })

// Why a file has no hunks to show.
function noHunks(g: DiffGroup) {
  if (g.binary) return 'Binary file'
  if (g.tooLarge) return 'Too large to show'
  if (live.value?.large && !loadedHunks.value[g.path]) return 'Loading…'
  if (hunksOf(g).length) return ''
  return g.kind === 'R' ? 'Renamed, no line changes' : 'No line changes'
}

function diffText(g: DiffGroup) {
  const out = [`--- a/${g.label}`, `+++ b/${g.label}`]
  for (const h of hunksOf(g)) out.push(`@@ -${h.oldStart},${h.oldLines} +${h.newStart},${h.newLines} @@${h.header ? ' ' + h.header : ''}`, ...h.lines)
  return out.join('\n')
}

async function copy(text: string) {
  try {
    await navigator.clipboard.writeText(text)
  } catch {
    // Clipboard unavailable (insecure context): nothing to do.
  }
  copyMenu.value = false
  fileMenu.value = ''
}
</script>

<template>
  <section
    :aria-label="tt('Changes')"
    class="flex h-full flex-col bg-default"
  >
    <div class="flex h-11 shrink-0 items-center gap-0.5 border-b border-default pr-1.5 pl-3.5">
      <span
        v-if="!baseTitle"
        class="font-display text-[13px] text-strong"
      >{{ tt('Changes') }}</span>
      <UPopover
        v-else
        v-model:open="baseMenu"
        :content="{ side: 'bottom', align: 'start', sideOffset: 4 }"
      >
        <button
          type="button"
          :aria-label="tt('Compare with')"
          class="flex min-w-0 items-center gap-1.5 px-1.5 py-1 font-display text-[13px] text-strong hover:bg-hover"
          :class="baseMenu && 'bg-hover'"
        >
          <span class="truncate">{{ baseTitle }}</span>
          <HIcon
            name="down"
            :size="12"
            class="shrink-0 text-muted"
          />
        </button>
        <template #content>
          <div
            role="menu"
            :aria-label="tt('Compare with')"
            class="w-[260px] border border-accented bg-hover py-1 text-[12.5px]"
          >
            <PaneMenuItem
              role="menuitemradio"
              :checked="base.kind === 'all'"
              :value="bases?.defaultLabel ? `vs ${bases.defaultLabel}` : ''"
              reserve
              @click="pickBase({ kind: 'all' })"
            >
              {{ tt('All changes') }}
            </PaneMenuItem>
            <PaneMenuItem
              v-if="bases?.inGit"
              role="menuitemradio"
              :checked="base.kind === 'uncommitted'"
              reserve
              @click="pickBase({ kind: 'uncommitted' })"
            >
              {{ tt('Uncommitted changes') }}
            </PaneMenuItem>
            <template v-if="bases?.inGit">
              <div class="my-1 h-px bg-line" />
              <UPopover
                :open="baseSub === 'ref'"
                :content="{ side: 'right', align: 'start', sideOffset: 4 }"
                @update:open="baseSub = $event ? 'ref' : ''"
              >
                <PaneMenuItem
                  sub
                  :value="base.kind === 'ref' ? base.ref : bases.defaultLabel"
                >
                  {{ tt('Compare against') }}
                </PaneMenuItem>
                <template #content>
                  <div class="flex max-h-[320px] w-[260px] flex-col border border-accented bg-hover text-[12.5px]">
                    <input
                      v-model="branchFilter"
                      :aria-label="tt('Filter branches')"
                      :placeholder="tt('Filter branches')"
                      class="border-b border-default bg-transparent px-[9px] py-1.5 outline-none"
                    >
                    <div class="min-h-0 flex-1 overflow-y-auto py-1">
                      <Virtualizer
                        v-slot="{ item: b }"
                        :data="branches"
                      >
                        <PaneMenuItem
                          :key="b.name"
                          role="menuitemradio"
                          :checked="base.kind === 'ref' && base.ref === b.name"
                          reserve
                          @click="pickBase({ kind: 'ref', ref: b.name })"
                        >
                          {{ b.name }}
                        </PaneMenuItem>
                      </Virtualizer>
                    </div>
                  </div>
                </template>
              </UPopover>
              <UPopover
                :open="baseSub === 'commits'"
                :content="{ side: 'right', align: 'start', sideOffset: 4 }"
                @update:open="baseSub = $event ? 'commits' : ''"
              >
                <PaneMenuItem
                  sub
                  :value="String(bases.commits.length)"
                  :disabled="!bases.commits.length"
                >
                  {{ tt('Commits') }}
                </PaneMenuItem>
                <template #content>
                  <div class="h-[320px] max-h-[60vh] w-[340px] overflow-y-auto border border-accented bg-hover py-1 text-[12.5px]">
                    <Virtualizer
                      v-slot="{ item: c }"
                      :data="bases.commits"
                    >
                      <PaneMenuItem
                        :key="c.sha"
                        role="menuitemradio"
                        :checked="base.kind === 'commit' && base.sha === c.sha"
                        :value="c.sha.slice(0, 7)"
                        reserve
                        @click="pickBase({ kind: 'commit', sha: c.sha })"
                      >
                        <span class="block max-w-[210px] truncate">{{ c.subject }}</span>
                      </PaneMenuItem>
                    </Virtualizer>
                  </div>
                </template>
              </UPopover>
            </template>
          </div>
        </template>
      </UPopover>
      <span
        v-if="live?.large"
        class="ml-1.5 shrink-0 border border-line px-1.5 py-px text-[11px] text-muted"
        :title="tt('A large diff: files stay collapsed until you open them')"
      >{{ tt('Large diff') }}</span>
      <span class="flex-1" />
      <HIconBtn
        v-if="!remote"
        icon="refresh"
        :label="tt('Refresh')"
        :size="30"
        :class="refreshing && 'text-muted'"
        @click="refresh"
      />
      <UPopover
        v-model:open="viewMenu"
        :content="{ side: 'bottom', align: 'end', sideOffset: 4 }"
      >
        <HIconBtn
          icon="list"
          :label="tt('Diff view')"
          :size="30"
          :pressed="viewMenu"
        />
        <template #content>
          <div
            role="menu"
            :aria-label="tt('Diff view')"
            class="w-[180px] border border-accented bg-hover py-1 text-[12.5px]"
          >
            <PaneMenuItem
              v-for="v in (['unified', 'split'] as const)"
              :key="v"
              role="menuitemradio"
              :checked="view === v"
              reserve
              @click="setView(v)"
            >
              {{ v === 'unified' ? tt('Unified') : tt('Split') }}
            </PaneMenuItem>
          </div>
        </template>
      </UPopover>
      <PaneFullBtn />
      <HIconBtn
        icon="close"
        :label="tt('Close')"
        :size="30"
        @click="closePane('diff')"
      />
    </div>
    <div
      v-if="!groups.length"
      class="flex flex-1 flex-col items-center justify-center gap-2 p-6 text-center"
    >
      <HIcon
        name="diff"
        :size="24"
        class="text-muted"
      />
      <span class="text-strong">{{ tt('No changes yet') }}</span>
      <span class="max-w-80 text-[12px] leading-[1.55] text-muted">{{ live ? tt('Files changed in this session show up here for review.') : tt('Edits the agent makes in this session show up here for review.') }}</span>
    </div>
    <VList
      v-else
      ref="list"
      v-slot="{ item: row }"
      :data="rows"
      class="min-h-0 flex-1"
    >
      <DiffSummary
        v-if="row.kind === 'summary'"
        :files="summary"
        flush
        total-only
      />
      <div
        v-else-if="row.kind === 'truncated'"
        class="border-b border-default px-3 py-[5px] text-[12px] text-muted"
      >
        {{ tt('Too many changed files: showing the first {0}.', [groups.length]) }}
      </div>
      <section
        v-else
        :key="row.group.path"
        class="border-b border-default"
      >
        <div
          :title="row.group.oldPath ? `${row.group.oldPath} → ${row.group.path}` : row.group.path"
          class="sticky top-0 z-10 flex cursor-pointer items-center gap-2 border-b border-default bg-surface py-[3px] pr-1.5 pl-2 text-[12px] text-toned"
          @click="collapsed[row.group.path] = !isCollapsed(row.group.path)"
        >
          <HIcon
            name="down"
            :size="12"
            class="shrink-0 text-muted transition-transform"
            :class="isCollapsed(row.group.path) && '-rotate-90'"
          />
          <span
            class="w-3 shrink-0"
            :class="row.group.kind === 'A' ? 'text-ok' : row.group.kind === 'D' ? 'text-bad' : 'text-warn'"
          >{{ row.group.kind }}</span>
          <span
            class="min-w-0 flex-1 truncate"
            :class="viewed[row.group.path] && 'text-muted'"
          >{{ row.group.label }}</span>
          <span class="text-ok">+{{ row.group.stats.add }}</span><span class="text-bad">−{{ row.group.stats.del }}</span>
          <div
            class="flex items-center"
            @click.stop
          >
            <HIconBtn
              icon="check"
              :label="viewed[row.group.path] ? tt('Mark as not viewed') : tt('Mark as viewed')"
              :size="26"
              :icon-size="13"
              :pressed="viewed[row.group.path]"
              :class="viewed[row.group.path] && 'text-accent'"
              @click="toggleViewed(row.group.path)"
            />
            <HIconBtn
              icon="arrow-up-right"
              :label="tt('Open file')"
              :size="26"
              :icon-size="13"
              @click="openFile(row.group.path)"
            />
            <UPopover
              :open="fileMenu === row.group.path"
              :content="{ side: 'bottom', align: 'end', sideOffset: 4 }"
              @update:open="fileMenu = $event ? row.group.path : ''"
            >
              <HIconBtn
                icon="more"
                :label="tt('File actions')"
                :size="26"
                :icon-size="13"
                :pressed="fileMenu === row.group.path"
              />
              <template #content>
                <div
                  role="menu"
                  :aria-label="tt('File actions')"
                  class="w-[250px] border border-accented bg-hover py-1 text-[12.5px]"
                >
                  <PaneMenuItem @click="attach = [...attach, relative(row.group.path)]; fileMenu = ''">
                    {{ tt('Attach as context') }}
                  </PaneMenuItem>
                  <UPopover
                    v-model:open="copyMenu"
                    :content="{ side: 'left', align: 'start', sideOffset: 4 }"
                  >
                    <PaneMenuItem sub>
                      {{ tt('Copy') }}
                    </PaneMenuItem>
                    <template #content>
                      <div
                        role="menu"
                        :aria-label="tt('Copy')"
                        class="w-[180px] border border-accented bg-hover py-1 text-[12.5px]"
                      >
                        <PaneMenuItem @click="copy(relative(row.group.path))">
                          {{ tt('Relative path') }}
                        </PaneMenuItem>
                        <PaneMenuItem @click="copy(row.group.path)">
                          {{ tt('Absolute path') }}
                        </PaneMenuItem>
                        <PaneMenuItem @click="copy(diffText(row.group))">
                          {{ tt('Diff') }}
                        </PaneMenuItem>
                      </div>
                    </template>
                  </UPopover>
                  <div class="my-1 h-px bg-line" />
                  <PaneMenuItem @click="openFile(row.group.path)">
                    {{ tt('Show whole file') }}
                  </PaneMenuItem>
                  <PaneMenuItem @click="toggleViewed(row.group.path); fileMenu = ''">
                    {{ viewed[row.group.path] ? tt('Mark as not viewed') : tt('Mark as viewed') }}
                  </PaneMenuItem>
                  <div class="my-1 h-px bg-line" />
                  <PaneMenuItem @click="openFile(row.group.path)">
                    {{ tt('Open file') }}
                  </PaneMenuItem>
                  <PaneMenuItem
                    :disabled="!desktop"
                    :value="desktop ? '' : 'desktop'"
                    @click="desktop && (openWithDefaultApp(row.group.path), fileMenu = '')"
                  >
                    {{ tt('Open in default app') }}
                  </PaneMenuItem>
                  <PaneMenuItem
                    :disabled="!desktop"
                    :value="desktop ? '' : 'desktop'"
                    @click="desktop && (revealInFolder(row.group.path), fileMenu = '')"
                  >
                    {{ tt('Show in Finder') }}
                  </PaneMenuItem>
                </div>
              </template>
            </UPopover>
          </div>
        </div>
        <div
          v-if="!isCollapsed(row.group.path) && noHunks(row.group)"
          class="px-3 py-2 text-[12px] text-muted"
        >
          {{ noHunks(row.group) }}
        </div>
        <template v-else-if="!isCollapsed(row.group.path)">
          <DiffView
            v-model:comments="comments"
            :path="row.group.path"
            :hunks="visibleHunks(row.group)"
            :mode="view"
            :thread-of="threadOf"
            bare
            commentable
            @reply="onReply"
          />
          <button
            v-if="hiddenLines(row.group)"
            type="button"
            class="w-full border-t border-default px-3 py-1.5 text-left text-[12px] text-muted hover:bg-hover"
            @click="shownLines[row.group.path] = (shownLines[row.group.path] ?? LINES_STEP) + LINES_STEP"
          >
            {{ tt('Show {0} more of {1} lines', [Math.min(hiddenLines(row.group), LINES_STEP), hiddenLines(row.group)]) }}
          </button>
        </template>
      </section>
    </VList>
  </section>
</template>
