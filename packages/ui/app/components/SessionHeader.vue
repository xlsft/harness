<script setup lang="ts">
// Session header (Main.dc.html): title, pane toggles and the tools menu (HeaderMenu.dc.html).
import type { SessionState } from '~/lib/session/types'
import type { IconName } from '~/utils/icons'
import type { PaneName } from '~/composables/usePanes'

defineProps<{ session: SessionState }>()

const { pane, menu, togglePane } = usePanes()
const collapsed = useState('sidebar-collapsed', () => false)
const sidebarOpen = useState('sidebar-open', () => false)
const toolsOpen = ref(menu.value === 'tools')

const buttons: { pane: PaneName, icon: IconName, label: string, kbd?: string }[] = [
  { pane: 'details', icon: 'tasks', label: 'Tasks' },
  { pane: 'terminal', icon: 'terminal', label: 'Terminal', kbd: 'meta+j' },
  { pane: 'diff', icon: 'diff', label: 'Diff' },
  { pane: 'browser', icon: 'globe', label: 'Browser' },
  { pane: 'files', icon: 'folder', label: 'Files' }
]
</script>

<template>
  <header class="relative flex h-11 shrink-0 flex-wrap items-center gap-1.5 border-b border-default px-4">
    <HIconBtn
      v-if="collapsed"
      icon="sidebar"
      label="Show sidebar"
      class="-ml-2"
      @click="collapsed = false"
    />
    <HIconBtn
      icon="sidebar"
      label="Sessions"
      class="-ml-2 lg:hidden"
      @click="sidebarOpen = true"
    />
    <h1 class="m-0 min-w-0 truncate font-display text-[13.5px] font-semibold text-white">
      {{ session.meta.title }}
    </h1>
    <HIcon
      name="down"
      class="text-muted"
    />
    <span class="flex-1" />
    <HIconBtn
      v-for="b in buttons"
      :key="b.pane"
      :icon="b.icon"
      :label="b.label"
      :kbd="b.kbd"
      :active="pane === b.pane"
      :class="b.pane !== 'details' && b.pane !== 'terminal' ? 'max-sm:hidden' : ''"
      @click="togglePane(b.pane)"
    />
    <UPopover
      v-model:open="toolsOpen"
      :content="{ align: 'end', side: 'bottom', sideOffset: 4, alignOffset: -4 }"
    >
      <button
        type="button"
        aria-label="More tools"
        aria-haspopup="menu"
        :aria-expanded="toolsOpen"
        class="flex size-7 items-center justify-center border"
        :class="toolsOpen ? 'border-default bg-raised text-white' : 'border-transparent text-muted hover:text-highlighted'"
      >
        <HIcon name="more" />
      </button>
      <template #content>
        <ToolsMenu @select="toolsOpen = false" />
      </template>
    </UPopover>
  </header>
</template>
