<script setup lang="ts">
// Files pane (docs/design/ui/Files.dc.html), on demo fixtures. Opens on evals/run/client.go as a
// preview tab (the design's default); the tree selects files and toggles folders, `?` in the filter
// searches inside files. URL `menu` values: settings (menu + tab-size submenu, on the markdown file),
// markdown, folder, search.
import { usePanes } from '~/composables/usePanes'
import type { SessionState } from '~/lib/session/types'
import {
  BASELINE, BASELINE_SOURCE, CODE_CURRENT_LINE, CODE_FILE, CODE_LANG, CODE_LINES, FILE_TREE, FILES_MENU,
  FILES_SETTINGS_DEFAULTS, FOLDER_INFO, MD_FILE, OPEN_DIRS, SEARCH_HITS, SEARCH_QUERY, SEARCH_SUMMARY, TAB_SIZES,
  type FileNode
} from '~/lib/fixtures/files'
import type { IconName } from '~/utils/icons'

const props = defineProps<{ session: SessionState, menu: string }>()

const { openPane, closePane } = usePanes()

const initial = props.menu === 'settings' || props.menu === 'markdown' ? MD_FILE : props.menu === 'folder' ? 'evals/results' : CODE_FILE
const selectedPath = ref(initial)
const openDirs = ref<string[]>(props.menu === 'folder' ? ['docs', 'docs/evals', 'evals'] : [...OPEN_DIRS])
// Files opened for good (a double click); the others are italic preview tabs.
const pinned = ref<string[]>([MD_FILE])
const query = ref(props.menu === 'search' ? SEARCH_QUERY : '')
const searching = computed(() => query.value.startsWith('?'))
const showSource = ref(false)
const currentLine = ref(CODE_CURRENT_LINE)

const menuOpen = ref(props.menu === 'settings')
const tabSizeMenu = ref(props.menu === 'settings')
watch(() => props.menu, (m) => {
  menuOpen.value = m === 'settings'
  tabSizeMenu.value = m === 'settings'
})
watch(menuOpen, (v) => {
  if (!v) tabSizeMenu.value = false
})

const settings = reactive<Record<string, boolean>>({ ...FILES_SETTINGS_DEFAULTS, tree: true })
const tabSize = ref(4)

// Files opened from outside the tree (Show, "Open file" in the diff) that the demo tree does not have.
const outside = ref<FileNode[]>([])
const selected = computed(() => FILE_TREE.find(n => n.path === selectedPath.value) ?? outside.value.find(n => n.path === selectedPath.value) ?? null)
const view = computed(() => {
  const n = selected.value
  if (!n) return 'none'
  if (n.kind === 'dir') return 'folder'
  if (n.path === CODE_FILE) return 'code'
  if (n.path === MD_FILE) return 'md'
  return 'other'
})
const crumbs = computed(() => selectedPath.value.split('/').join(' / '))

function name(path: string) {
  return path.slice(path.lastIndexOf('/') + 1)
}

function depth(path: string) {
  return path.split('/').length - 1
}

const rows = computed(() => {
  const q = query.value.trim().toLowerCase()
  if (q && !searching.value) return FILE_TREE.filter(n => name(n.path).toLowerCase().includes(q))
  return FILE_TREE.filter((n) => {
    const parts = n.path.split('/')
    for (let i = 1; i < parts.length; i++) {
      if (!openDirs.value.includes(parts.slice(0, i).join('/'))) return false
    }
    return true
  })
})

const folderInfo = computed(() => {
  const p = selectedPath.value
  if (FOLDER_INFO[p]) return FOLDER_INFO[p]
  const n = FILE_TREE.filter(f => f.path.startsWith(`${p}/`) && depth(f.path) === depth(p) + 1).length
  return `${n} ${n === 1 ? 'item' : 'items'}`
})

function icon(n: FileNode): { name: IconName, color: string } {
  if (n.kind === 'dir') return { name: 'folder', color: 'text-muted' }
  if (n.kind === 'go') return { name: 'code', color: 'text-cyan' }
  if (n.kind === 'md') return { name: 'md', color: 'text-toned' }
  return { name: 'file', color: 'text-muted' }
}

