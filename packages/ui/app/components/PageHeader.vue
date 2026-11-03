<script setup lang="ts">
// Top bar of the pages beside sessions (Settings, Artifacts): drags the window in the desktop app and, with
// the sidebar collapsed (or on a narrow screen), brings it back, so a page never leaves you without it.
import { isDesktop } from '~/lib/desktop'

defineProps<{ title?: string }>()
const collapsed = useState('sidebar-collapsed', () => false)
const sidebarOpen = useState('sidebar-open', () => false)
const desktop = isDesktop()
</script>

<template>
  <header
    data-tauri-drag-region
    class="flex h-11 shrink-0 items-center gap-1.5 pr-4"
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
    <span
      v-if="title"
      data-tauri-drag-region
      class="text-[12.5px] text-muted"
    >{{ title }}</span>
  </header>
</template>
