<script setup lang="ts">
// Files pane: the session's working directories (cwd and additional directories) on the client's machine, through
// the transport's FilesApi (lib/files). The tree on the right expands folders lazily, marks git changes and
// hides ignored entries unless asked; the left side shows open files as tabs (a single click opens an italic
// preview tab, a double click keeps it). The filter searches file names (fuzzy, the loaded tree plus SuggestFiles).
// The tree, git status and the open file refresh after the agent's file edits, when a turn ends and every few
// seconds while the pane is visible; Show and "Open file" from other panes open files here, at their lines.
import { usePanes } from '~/composables/usePanes'
import type { SessionState, ToolItem } from '~/lib/session/types'
import type { FileContent, FileEntry, FileRoot, GitStatus } from '~/lib/files/types'
import {
  absolutePath, ancestorsToExpand, baseName, buildRows, dirName, filterPaths, isWithin, relativePath, showTargetFile,
  statusOf, writtenPaths, type TreeRow
} from '~/lib/files/tree'
import { isDesktop, openWithDefaultApp, revealInFolder } from '~/lib/desktop'

const props = defineProps<{ session: SessionState, menu?: string }>()

const store = useSessionStore()
const toast = useToast()
const { closePane } = usePanes()
const { show } = useShow()
const { openTerminal } = useTerminalRequests()
const desktop = isDesktop()
const attach = useState<string[]>('composer-attach', () => [])

const sid = computed(() => props.session.meta.id)
const remote = computed(() => !!props.session.meta.remote)
const api = () => store.transport?.files()

function message(e: unknown) {
  return e instanceof Error ? e.message.replace(/^\[\w+\]\s*/, '') : String(e)
}

// --- Settings (this browser) ---
const SETTINGS_KEY = 'harness-files-settings'
interface Settings { tree: boolean, wrap: boolean, showIgnored: boolean, tabSize: number }
function loadSettings(): Settings {
  const d: Settings = { tree: true, wrap: false, showIgnored: false, tabSize: 4 }
  if (!import.meta.client) return d
  try {
    return { ...d, ...JSON.parse(localStorage.getItem(SETTINGS_KEY) ?? '{}') }
  } catch {
    return d
  }
}
const settings = useState<Settings>('files-settings', loadSettings)
watch(settings, (s) => {
  try {
    localStorage.setItem(SETTINGS_KEY, JSON.stringify(s))
  } catch {
    // Storage unavailable: settings last for this page only.
  }
}, { deep: true })

// --- Per-session view state (kept while the app runs, so the pane reopens where it was) ---
interface Tab { path: string, pinned: boolean }
interface ViewState {
  tabs: Tab[]
  selected: string
  expanded: string[]
  // Markdown and SVG files shown as source.
  source: Record<string, boolean>
}
const views = useState<Record<string, ViewState>>('files-views', () => ({}))
watch(sid, (id) => {
  if (!views.value[id]) views.value[id] = { tabs: [], selected: '', expanded: [], source: {} }
}, { immediate: true })
const vs = computed<ViewState>(() => views.value[sid.value]!)

// --- Loaded data (this pane instance) ---
const roots = ref<FileRoot[]>([])
const rootsError = ref('')
const listings = ref<Record<string, FileEntry[]>>({})
const status = ref<Record<string, GitStatus>>({})
const contents = ref<Record<string, FileContent>>({})
const readErrors = ref<Record<string, string>>({})
const reading = ref<Record<string, boolean>>({})

const rows = computed(() => buildRows({ roots: roots.value, listings: listings.value, expanded: new Set(vs.value.expanded), status: status.value }))

async function loadRoots() {
  const a = api()
  if (!a || remote.value) return
  rootsError.value = ''
  try {
    roots.value = await a.roots(sid.value)
    if (!vs.value.expanded.length && roots.value[0]) vs.value.expanded = [roots.value[0].path]
    await Promise.all([...vs.value.expanded.map(d => loadDir(d)), loadStatus()])
  } catch (e) {
    rootsError.value = message(e)
  }
}

async function loadDir(dir: string) {
  const a = api()
  if (!a) return
  try {
    const l = await a.list(sid.value, dir, { showIgnored: settings.value.showIgnored })
    listings.value = { ...listings.value, [dir]: l.entries }
  } catch (e) {
    // Gone (deleted, or moved out of reach): fold it away.
    vs.value.expanded = vs.value.expanded.filter(d => !isWithin(d, dir))
    const { [dir]: _gone, ...rest } = listings.value
    listings.value = rest
    if (roots.value.some(r => r.path === dir)) rootsError.value = message(e)
  }
}