function gitColor(git: string) {
  return git === 'M' ? 'text-warn' : git === 'A' ? 'text-ok' : 'text-muted'
}

function select(n: FileNode) {
  selectedPath.value = n.path
  showSource.value = false
  if (n.kind !== 'dir') return
  const i = openDirs.value.indexOf(n.path)
  if (i >= 0) openDirs.value.splice(i, 1)
  else openDirs.value.push(n.path)
}

function pin(n: FileNode) {
  if (n.kind !== 'dir' && !pinned.value.includes(n.path)) pinned.value.push(n.path)
}

function openHit(path: string) {
  if (FILE_TREE.some(n => n.path === path)) selectedPath.value = path
}

// Lines highlighted by Show (1-based, inclusive); cleared when another file is picked.
const shownLines = ref<{ start: number, end: number } | null>(null)
watch(selectedPath, () => shownLines.value = null)
const codeView = ref<HTMLElement | null>(null)

// Opens a file given by another pane or by Show: the tree's node when it has it (absolute paths match by
// suffix), else a tab of its own, relative to the session's directory.
function openPath(p: string) {
  const hit = FILE_TREE.find(n => n.kind !== 'dir' && (n.path === p || p.endsWith(`/${n.path}`)))
  if (hit) {
    selectedPath.value = hit.path
    return
  }
  const cwd = props.session.meta.cwd.replace(/\/+$/, '')
  const rel = cwd && p.startsWith(`${cwd}/`) ? p.slice(cwd.length + 1) : p
  if (!outside.value.some(n => n.path === rel)) outside.value.push({ path: rel, kind: 'file', git: '' })
  selectedPath.value = rel
}

// "Open file" from other panes (diff).
const filesOpen = useState<string>('files-open-path', () => '')
watch(filesOpen, (p) => {
  if (!p) return
  openPath(p)
  filesOpen.value = ''
}, { immediate: true })

// Show: open the file and scroll its lines into view, highlighted.
const { target: showTarget } = useShow()
watch(showTarget, async (t) => {
  if (t?.kind !== 'file') return
  showTarget.value = null
  openPath(t.path)
  if (!t.lineStart) return
  await nextTick()
  shownLines.value = { start: t.lineStart, end: Math.max(t.lineEnd, t.lineStart) }
  currentLine.value = t.lineStart
  await nextTick()
  codeView.value?.querySelector(`[data-line="${t.lineStart}"]`)?.scrollIntoView({ block: 'center' })
}, { immediate: true })

const isShown = (n: number) => !!shownLines.value && n >= shownLines.value.start && n <= shownLines.value.end

function toggleSetting(key: string) {
  settings[key] = !settings[key]
}
</script>

