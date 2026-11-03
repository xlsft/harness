<script setup lang="ts">
// Session header (Main.dc.html): title, pane toggles and the tools menu (HeaderMenu.dc.html).
import { isDesktop } from '~/lib/desktop'
import type { SessionState } from '~/lib/session/types'
import type { IconName } from '~/utils/icons'
import type { PaneName } from '~/composables/usePanes'

defineProps<{ session: SessionState }>()

const { isOpen, menu, togglePane } = usePanes()
const collapsed = useState('sidebar-collapsed', () => false)
// In the desktop app the header drags the window and, with the sidebar collapsed, makes room for the window buttons.
const desktop = isDesktop()
const sidebarOpen = useState('sidebar-open', () => false)
const toolsOpen = ref(menu.value === 'tools')

const buttons: { pane: PaneName, icon: IconName, label: string, kbd?: string }[] = [
  { pane: 'details', icon: 'tasks', label: 'Tasks' },
  { pane: 'terminal', icon: 'terminal', label: 'Terminal', kbd: 'meta+j' },
  { pane: 'diff', icon: 'diff', label: 'Changes' },
  { pane: 'browser', icon: 'globe', label: 'Browser' },
  { pane: 'files', icon: 'folder', label: 'Files' }
]
</script>

<template>
  <header
    data-tauri-drag-region
    class="relative flex h-11 shrink-0 flex-nowrap items-center gap-1.5 border-b border-default pr-4"
    :class="desktop ? (collapsed ? 'pl-[92px]' : 'pl-4 max-lg:pl-[92px]') : 'pl-4'"
  >
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
    <h1
      data-tauri-drag-region
      class="m-0 min-w-0 truncate font-display text-[13.5px] font-semibold text-strong"
    >
      {{ session.meta.title }}
    </h1>
    <HIcon
      name="down"
      class="shrink-0 text-muted"
    />
    <span
      data-tauri-drag-region
      class="flex-1 self-stretch"
    />
    <HIconBtn
      v-for="b in buttons"
      :key="b.pane"
      :icon="b.icon"
      :label="b.label"
      :kbd="b.kbd"
      :active="isOpen(b.pane)"
      :class="b.pane !== 'details' && b.pane !== 'terminal' ? 'max-sm:hidden @max-[640px]:hidden' : ''"
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
        :class="toolsOpen ? 'border-default bg-raised text-strong' : 'border-transparent text-muted hover:text-highlighted'"
      >
        <HIcon name="more" />
      </button>
      <template #content>
        <ToolsMenu @select="toolsOpen = false" />
      </template>
    </UPopover>
  </header>
</template>
