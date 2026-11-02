<script setup lang="ts">
// One right pane inside the pane layout. Its 44px header is a drag handle (pointer events): drop it on another pane's edge
// to place it beside (left / right) or under / above (top / bottom) that pane, or on its middle to swap.
import type { SessionState } from '~/lib/session/types'
import type { PaneName } from '~/composables/usePanes'

const props = defineProps<{ pane: PaneName, session: SessionState }>()
const { menu } = usePanes()
const { drag, startDrag } = usePaneLayout()

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

const HEADER = 44

// A press anywhere in the 44px header (its buttons and tabs too) becomes a drag once the pointer moves;
// a click without movement still reaches the button. Text fields are left alone.
function onPointerDown(e: PointerEvent) {
  if (e.button !== 0) return
  const y = e.clientY - (e.currentTarget as HTMLElement).getBoundingClientRect().top
  const field = (e.target as HTMLElement).closest('input, textarea, select, [contenteditable]')
  if (y <= HEADER && !field) startDrag(props.pane, e)
}

const region = computed(() => drag.value?.target === props.pane ? drag.value.region : null)

// The highlighted part of the target: the half the dragged pane will take, or the whole pane for a swap.
const highlight = computed(() => ({
  left: 'inset-y-0 left-0 w-1/2',
  right: 'inset-y-0 right-0 w-1/2',
  top: 'inset-x-0 top-0 h-1/2',
  bottom: 'inset-x-0 bottom-0 h-1/2',
  center: 'inset-0'
}[region.value ?? 'center']))
</script>

<template>
  <div
    class="relative size-full min-h-0 min-w-0 overflow-hidden bg-default"
    :class="drag?.src === pane && 'opacity-50'"
    :data-pane="pane"
    @pointerdown="onPointerDown"
  >
    <component
      :is="component"
      :session="session"
      :menu="menu"
    />
    <div
      v-if="region"
      class="pointer-events-none absolute z-50 border border-primary bg-primary/15 transition-all duration-100"
      :class="highlight"
    />
  </div>
</template>
