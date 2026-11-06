<script setup lang="ts">
// The files pane's tree (lib/files/tree.ts buildRows): working directories at the top, folders that expand
// lazily, file-type icons, git letters on files and a dot on folders with changes inside. A click selects
// (a folder toggles), a double click opens a file for good, right click or "…" opens the row's actions.
import { Virtualizer } from 'virtua/vue'
import type { TreeRow } from '~/lib/files/tree'
import { fileKind } from '~/lib/files/tree'
import type { IconName } from '~/utils/icons'

const props = defineProps<{ rows: TreeRow[], selected: string, menuFor: string }>()
const emit = defineEmits<{
  toggle: [row: TreeRow]
  open: [row: TreeRow, pin: boolean]
  menu: [row: TreeRow, at: { x: number, y: number }]
}>()

const root = ref<HTMLElement | null>(null)
const list = ref<InstanceType<typeof Virtualizer> | null>(null)

function icon(r: TreeRow): { name: IconName, color: string } {
  if (r.kind === 'symlink') return { name: 'link', color: 'text-muted' }
  switch (fileKind(r.path, r.kind)) {
    case 'dir': return { name: 'folder', color: r.root ? 'text-toned' : 'text-muted' }
    case 'md': return { name: 'md', color: 'text-toned' }
    case 'code': return { name: 'code', color: 'text-cyan' }
    case 'image':
    case 'svg': return { name: 'image', color: 'text-pink' }
    default: return { name: 'file', color: 'text-muted' }
  }
}

const GIT_COLOR: Record<string, string> = { A: 'text-ok', M: 'text-warn', D: 'text-bad', U: 'text-ok', R: 'text-ok', C: 'text-bad' }
const GIT_LABEL: Record<string, string> = { A: 'Added', M: 'Modified', D: 'Deleted', U: 'Untracked', R: 'Renamed', C: 'Conflicted' }

function click(r: TreeRow) {
  if (r.kind === 'dir') emit('toggle', r)
  else emit('open', r, false)
}

function contextMenu(r: TreeRow, e: MouseEvent) {
  emit('menu', r, { x: e.clientX, y: e.clientY })
}

function moreMenu(r: TreeRow, e: MouseEvent) {
  const b = (e.currentTarget as HTMLElement).getBoundingClientRect()
  emit('menu', r, { x: b.right, y: b.bottom })
}

function onKey(e: KeyboardEvent, r: TreeRow) {
  if (e.key === 'Enter') {
    if (r.kind === 'dir') emit('toggle', r)
    else emit('open', r, true)
  } else if (e.key === 'ArrowRight' && r.kind === 'dir' && !r.expanded) {
    emit('toggle', r)
  } else if (e.key === 'ArrowLeft' && r.kind === 'dir' && r.expanded) {
    emit('toggle', r)
  } else if (e.key === 'ArrowDown' || e.key === 'ArrowUp') {
    e.preventDefault()
    const items = [...(root.value?.querySelectorAll<HTMLElement>('[role="treeitem"]') ?? [])]
    const i = items.indexOf(e.currentTarget as HTMLElement)
    items[i + (e.key === 'ArrowDown' ? 1 : -1)]?.focus()
  }
}

// Scrolls a row into view (the pane reveals a file opened from elsewhere).
function reveal(path: string) {
  const at = props.rows.findIndex(r => r.path === path)
  if (at >= 0) list.value?.scrollToIndex(at, { align: 'nearest' })
  nextTick(() => root.value?.querySelector(`[data-path="${CSS.escape(path)}"]`)?.scrollIntoView({ block: 'nearest' }))
}

defineExpose({ reveal })
watch(() => props.selected, p => p && reveal(p))
</script>

<template>
  <div
    ref="root"
    role="tree"
    aria-label="Project files"
    class="min-h-0 flex-1 overflow-y-auto p-1 text-[12.5px]"
  >
    <Virtualizer
      ref="list"
      v-slot="{ item: r }"
      :data="rows"
      :item-size="26"
      :start-margin="4"
    >
      <div
        :key="r.path"
        role="treeitem"
        tabindex="0"
        :data-path="r.path"
        :aria-selected="r.path === selected"
        :aria-expanded="r.kind === 'dir' ? r.expanded : undefined"
        :title="r.path"
        class="group flex h-[26px] cursor-pointer items-center gap-[7px] overflow-hidden border-l-2 pr-1 whitespace-nowrap outline-none focus-visible:bg-hover"
        :class="[
          r.path === selected || r.path === menuFor ? 'border-accent bg-raised text-strong' : 'border-transparent text-toned hover:bg-hover',
          (r.ignored || r.deleted) && 'opacity-60'
        ]"
        :style="{ paddingLeft: `${6 + r.depth * 14}px` }"
        @click="click(r)"
        @dblclick="r.kind !== 'dir' && emit('open', r, true)"
        @contextmenu.prevent="contextMenu(r, $event)"
        @keydown="onKey($event, r)"
      >
        <HIcon
          v-if="r.kind === 'dir'"
          :name="r.loading ? 'loading' : r.expanded ? 'down' : 'right'"
          :spin="r.loading"
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
        <span
          class="truncate"
          :class="[r.root && 'text-strong', r.deleted && 'line-through']"
        >{{ r.name }}</span>
        <span class="flex-1" />
        <button
          type="button"
          aria-label="File actions"
          title="Actions"
          class="flex size-5 shrink-0 items-center justify-center text-muted hover:text-strong"
          :class="r.path === menuFor ? 'visible' : 'invisible group-hover:visible group-focus-visible:visible'"
          @click.stop="moreMenu(r, $event)"
          @dblclick.stop
        >
          <HIcon
            name="more"
            :size="13"
          />
        </button>
        <span
          v-if="r.git"
          class="w-3 shrink-0 text-center text-[10.5px]"
          :class="GIT_COLOR[r.git]"
          :title="GIT_LABEL[r.git]"
        >{{ r.git }}</span>
        <span
          v-else-if="r.changed"
          class="flex w-3 shrink-0 justify-center"
          title="Contains changes"
        ><span class="size-[5px] bg-warn" /></span>
        <span
          v-else
          class="w-3 shrink-0"
        />
      </div>
    </Virtualizer>
  </div>
</template>
