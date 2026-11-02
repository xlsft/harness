<script setup lang="ts">
// Right-side pane container: one resizable panel per pane kind (each remembers its own width).
import type { SessionState } from '~/lib/session/types'
import { PANE_WIDTH, type PaneName } from '~/composables/usePanes'

const props = defineProps<{ pane: PaneName, session: SessionState }>()
const { menu } = usePanes()

const component = computed(() => ({
  details: resolveComponent('DetailsPane'),
  terminal: resolveComponent('TerminalPane'),
  diff: resolveComponent('DiffPane'),
  browser: resolveComponent('BrowserPane'),
  files: resolveComponent('FilesPane'),
  artifact: resolveComponent('ArtifactPane'),
  sidechat: resolveComponent('SideChatPane'),
  tasks: resolveComponent('TasksPane'),
  agents: resolveComponent('AgentsPane')
}[props.pane]))
</script>

<template>
  <UDashboardPanel
    :id="`pane-${pane}`"
    :key="pane"
    resizable
    :default-size="PANE_WIDTH[pane]"
    :min-size="pane === 'details' ? 250 : 360"
    :max-size="pane === 'details' ? 292 : 960"
    :ui="{
      root: 'h-svh min-h-0 border-s border-default bg-default max-lg:fixed max-lg:inset-0 max-lg:z-40 max-lg:border-s-0',
      body: 'p-0 sm:p-0 gap-0 overflow-hidden'
    }"
  >
    <template #body>
      <component
        :is="component"
        :session="session"
        :menu="menu"
      />
    </template>
  </UDashboardPanel>
</template>