<template>
  <section
    aria-label="Files"
    class="relative flex h-full flex-col overflow-hidden bg-default"
  >
    <div class="flex h-[44px] shrink-0 items-stretch border-b border-line pr-1.5">
      <div
        role="tablist"
        aria-label="Open files"
        class="flex min-w-0 items-stretch overflow-hidden"
      >
        <PaneTab
          v-if="selected"
          selected
          :italic="settings.previewTabs && !pinned.includes(selectedPath)"
          class="gap-2 px-3"
          @close="selectedPath = ''"
          @dblclick="pin(selected)"
        >
          {{ name(selectedPath) }}
        </PaneTab>
      </div>
      <span class="flex-1" />
      <div class="flex items-center gap-[2px]">
        <PaneBtn
          icon="search"
          label="Search in files"
          :class="searching && 'bg-raised'"
          @click="query = searching ? '' : SEARCH_QUERY"
        />
        <PaneBtn
          icon="list"
          label="Toggle file tree"
          :pressed="settings.tree"
          :aria-pressed="settings.tree"
          @click="toggleSetting('tree')"
        />
        <PaneBtn
          icon="more"
          label="Files settings"
          white
          :pressed="menuOpen"
          data-menu-anchor
          @click="menuOpen = !menuOpen"
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

    <div class="flex min-h-0 flex-1">
      <div class="flex min-w-0 flex-1 flex-col">
        <div class="flex h-[38px] shrink-0 items-center gap-2 border-b border-line px-3.5 text-[12px] text-muted">
          <span class="min-w-0 flex-1 truncate">{{ crumbs }}</span>
          <button
            v-if="view === 'md'"
            type="button"
            aria-label="Show source"
            title="Show source"
            :aria-pressed="showSource"
            class="flex size-[26px] items-center justify-center border border-line text-strong"
            :class="showSource ? 'bg-raised' : 'hover:bg-hover'"
            @click="showSource = !showSource"
          >
            <HIcon
              name="code"
              :size="14"
            />
          </button>
          <span
            v-if="view === 'code'"
            class="text-[11px]"
          >{{ CODE_LANG }}</span>
        </div>

        <div
          v-if="view === 'folder'"
          class="flex flex-1 flex-col items-center justify-center gap-2.5 p-5 text-center"
        >
          <HIcon
            name="folder"
            :size="26"
            class="text-muted"
          />
          <span class="text-[13px] text-strong">{{ selectedPath }}</span><span class="text-[12px] text-muted">{{ folderInfo }}</span>
          <div class="flex flex-wrap justify-center gap-1.5 pt-1.5">
            <button
              type="button"
              class="border border-line bg-hover px-2.5 py-[5px] text-[12px] text-strong hover:bg-raised"
            >
              Attach as context
            </button>
            <button
              type="button"
              class="border border-line bg-hover px-2.5 py-[5px] text-[12px] text-strong hover:bg-raised"
              @click="openPane('terminal')"
            >
              Open in terminal
            </button>
            <button
              type="button"
              class="border border-line bg-hover px-2.5 py-[5px] text-[12px] text-strong hover:bg-raised"
            >
              Show in Finder
            </button>
            <button
              type="button"
              aria-label="More actions"
              title="More actions"
              class="flex w-[30px] items-center justify-center text-muted hover:text-strong"
            >
              <HIcon name="more" />
            </button>
          </div>
        </div>

        <div
          v-else-if="view === 'code'"
          ref="codeView"
          class="min-h-0 flex-1 overflow-auto py-2 text-[12px] leading-[1.65]"
        >
          <div
            v-for="(l, i) in CODE_LINES"
            :key="i"
            class="flex"
            :class="isShown(i + 1) ? 'bg-accent/15' : i + 1 === currentLine && 'bg-hover'"
            :data-line="i + 1"
            @click="currentLine = i + 1"
          >
            <span
              class="w-[38px] shrink-0 pr-3 text-right"
              :class="i + 1 === currentLine ? 'text-strong' : 'text-dimmed'"
            >{{ i + 1 }}</span>
            <span
              class="min-w-0 flex-1 pr-2.5"
              :class="settings.wrap ? 'font-mono break-words whitespace-pre-wrap' : 'font-mono whitespace-pre'"
              :style="{ tabSize }"
            ><span
              v-for="(s, j) in l"
              :key="j"
              :style="{ color: s.c }"
            >{{ s.t }}</span></span>
          </div>
        </div>

        <pre
          v-else-if="view === 'md' && showSource"
          class="m-0 min-h-0 flex-1 overflow-auto px-3.5 py-2 font-mono text-[12px] leading-[1.65] whitespace-pre-wrap text-toned"
        >{{ BASELINE_SOURCE }}</pre>

        <div
          v-else-if="view === 'md'"
          class="flex min-h-0 flex-1 flex-col gap-3 overflow-auto px-[18px] py-4 text-[12.5px] leading-[1.6]"
        >
          <h1 class="m-0 font-display text-[20px] leading-[1.3] text-strong">
            {{ BASELINE.title }}
          </h1>
          <p class="m-0">
            {{ BASELINE.intro }}
          </p>
          <div class="relative overflow-x-auto border border-line">
            <table class="w-full border-collapse text-[12px] whitespace-nowrap">
              <thead>
                <tr class="bg-surface text-strong">
                  <th class="border-b border-line px-2.5 py-1.5 text-left font-normal">
                    Model
                  </th>
                  <th class="border-b border-line px-2.5 py-1.5 text-right font-normal">
                    Pass rate
                  </th>
                  <th class="border-b border-line px-2.5 py-1.5 text-right font-normal">
                    Passed
                  </th>
                  <th class="border-b border-line px-2.5 py-1.5 text-right font-normal">
                    Timeouts
                  </th>
                </tr>
              </thead>
              <tbody>
                <tr
                  v-for="r in BASELINE.results"
                  :key="r.model"
                >
                  <td class="border-b border-muted px-2.5 py-1.5">
                    {{ r.model }}
                  </td>
                  <td
                    class="border-b border-muted px-2.5 py-1.5 text-right"
                    :style="{ color: r.color }"
                  >
                    {{ r.rate }}
                  </td>
                  <td class="border-b border-muted px-2.5 py-1.5 text-right">
                    {{ r.passed }}
                  </td>
                  <td class="border-b border-muted px-2.5 py-1.5 text-right">
                    0
                  </td>
                </tr>
              </tbody>
            </table>
          </div>
          <h2 class="mt-1 mb-0 font-display text-[16px] text-strong">
            Failures
          </h2>
          <div class="overflow-x-auto border border-line">
            <table class="w-full border-collapse text-[12px]">
              <thead>
                <tr class="bg-surface text-strong">
                  <th class="border-b border-line px-2.5 py-1.5 text-left font-normal">
                    Model
                  </th>
                  <th class="border-b border-line px-2.5 py-1.5 text-left font-normal">
                    Task
                  </th>
                  <th class="border-b border-line px-2.5 py-1.5 text-left font-normal">
                    Status
                  </th>
                  <th class="border-b border-line px-2.5 py-1.5 text-right font-normal">
                    Turns
                  </th>
                </tr>
              </thead>
              <tbody>
                <tr
                  v-for="f in BASELINE.failures"
                  :key="f.task"
                >
                  <td class="px-2.5 py-1.5">
                    {{ f.model }}
                  </td>
                  <td class="px-2.5 py-1.5">
                    {{ f.task }}
                  </td>
                  <td class="px-2.5 py-1.5 text-bad">
                    {{ f.status }}
                  </td>
                  <td class="px-2.5 py-1.5 text-right">
                    {{ f.turns }}
                  </td>
                </tr>
              </tbody>
            </table>
          </div>
        </div>

        <div
          v-else
          class="flex flex-1 flex-col items-center justify-center gap-2.5 p-5 text-center"
        >
          <HIcon
            :name="selected ? 'file' : 'folder'"
            :size="26"
            class="text-muted"
          />
          <span class="text-[13px] text-strong">{{ selected ? name(selectedPath) : 'No file open' }}</span>
          <span class="text-[12px] text-muted">{{ selected ? 'No preview for this file' : 'Pick a file in the tree' }}</span>
        </div>
      </div>

      <div
        v-if="settings.tree"
        class="flex min-h-0 w-[236px] shrink-0 flex-col border-l border-line"
      >
        <div class="border-b border-line p-2">
          <label
            class="flex h-[30px] items-center gap-2 border bg-surface px-[9px] text-[12px] focus-within:border-accent"
            :class="searching ? 'border-accent' : 'border-line'"
          >
            <HIcon
              name="search"
              :size="13"
              class="text-muted"
            />
            <span class="sr-only">Filter files</span>
            <input
              v-model="query"
              type="text"
              spellcheck="false"
              placeholder="Filter, or ? to search in files"
              class="min-w-0 flex-1 truncate bg-transparent text-strong outline-none placeholder:text-muted"
              @keydown.esc="query = ''"
            >
          </label>
        </div>

        <div
          v-if="!searching"
          role="tree"
          aria-label="Project files"
          class="min-h-0 flex-1 overflow-y-auto p-1 text-[12.5px]"
        >
          <div
            v-for="r in rows"
            :key="r.path"
            role="treeitem"
            :aria-selected="r.path === selectedPath"
            :aria-expanded="r.kind === 'dir' ? openDirs.includes(r.path) : undefined"
            class="flex h-[26px] cursor-pointer items-center gap-[7px] overflow-hidden border-l-2 pr-2 whitespace-nowrap"
            :class="r.path === selectedPath ? 'border-accent bg-raised text-strong' : 'border-transparent text-toned hover:bg-hover'"
            :style="{ paddingLeft: `${6 + depth(r.path) * 14}px` }"
            @click="select(r)"
            @dblclick="pin(r)"
          >
            <HIcon
              v-if="r.kind === 'dir'"
              :name="openDirs.includes(r.path) ? 'down' : 'right'"
              :size="12"
              class="text-muted"
            />
            <span
              v-else
              class="size-3 shrink-0"
            />
            <HIcon
              :name="icon(r).name"
              :size="14"
              :class="icon(r).color"
            />
            <span class="truncate">{{ name(r.path) }}</span>
            <span class="flex-1" /><span
              class="text-[10.5px]"
              :class="gitColor(r.git)"
            >{{ r.git }}</span>
          </div>
        </div>

        <div
          v-else
          class="flex min-h-0 flex-1 flex-col gap-[2px] overflow-y-auto px-2 py-1.5 text-[12px]"
        >
          <span class="pb-1 text-[11px] text-muted">{{ SEARCH_SUMMARY }}</span>
          <div
            v-for="(h, i) in SEARCH_HITS"
            :key="i"
            class="flex cursor-pointer gap-2 overflow-hidden py-[3px] pr-1 whitespace-nowrap hover:bg-hover"
            :class="h.file ? 'pl-[2px] text-strong' : 'pl-3 text-toned'"
            @click="h.file && openHit(h.before)"
          >
            <span class="text-[11px] text-dimmed">{{ h.line }}</span><span class="truncate">{{ h.before }}<span
              v-if="h.match"
              class="bg-accent text-on-accent"
            >{{ h.match }}</span>{{ h.after }}</span>
          </div>
        </div>
      </div>
    </div>

    <PaneMenu v-model:open="menuOpen">
      <div
        v-if="tabSizeMenu"
        role="menu"
        aria-label="Tab size"
        class="absolute top-[368px] right-[324px] z-30 menu-panel w-[110px]"
      >
        <PaneMenuItem
          v-for="n in TAB_SIZES"
          :key="n"
          role="menuitemradio"
          gap="gap-0"
          :checked="tabSize === n"
          :active="tabSize === n"
          @click="tabSize = n; menuOpen = false"
        >
          {{ n }}
        </PaneMenuItem>
      </div>
      <div
        role="menu"
        aria-label="Files settings"
        class="absolute top-[46px] right-[74px] z-30 menu-panel w-[250px]"
      >
        <template
          v-for="(m, i) in FILES_MENU"
          :key="i"
        >
          <div
            v-if="m === 'sep'"
            class="my-1 h-px bg-line"
          />
          <PaneMenuItem
            v-else-if="m.key === 'tabSize'"
            :value="String(tabSize)"
            value-class="text-[11.5px] text-muted"
            sub
            :sub-size="14"
            :highlight="tabSizeMenu"
            @click="tabSizeMenu = !tabSizeMenu"
            @mouseenter="tabSizeMenu = true"
          >
            {{ m.label }}
          </PaneMenuItem>
          <PaneMenuItem
            v-else
            :role="m.check ? 'menuitemcheckbox' : 'menuitem'"
            :value="m.value"
            :kbd="m.kbd"
            value-class="text-[11.5px] text-muted"
            :sub="m.sub"
            :sub-size="14"
            :checked="m.check && settings[m.key]"
            reserve
            @click="m.check && toggleSetting(m.key)"
            @mouseenter="tabSizeMenu = false"
          >
            {{ m.label }}
          </PaneMenuItem>
        </template>
      </div>
    </PaneMenu>
  </section>
</template>