async function loadStatus() {
  const a = api()
  if (!a) return
  // One call per repository: roots in the same repository share it.
  const seen = new Set<string>()
  const merged: Record<string, GitStatus> = {}
  for (const r of roots.value) {
    const key = r.gitRoot || r.path
    if (!r.gitRoot || seen.has(key)) continue
    seen.add(key)
    try {
      Object.assign(merged, (await a.gitStatus(sid.value, r.path)).files)
    } catch {
      // No status for this repository: the tree shows none.
    }
  }
  status.value = merged
}

async function read(path: string, force = false) {
  const a = api()
  if (!a || !path) return
  const known = contents.value[path]
  reading.value = { ...reading.value, [path]: true }
  try {
    const c = await a.read(sid.value, path, { ifNoneMatch: force ? undefined : known?.etag })
    if (!c.notModified) contents.value = { ...contents.value, [path]: c }
    const { [path]: _e, ...rest } = readErrors.value
    readErrors.value = rest
  } catch (e) {
    readErrors.value = { ...readErrors.value, [path]: message(e) }
  } finally {
    reading.value = { ...reading.value, [path]: false }
  }
}

// Everything visible again: roots, expanded listings, git status, the open file (only when it changed).
let refreshing: Promise<void> | null = null
function refresh() {
  if (refreshing || remote.value) return refreshing
  refreshing = (async () => {
    try {
      if (!roots.value.length) await loadRoots()
      else await Promise.all([...vs.value.expanded.map(d => loadDir(d)), loadStatus(), read(vs.value.selected)])
    } finally {
      refreshing = null
    }
  })()
  return refreshing
}

function reset() {
  roots.value = []
  listings.value = {}
  status.value = {}
  contents.value = {}
  readErrors.value = {}
  void loadRoots().then(() => read(vs.value.selected))
}

onMounted(reset)
watch(sid, reset)
watch(() => store.transport, (t, old) => t && t !== old && reset())
watch(() => settings.value.showIgnored, () => {
  listings.value = {}
  void Promise.all(vs.value.expanded.map(d => loadDir(d)))
})

// --- Live updates ---
// The agent's file edits: once each finishes, refresh (debounced, so a burst of edits refreshes once).
const finishedWrites = computed(() => props.session.items
  .filter((i): i is ToolItem => i.kind === 'tool' && i.status !== 'running' && writtenPaths(i).length > 0)
  .map(i => i.id)
  .join(','))
let writeTimer: ReturnType<typeof setTimeout> | undefined
watch(finishedWrites, (now, before) => {
  if (now === before) return
  clearTimeout(writeTimer)
  writeTimer = setTimeout(() => void refresh(), 300)
})
// A turn ending catches what shell commands changed.
watch(() => props.session.meta.status, (s, before) => {
  if (before === 'working' && s !== 'working') void refresh()
})
// A light poll while the page is visible.
const POLL_MS = 8000
let poll: ReturnType<typeof setInterval> | undefined
onMounted(() => {
  poll = setInterval(() => {
    if (document.visibilityState === 'visible') void refresh()
  }, POLL_MS)
})
onBeforeUnmount(() => {
  clearInterval(poll)
  clearTimeout(writeTimer)
})

// --- Tree ---
const treeRef = ref<{ reveal: (path: string) => void } | null>(null)

function expand(path: string) {
  if (!vs.value.expanded.includes(path)) vs.value.expanded = [...vs.value.expanded, path]
  if (!listings.value[path]) void loadDir(path)
}

function toggle(r: TreeRow) {
  if (vs.value.expanded.includes(r.path)) vs.value.expanded = vs.value.expanded.filter(d => d !== r.path)
  else expand(r.path)
}

async function revealInTree(path: string) {
  const dirs = ancestorsToExpand(path, roots.value)
  if (!dirs.length) return
  vs.value.expanded = [...new Set([...vs.value.expanded, ...dirs])]
  await Promise.all(dirs.filter(d => !listings.value[d]).map(d => loadDir(d)))
  treeRef.value?.reveal(path)
}

