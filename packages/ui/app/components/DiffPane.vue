<script setup lang="ts">
// Diff pane (header "Diff" button). Not drawn as its own artboard: built from Diff.dc.html —
// the turn summary on top, then every edit of the session as a unified / split diff, edge to edge.
// Review comments: hover a line, add a comment; "Send to agent" posts them all as one message.
import type { SessionState, ToolItem } from '~/lib/session/types'
import type { DiffComment } from '~/components/transcript/DiffView.vue'

const props = defineProps<{ session: SessionState }>()
const { closePane } = usePanes()
const store = useSessionStore()

// Comments survive closing and reopening the pane within the session (not a reload).
const comments = useState<DiffComment[]>(`diff-comments-${props.session.meta.id}`, () => [])
const sending = ref(false)

function reviewMessage(list: DiffComment[]) {
  const lines = ['Review comments on your changes. Address each one:', '']
  for (const c of [...list].sort((a, b) => a.path.localeCompare(b.path) || a.line - b.line)) {
    lines.push(`- ${c.path}:${c.line}${c.side === 'old' ? ' (removed line)' : ''}`)
    if (c.code.trim()) lines.push(`  \`${c.code.trim()}\``)
    lines.push(...c.text.split('\n').map(l => `  ${l}`))
  }
  return lines.join('\n')
}

async function sendComments() {
  if (!comments.value.length || sending.value) return
  sending.value = true
  try {
    await store.send(props.session.meta.id, reviewMessage(comments.value))
    comments.value = []
  } finally {
    sending.value = false
  }
}

const edits = computed(() => props.session.items.filter((i): i is ToolItem => i.kind === 'tool' && ['Edit', 'MultiEdit', 'Write'].includes(i.name) && i.status === 'ok' && patchOf(i).length > 0))
const files = computed(() => fileChanges(edits.value))

// One continuous diff: the hunks of every edit, grouped by file in first-edit order.
const groups = computed(() => {
  const map = new Map<string, { path: string, label: string, hunks: ReturnType<typeof patchOf> }>()
  for (const t of edits.value) {
    const path = String(t.input.file_path ?? '')
    if (!map.has(path)) map.set(path, { path, label: toolTarget(t), hunks: [] })
    map.get(path)!.hunks.push(...patchOf(t))
  }
  return [...map.values()].map(g => ({ ...g, stats: patchStats(g.hunks) }))
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

const kindOf = (path: string) => files.value.find(f => f.path === path)?.kind ?? 'M'

// Per-file state for this session: collapsed, and viewed (which also collapses it).
const collapsed = useState<Record<string, boolean>>(`diff-collapsed-${props.session.meta.id}`, () => ({}))
const viewed = useState<Record<string, boolean>>(`diff-viewed-${props.session.meta.id}`, () => ({}))
const fileMenu = ref('')
const copyMenu = ref(false)
const { openPane } = usePanes()
const filesOpen = useState<string>('files-open-path', () => '')
const attach = useState<string[]>('composer-attach', () => [])

const relative = (path: string) => {
  const cwd = props.session.meta.cwd.replace(/\/+$/, '')
  return cwd && path.startsWith(`${cwd}/`) ? path.slice(cwd.length + 1) : path
}

function toggleViewed(path: string) {
  viewed.value[path] = !viewed.value[path]
  collapsed.value[path] = viewed.value[path]!
}

function openFile(path: string) {
  filesOpen.value = path
  openPane('files')
  fileMenu.value = ''
}

function diffText(g: { path: string, hunks: ReturnType<typeof patchOf> }) {
  const out = [`--- a/${relative(g.path)}`, `+++ b/${relative(g.path)}`]
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
    aria-label="Diff"
    class="flex h-full flex-col bg-default"
  >
    <div class="flex h-11 shrink-0 items-center gap-0.5 border-b border-default pr-1.5 pl-3.5">
      <span class="font-display text-[13px] text-white">Diff</span>
      <span class="flex-1" />
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
      v-if="!edits.length"
      class="flex flex-1 flex-col items-center justify-center gap-2 p-6 text-center"
    >
      <HIcon
        name="diff"
        :size="24"
        class="text-muted"
      />
      <span class="text-white">No changes yet</span>
      <span class="max-w-80 text-[12px] leading-[1.55] text-muted">Edits the agent makes in this session show up here for review.</span>
    </div>
    <div
      v-else
      class="flex min-h-0 flex-1 flex-col overflow-y-auto"
    >
      <DiffSummary
        :files="files"
        flush
        total-only
      />
      <section
        v-for="g in groups"
        :id="`diff-${g.path}`"
        :key="g.path"
        class="border-b border-default"
      >
        <div
          :title="g.path"
          class="sticky top-0 z-10 flex cursor-default items-center gap-2 border-b border-default bg-surface py-[3px] pr-1.5 pl-2 text-[12px] text-toned"
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
            :class="kindOf(g.path) === 'A' ? 'text-ok' : kindOf(g.path) === 'D' ? 'text-bad' : 'text-warn'"
          >{{ kindOf(g.path) }}</span>
          <span
            class="min-w-0 flex-1 truncate"
            :class="viewed[g.path] && 'text-muted'"
          >{{ relative(g.label) }}</span>
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
                    sub
                    disabled
                  >
                    Open in
                  </PaneMenuItem>
                  <PaneMenuItem
                    disabled
                    value="desktop"
                  >
                    Show in Finder
                  </PaneMenuItem>
                </div>
              </template>
            </UPopover>
          </div>
        </div>
        <DiffView
          v-show="!collapsed[g.path]"
          v-model:comments="comments"
          :path="g.label"
          :hunks="g.hunks"
          :mode="view"
          bare
          commentable
        />
      </section>
    </div>
    <div
      v-if="comments.length"
      class="flex shrink-0 items-center gap-2 border-t border-default bg-surface px-3 py-2 text-[12px]"
    >
      <HIcon
        name="comment"
        :size="13"
        class="text-accent"
      />
      <span class="text-white">{{ comments.length }} comment{{ comments.length === 1 ? '' : 's' }}</span>
      <span class="flex-1" />
      <button
        type="button"
        class="border border-default px-2 py-0.5 text-toned hover:text-white"
        @click="comments = []"
      >
        Discard
      </button>
      <button
        type="button"
        :disabled="sending"
        class="bg-accent px-2.5 py-0.5 text-white disabled:opacity-50"
        @click="sendComments"
      >
        {{ store.isBusy(session.meta.id) ? 'Queue for agent' : 'Send to agent' }}
      </button>
    </div>
  </section>
</template>
