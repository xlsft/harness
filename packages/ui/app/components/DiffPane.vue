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
import { SessionDiffLoader, type SessionDiff } from '~/lib/files/sessionDiff'

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
const loader = new SessionDiffLoader({
  fetch: (ifNoneMatch) => {
    const t = store.transport
    if (!t) return Promise.reject(new Error('no transport'))
    return store.whenOpen(sid.value, () => t.sessionDiff(sid.value, { ifNoneMatch }))
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
// A light poll while the page is visible: the user and background tasks change files as well.
const POLL_MS = 5000
let poll: ReturnType<typeof setInterval> | undefined
onMounted(() => {
  poll = setInterval(() => {
    if (document.visibilityState === 'visible') void refresh()
  }, POLL_MS)
})
onBeforeUnmount(() => {
  clearInterval(poll)
  clearTimeout(toolTimer)
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

function openFile(path: string) {
  filesOpen.value = path
  openPane('files')
  fileMenu.value = ''
}

// Show: scroll to one file's changes (expanded), or to the top for the whole diff.
const scroller = ref<HTMLElement | null>(null)
const { target: showTarget } = useShow()
watch(showTarget, async (t) => {
  if (t?.kind !== 'diff') return
  showTarget.value = null
  if (t.path) collapsed.value[t.path] = false
  await nextTick()
  const el = t.path ? document.getElementById(`diff-${t.path}`) : null
  if (el) el.scrollIntoView({ block: 'start' })
  else scroller.value?.scrollTo({ top: 0 })
}, { immediate: true })

// Why a file has no hunks to show.
function noHunks(g: DiffGroup) {
  if (g.binary) return 'Binary file'
  if (g.tooLarge) return 'Too large to show'
  if (g.kind === 'R') return 'Renamed, no line changes'
  return g.hunks.length ? '' : 'No line changes'
}

function diffText(g: DiffGroup) {
  const out = [`--- a/${g.label}`, `+++ b/${g.label}`]
  for (const h of g.hunks) out.push(`@@ -${h.oldStart},${h.oldLines} +${h.newStart},${h.newLines} @@${h.header ? ' ' + h.header : ''}`, ...h.lines)
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
    aria-label="Changes"
    class="flex h-full flex-col bg-default"
  >
    <div class="flex h-11 shrink-0 items-center gap-0.5 border-b border-default pr-1.5 pl-3.5">
      <span class="font-display text-[13px] text-strong">Changes</span>
      <span class="flex-1" />
      <HIconBtn
        v-if="!remote"
        icon="refresh"
        label="Refresh"
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
          label="Diff view"
          :size="30"
          :pressed="viewMenu"
        />
        <template #content>
          <div
            role="menu"
            aria-label="Diff view"
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
              {{ v === 'unified' ? 'Unified' : 'Split' }}
            </PaneMenuItem>
          </div>
        </template>
      </UPopover>
      <HIconBtn
        icon="full"
        label="Maximize"
        :size="30"
      />
      <HIconBtn
        icon="close"
        label="Close"
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
      <span class="text-strong">No changes yet</span>
      <span class="max-w-80 text-[12px] leading-[1.55] text-muted">{{ live ? 'Files changed in this session show up here for review.' : 'Edits the agent makes in this session show up here for review.' }}</span>
    </div>
    <div
      v-else
      ref="scroller"
      class="flex min-h-0 flex-1 flex-col overflow-y-auto"
    >
      <DiffSummary
        :files="summary"
        flush
        total-only
      />
      <div
        v-if="live?.truncated"
        class="border-b border-default px-3 py-[5px] text-[12px] text-muted"
      >
        Too many changed files: showing the first {{ groups.length }}.
      </div>
      <section
        v-for="g in groups"
        :id="`diff-${g.path}`"
        :key="g.path"
        class="not-first-of-type:border-t not-first-of-type:border-default"
      >
        <div
          :title="g.oldPath ? `${g.oldPath} → ${g.path}` : g.path"
          class="sticky top-0 z-10 flex cursor-pointer items-center gap-2 border-b border-default bg-surface py-[3px] pr-1.5 pl-2 text-[12px] text-toned"
          @click="collapsed[g.path] = !collapsed[g.path]"
        >
          <HIcon
            name="down"
            :size="12"
            class="shrink-0 text-muted transition-transform"
            :class="collapsed[g.path] && '-rotate-90'"
          />
          <span
            class="w-3 shrink-0"
            :class="g.kind === 'A' ? 'text-ok' : g.kind === 'D' ? 'text-bad' : 'text-warn'"
          >{{ g.kind }}</span>
          <span
            class="min-w-0 flex-1 truncate"
            :class="viewed[g.path] && 'text-muted'"
          >{{ g.label }}</span>
          <span class="text-ok">+{{ g.stats.add }}</span><span class="text-bad">−{{ g.stats.del }}</span>
          <div
            class="flex items-center"
            @click.stop
          >
            <HIconBtn
              icon="check"
              :label="viewed[g.path] ? 'Mark as not viewed' : 'Mark as viewed'"
              :size="26"
              :icon-size="13"
              :pressed="viewed[g.path]"
              :class="viewed[g.path] && 'text-accent'"
              @click="toggleViewed(g.path)"
            />
            <HIconBtn
              icon="arrow-up-right"
              label="Open file"
              :size="26"
              :icon-size="13"
              @click="openFile(g.path)"
            />
            <UPopover
              :open="fileMenu === g.path"
              :content="{ side: 'bottom', align: 'end', sideOffset: 4 }"
              @update:open="fileMenu = $event ? g.path : ''"
            >
              <HIconBtn
                icon="more"
                label="File actions"
                :size="26"
                :icon-size="13"
                :pressed="fileMenu === g.path"
              />
              <template #content>
                <div
                  role="menu"
                  aria-label="File actions"
                  class="w-[250px] border border-accented bg-hover py-1 text-[12.5px]"
                >
                  <PaneMenuItem @click="attach = [...attach, relative(g.path)]; fileMenu = ''">
                    Attach as context
                  </PaneMenuItem>
                  <UPopover
                    v-model:open="copyMenu"
                    :content="{ side: 'left', align: 'start', sideOffset: 4 }"
                  >
                    <PaneMenuItem sub>
                      Copy
                    </PaneMenuItem>
                    <template #content>
                      <div
                        role="menu"
                        aria-label="Copy"
                        class="w-[180px] border border-accented bg-hover py-1 text-[12.5px]"
                      >
                        <PaneMenuItem @click="copy(relative(g.path))">
                          Relative path
                        </PaneMenuItem>
                        <PaneMenuItem @click="copy(g.path)">
                          Absolute path
                        </PaneMenuItem>
                        <PaneMenuItem @click="copy(diffText(g))">
                          Diff
                        </PaneMenuItem>
                      </div>
                    </template>
                  </UPopover>
                  <div class="my-1 h-px bg-line" />
                  <PaneMenuItem @click="openFile(g.path)">
                    Show whole file
                  </PaneMenuItem>
                  <PaneMenuItem @click="toggleViewed(g.path); fileMenu = ''">
                    {{ viewed[g.path] ? 'Mark as not viewed' : 'Mark as viewed' }}
                  </PaneMenuItem>
                  <div class="my-1 h-px bg-line" />
                  <PaneMenuItem @click="openFile(g.path)">
                    Open file
                  </PaneMenuItem>
                  <PaneMenuItem
                    :disabled="!desktop"
                    :value="desktop ? '' : 'desktop'"
                    @click="desktop && (openWithDefaultApp(g.path), fileMenu = '')"
                  >
                    Open in default app
                  </PaneMenuItem>
                  <PaneMenuItem
                    :disabled="!desktop"
                    :value="desktop ? '' : 'desktop'"
                    @click="desktop && (revealInFolder(g.path), fileMenu = '')"
                  >
                    Show in Finder
                  </PaneMenuItem>
                </div>
              </template>
            </UPopover>
          </div>
        </div>
        <div
          v-if="noHunks(g)"
          v-show="!collapsed[g.path]"
          class="px-3 py-2 text-[12px] text-muted"
        >
          {{ noHunks(g) }}
        </div>
        <DiffView
          v-else
          v-show="!collapsed[g.path]"
          v-model:comments="comments"
          :path="g.path"
          :hunks="g.hunks"
          :mode="view"
          :thread-of="threadOf"
          bare
          commentable
          @reply="onReply"
        />
      </section>
    </div>
  </section>
</template>