// --- Tabs ---
// Lines Show asked for, per file, and a counter to scroll to them again.
const shownLines = ref<Record<string, { start: number, end: number }>>({})
const scrollRequest = ref(0)

function openFile(path: string, pin = false) {
  const tabs = vs.value.tabs
  const at = tabs.findIndex(t => t.path === path)
  if (at >= 0) {
    if (pin) tabs[at]!.pinned = true
  } else {
    // A preview tab (not pinned) is replaced by the next one.
    const preview = tabs.findIndex(t => !t.pinned)
    if (preview >= 0 && !pin) tabs.splice(preview, 1, { path, pinned: false })
    else tabs.push({ path, pinned: pin })
  }
  vs.value.selected = path
  void read(path)
}

function selectTab(path: string) {
  vs.value.selected = path
  void read(path)
}

function closeTab(path: string) {
  const tabs = vs.value.tabs
  const i = tabs.findIndex(t => t.path === path)
  if (i < 0) return
  tabs.splice(i, 1)
  if (vs.value.selected === path) {
    const next = tabs[Math.min(i, tabs.length - 1)]
    vs.value.selected = next?.path ?? ''
    if (next) void read(next.path)
  }
  const { [path]: _c, ...rest } = contents.value
  contents.value = rest
}

function onTreeOpen(r: TreeRow, pin: boolean) {
  if (r.deleted) {
    toast.add({ title: `${r.name} was deleted`, description: 'Open it in the diff to see what it had.', color: 'neutral' })
    return
  }
  openFile(r.path, pin)
}

const selectedContent = computed(() => contents.value[vs.value.selected] ?? null)
const source = computed({
  get: () => !!vs.value.source[vs.value.selected],
  set: (v: boolean) => {
    vs.value.source = { ...vs.value.source, [vs.value.selected]: v }
  }
})
const canToggleSource = computed(() => {
  const c = selectedContent.value
  if (!c || c.binary) return false
  return /\.(md|markdown|mdx|svg)$/i.test(vs.value.selected)
})

// The breadcrumb: the root's name, then the path inside it.
const crumbs = computed(() => vs.value.selected ? relativePath(vs.value.selected, roots.value).split('/') : [])

// --- Opening from elsewhere ---
function cwd() {
  return roots.value[0]?.path ?? props.session.meta.cwd
}

// Show: open the file (kept, not a preview), then scroll to its lines, highlighted.
const { target: showTarget } = useShow()
watch(showTarget, (t) => {
  if (t?.kind !== 'file' || remote.value) return
  showTarget.value = null
  const { path, lines } = showTargetFile(t, cwd())
  if (lines) shownLines.value = { ...shownLines.value, [path]: lines }
  else {
    const { [path]: _l, ...rest } = shownLines.value
    shownLines.value = rest
  }
  openFile(path, true)
  scrollRequest.value++
  void revealInTree(path)
}, { immediate: true })

// "Open file" from other panes (diff).
const filesOpen = useState<string>('files-open-path', () => '')
watch(filesOpen, (p) => {
  if (!p || remote.value) return
  filesOpen.value = ''
  const path = absolutePath(p, cwd())
  openFile(path, true)
  void revealInTree(path)
}, { immediate: true })
// The roots arrive after a request that came in first: reveal it then.
watch(roots, () => vs.value.selected && void revealInTree(vs.value.selected))

// --- Filter ---
const query = ref('')
const searchHits = ref<string[]>([])
const searching = ref(false)
let searchTimer: ReturnType<typeof setTimeout> | undefined
let searchSeq = 0
watch(query, (q) => {
  clearTimeout(searchTimer)
  if (!q.trim()) {
    searchHits.value = []
    return
  }
  searchTimer = setTimeout(async () => {
    const a = api()
    if (!a || remote.value) return
    const seq = ++searchSeq
    searching.value = true
    try {
      const paths = await a.search(sid.value, q.trim(), 100)
      if (seq === searchSeq) searchHits.value = paths.map(p => (p.endsWith('/') ? `${absolutePath(p, cwd())}/` : absolutePath(p, cwd())))
    } catch {
      if (seq === searchSeq) searchHits.value = []
    } finally {
      if (seq === searchSeq) searching.value = false
    }
  }, 150)
})
const hits = computed(() => {
  const q = query.value.trim()
  if (!q) return []
  const loaded = Object.values(listings.value).flat().filter(e => !e.deleted).map(e => ({ path: e.path, dir: e.kind === 'dir' }))
  return filterPaths(q, [...loaded, ...searchHits.value.map(p => ({ path: p }))], roots.value, 200)
})

function openHit(h: { path: string, dir: boolean }) {
  query.value = ''
  if (h.dir) {
    void revealInTree(h.path).then(() => {
      expand(h.path)
      treeRef.value?.reveal(h.path)
    })
    return
  }
  openFile(h.path, true)
  void revealInTree(h.path)
}

// --- Actions ---
interface Action { label: string, run: () => unknown, sep?: boolean }
const menuAt = ref<{ x: number, y: number } | null>(null)
const menuRow = ref<{ path: string, dir: boolean, git: GitStatus, name: string } | null>(null)
const menuOpen = computed({
  get: () => !!menuRow.value,
  set: (v: boolean) => {
    if (!v) menuRow.value = null
  }
})
const section = ref<HTMLElement | null>(null)

function openMenu(target: { path: string, dir: boolean, git: GitStatus, name: string }, at: { x: number, y: number }) {
  const box = section.value?.getBoundingClientRect()
  const w = 220
  const x = box ? Math.min(at.x - box.left, box.width - w - 4) : at.x
  const y = box ? at.y - box.top : at.y
  menuAt.value = { x: Math.max(4, x), y }
  menuRow.value = target
}

function onRowMenu(r: TreeRow, at: { x: number, y: number }) {
  openMenu({ path: r.path, dir: r.kind === 'dir', git: r.git, name: r.name }, at)
}

function onTabMenu(path: string, e: MouseEvent) {
  openMenu({ path, dir: false, git: statusOf(path, status.value), name: baseName(path) }, { x: e.clientX, y: e.clientY })
}

async function copy(text: string, what: string) {
  try {
    await navigator.clipboard.writeText(text)
    toast.add({ title: `Copied ${what}`, color: 'neutral' })
  } catch {
    toast.add({ title: 'Could not copy', description: 'The clipboard is not available here.', color: 'error' })
  }
}

// An @mention for the composer: relative to the session's cwd when inside it.
function mention(path: string) {
  const c = props.session.meta.cwd.replace(/\/+$/, '')
  const root = cwd().replace(/\/+$/, '')
  for (const base of [root, c]) {
    if (base && path.startsWith(`${base}/`)) return path.slice(base.length + 1)
  }
  return path
}

const actions = computed<Action[]>(() => {
  const t = menuRow.value
  if (!t) return []
  const out: Action[] = []
  if (!t.dir) out.push({ label: 'Open', run: () => openFile(t.path, true) })
  if (!t.dir && t.git) out.push({ label: 'Open in diff', run: () => show({ kind: 'diff', path: t.path }) })
  out.push(
    { label: 'Attach as context', run: () => (attach.value = [...attach.value, mention(t.path)]), sep: true },
    { label: 'Copy path', run: () => copy(t.path, 'path'), sep: true },
    { label: 'Copy relative path', run: () => copy(relativePath(t.path, roots.value), 'relative path') }
  )
  if (desktop) {
    out.push({ label: 'Reveal in Finder', run: () => revealInFolder(t.path), sep: true })
    if (!t.dir) out.push({ label: 'Open in default app', run: () => openWithDefaultApp(t.path) })
  }
  out.push({ label: 'Open in terminal', run: () => openTerminal(t.dir ? t.path : dirName(t.path)), sep: !desktop })
  return out
})

async function runAction(a: Action) {
  menuRow.value = null
  try {
    await a.run()
  } catch (e) {
    toast.add({ title: `Could not ${a.label.toLowerCase()}`, description: message(e), color: 'error' })
  }
}

// --- Settings menu ---
const settingsOpen = ref(false)
const TAB_SIZES = [2, 4, 8]

const remoteText = computed(() => {
  const device = props.session.meta.deviceName || 'another machine'
  return `This session runs on ${device}. Its files can only be browsed on that machine.`
})
</script>

<template>
  <section
    ref="section"
    aria-label="Files"
    class="relative flex h-full flex-col overflow-hidden bg-default"
  >
    <div class="flex h-[44px] shrink-0 items-stretch border-b border-line pr-1.5">
      <div class="flex items-center px-3 font-display text-[13px] text-strong">
        Files
      </div>
      <div
        role="tablist"
        aria-label="Open files"
        class="flex min-w-0 items-stretch overflow-x-auto [scrollbar-width:none]"
      >
        <PaneTab
          v-for="t in vs.tabs"
          :key="t.path"
          :selected="t.path === vs.selected"
          :italic="!t.pinned"
          :title="t.path"
          class="gap-2 px-3"
          @select="selectTab(t.path)"
          @close="closeTab(t.path)"
          @dblclick="t.pinned = true"
          @contextmenu.prevent="onTabMenu(t.path, $event)"
        >
          {{ baseName(t.path) }}
        </PaneTab>
      </div>
      <span class="flex-1" />
      <div class="flex items-center gap-[2px]">
        <PaneBtn
          icon="refresh"
          label="Refresh"
          :disabled="remote"
          @click="refresh()"
        />
        <PaneBtn
          icon="list"
          label="Toggle file tree"
          :pressed="settings.tree"
          :aria-pressed="settings.tree"
          @click="settings.tree = !settings.tree"
        />
        <PaneBtn
          icon="more"
          label="Files settings"
          white
          :pressed="settingsOpen"
          data-menu-anchor
          @click="settingsOpen = !settingsOpen"
        />
        <PaneBtn
          icon="full"
          label="Maximize"
        />
        <PaneBtn
          icon="close"
          label="Close files"
          @click="closePane('files')"
        />
      </div>
    </div>

    <div
      v-if="remote"
      class="flex flex-1 flex-col items-center justify-center gap-2.5 p-5 text-center"
    >
      <HIcon
        name="monitor"
        :size="26"
        class="text-muted"
      />
      <span class="text-[13px] text-strong">Files are on another machine</span>
      <span class="max-w-[360px] text-[12px] text-muted">{{ remoteText }}</span>
    </div>

    <div
      v-else
      class="flex min-h-0 flex-1"
    >
      <div class="flex min-w-0 flex-1 flex-col">
        <div class="flex h-[38px] shrink-0 items-center gap-2 border-b border-line px-3.5 text-[12px] text-muted">
          <span
            class="min-w-0 flex-1 truncate"
            :title="vs.selected"
          >
            <template
              v-for="(c, i) in crumbs"
              :key="i"
            >
              <span
                v-if="i"
                class="px-1 text-dimmed"
              >/</span>
              <span :class="i === crumbs.length - 1 && 'text-toned'">{{ c }}</span>
            </template>
          </span>
          <button
            v-if="canToggleSource"
            type="button"
            :aria-label="source ? 'Show preview' : 'Show source'"
            :title="source ? 'Show preview' : 'Show source'"
            :aria-pressed="source"
            class="flex size-[26px] items-center justify-center border border-line text-strong"
            :class="source ? 'bg-raised' : 'hover:bg-hover'"
            @click="source = !source"
          >
            <HIcon
              name="code"
              :size="14"
            />
          </button>
          <button
            v-if="vs.selected"
            type="button"
            aria-label="File actions"
            title="Actions"
            data-menu-anchor
            class="flex size-[26px] items-center justify-center text-muted hover:text-strong"
            @click="onTabMenu(vs.selected, $event)"
          >
            <HIcon
              name="more"
              :size="14"
            />
          </button>
        </div>

        <FileViewer
          v-model:source="source"
          :path="vs.selected"
          :content="selectedContent"
          :loading="!!reading[vs.selected]"
          :error="readErrors[vs.selected] ?? ''"
          :wrap="settings.wrap"
          :tab-size="settings.tabSize"
          :lines="shownLines[vs.selected] ?? null"
          :scroll-request="scrollRequest"
          :desktop="desktop"
          @copy-path="copy(vs.selected, 'path')"
          @open-app="openWithDefaultApp(vs.selected)"
        />
      </div>

      <div
        v-if="settings.tree"
        class="flex min-h-0 w-[260px] shrink-0 flex-col border-l border-line"
      >
        <div class="border-b border-line p-2">
          <label class="flex h-[30px] items-center gap-2 border border-line bg-surface px-[9px] text-[12px] focus-within:border-accent">
            <HIcon
              :name="searching ? 'loading' : 'search'"
              :spin="searching"
              :size="13"
              class="text-muted"
            />
            <span class="sr-only">Filter files</span>
            <input
              v-model="query"
              type="text"
              spellcheck="false"
              placeholder="Go to file"
              class="min-w-0 flex-1 truncate bg-transparent text-strong outline-none placeholder:text-muted"
              @keydown.esc="query = ''"
              @keydown.enter="hits[0] && openHit(hits[0])"
            >
            <button
              type="button"
              :aria-label="settings.showIgnored ? 'Hide ignored files' : 'Show ignored files'"
              :title="settings.showIgnored ? 'Hide ignored files' : 'Show ignored files'"
              :aria-pressed="settings.showIgnored"
              class="flex size-5 shrink-0 items-center justify-center"
              :class="settings.showIgnored ? 'text-accent' : 'text-muted hover:text-strong'"
              @click.prevent="settings.showIgnored = !settings.showIgnored"
            >
              <HIcon
                name="filter"
                :size="13"
              />
            </button>
          </label>
        </div>

        <div
          v-if="rootsError && !roots.length"
          class="p-3 text-[12px] text-muted"
          role="alert"
        >
          {{ rootsError }}
        </div>

        <div
          v-else-if="query.trim()"
          role="listbox"
          aria-label="Matching files"
          class="min-h-0 flex-1 overflow-y-auto p-1 text-[12.5px]"
        >
          <div
            v-for="h in hits"
            :key="h.path"
            role="option"
            :aria-selected="h.path === vs.selected"
            :title="h.path"
            class="flex h-[26px] cursor-pointer items-center gap-[7px] overflow-hidden px-1.5 whitespace-nowrap text-toned hover:bg-hover"
            @click="openHit(h)"
          >
            <HIcon
              :name="h.dir ? 'folder' : 'file'"
              :size="13"
              class="text-muted"
            />
            <span class="shrink-0 text-strong">{{ baseName(h.rel) }}</span>
            <span class="truncate text-[11.5px] text-muted">{{ h.rel.includes('/') ? h.rel.slice(0, h.rel.lastIndexOf('/')) : '' }}</span>
          </div>
          <div
            v-if="!hits.length && !searching"
            class="px-2 py-1.5 text-[12px] text-muted"
          >
            No matching files
          </div>
        </div>

        <FileTree
          v-else
          ref="treeRef"
          :rows="rows"
          :selected="vs.selected"
          :menu-for="menuRow?.path ?? ''"
          @toggle="toggle"
          @open="onTreeOpen"
          @menu="onRowMenu"
        />
      </div>
    </div>

    <PaneMenu v-model:open="menuOpen">
      <div
        v-if="menuAt"
        role="menu"
        :aria-label="`Actions for ${menuRow?.name}`"
        class="absolute z-30 menu-panel w-[220px]"
        :style="{ left: `${menuAt.x}px`, top: `${menuAt.y}px` }"
      >
        <template
          v-for="a in actions"
          :key="a.label"
        >
          <div
            v-if="a.sep && a !== actions[0]"
            class="my-1 h-px bg-line"
          />
          <PaneMenuItem @click="runAction(a)">
            {{ a.label }}
          </PaneMenuItem>
        </template>
      </div>
    </PaneMenu>

    <PaneMenu v-model:open="settingsOpen">
      <div
        role="menu"
        aria-label="Files settings"
        class="absolute top-[46px] right-[74px] z-30 menu-panel w-[230px]"
      >
        <PaneMenuItem
          role="menuitemcheckbox"
          :checked="settings.tree"
          reserve
          @click="settings.tree = !settings.tree"
        >
          Show file tree
        </PaneMenuItem>
        <PaneMenuItem
          role="menuitemcheckbox"
          :checked="settings.showIgnored"
          reserve
          @click="settings.showIgnored = !settings.showIgnored"
        >
          Show ignored files
        </PaneMenuItem>
        <div class="my-1 h-px bg-line" />
        <PaneMenuItem
          role="menuitemcheckbox"
          :checked="settings.wrap"
          reserve
          @click="settings.wrap = !settings.wrap"
        >
          Word wrap
        </PaneMenuItem>
        <div class="my-1 h-px bg-line" />
        <div class="px-[11px] pt-1 pb-0.5 text-[11px] text-muted">
          Tab size
        </div>
        <PaneMenuItem
          v-for="n in TAB_SIZES"
          :key="n"
          role="menuitemradio"
          :checked="settings.tabSize === n"
          :active="settings.tabSize === n"
          reserve
          @click="settings.tabSize = n"
        >
          {{ n }}
        </PaneMenuItem>
      </div>
    </PaneMenu>
  </section>
</template>
